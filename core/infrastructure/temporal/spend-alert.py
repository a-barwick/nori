#!/usr/bin/env python3
"""Read Temporal's account billing CSV; metric publication is explicitly opt-in."""
import argparse
import csv
from datetime import datetime, timezone
from decimal import Decimal, InvalidOperation
import io
import json
import os
from pathlib import Path
import shlex
import ssl
import subprocess
import sys
import time
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlparse
from urllib.request import Request, urlopen
import uuid

API = "https://saas-api.tmprl.cloud"
API_VERSION = "v0.25.0"
METRIC_PREFIX = "custom.googleapis.com/nori/temporal_billing/"
MAX_CSV_BYTES = 10 * 1024 * 1024
TLS = ssl.create_default_context(cafile="/etc/ssl/cert.pem") if sys.platform == "darwin" and Path("/etc/ssl/cert.pem").is_file() else ssl.create_default_context()


def utc(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00")).astimezone(timezone.utc)


def stamp(value):
    return value.isoformat().replace("+00:00", "Z")


def billing_key(env_file):
    key = os.environ.get("TEMPORAL_BILLING_API_KEY", "")
    if not key and env_file and Path(env_file).is_file():
        for line in Path(env_file).read_text().splitlines():
            words = shlex.split(line, comments=True)
            if words and words[0] == "export":
                words = words[1:]
            if len(words) == 1 and words[0].startswith("TEMPORAL_BILLING_API_KEY="):
                key = words[0].split("=", 1)[1]
    if not key.strip():
        raise ValueError("TEMPORAL_BILLING_API_KEY is missing or empty in the selected environment file")
    return key


def request_json(method, url, headers, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = Request(url, data=data, method=method, headers=headers)
    try:
        with urlopen(req, timeout=30, context=TLS) as response:
            return json.load(response)
    except HTTPError as error:
        # Response bodies can include credentials or signed download URLs.
        raise RuntimeError(f"{method} API request failed: HTTP {error.code}") from None
    except (URLError, TimeoutError, json.JSONDecodeError):
        raise RuntimeError(f"{method} API request failed or returned unreadable JSON") from None


def report(key, start, end, report_id, timeout):
    headers = {"Authorization": "Bearer " + key, "Content-Type": "application/json", "temporal-cloud-api-version": API_VERSION}
    if not report_id:
        created = request_json("POST", API + "/cloud/billing-reports", headers, {
            "spec": {"startTimeInclusive": stamp(start), "endTimeExclusive": stamp(end), "granularity": "BILLING_REPORT_GRANULARITY_DAILY"},
            "asyncOperationId": str(uuid.uuid4()),
        })
        report_id = created.get("billingReportId")
        if not report_id:
            raise ValueError("Temporal did not return a billing report ID")
        print(json.dumps({"status": "report_requested", "report_id": report_id}), flush=True)
    deadline = time.monotonic() + timeout
    delay = 1
    while True:
        result = request_json("GET", API + "/cloud/billing-reports/" + quote(report_id, safe=""), headers).get("billingReport", {})
        state = result.get("state")
        if state == "BILLING_REPORT_STATE_GENERATED":
            spec = result.get("spec", {})
            if utc(spec["startTimeInclusive"]) != start or not start < utc(spec["endTimeExclusive"]) <= end:
                raise ValueError("Billing report does not cover the current UTC billing month")
            return result
        if state != "BILLING_REPORT_STATE_IN_PROGRESS":
            raise ValueError(f"Billing report {report_id} failed or has an unsupported state")
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError(f"Billing report {report_id} is still pending; reuse --report-id on the next check")
        time.sleep(min(delay, remaining))
        delay = min(delay * 2, 15)


def download_rows(metadata):
    rows = []
    files = metadata.get("downloadInfo", [])
    if not files or any(item.get("fileFormat") != "FILE_FORMAT_CSV" for item in files):
        raise ValueError("Billing report has no CSV download or an unsupported additional file format")
    required = {"BillingAccountID", "BillingCurrency", "BillingPeriodStart", "BillingPeriodEnd", "ChargePeriodStart", "ChargePeriodEnd", "ContractedCost", "ContractedUnitPrice", "PricingQuantity"}
    for item in files:
        url = item.get("url", "")
        parsed = urlparse(url)
        if parsed.scheme != "https" or not parsed.netloc or parsed.username or parsed.password:
            raise ValueError("Temporal returned an unsupported billing download URL")
        try:
            # The signed URL authenticates this request. Never send the account key.
            with urlopen(url, timeout=30, context=TLS) as response:
                content = response.read(MAX_CSV_BYTES + 1)
        except HTTPError as error:
            raise RuntimeError(f"Billing CSV download failed: HTTP {error.code}; download URLs may have expired") from None
        except (URLError, TimeoutError):
            raise RuntimeError("Billing CSV download connection failed") from None
        if len(content) > MAX_CSV_BYTES:
            raise ValueError("Billing CSV exceeds the 10 MiB safety limit")
        reader = csv.DictReader(io.StringIO(content.decode("utf-8-sig")))
        if not required.issubset(reader.fieldnames or []):
            raise ValueError("Billing CSV is missing required charge columns")
        rows.extend(reader)
    return rows


def summarize(metadata, rows, start, end, threshold, cost_unit):
    summary = {
        "status": "no_charge_records" if not rows else "ok",
        "scope": "Temporal account, all reported charge categories and namespaces",
        "billing_month": start.strftime("%Y-%m"), "report_id": metadata["id"],
        "requested_at": metadata.get("requestedTime"), "generated_at": metadata.get("generatedTime"),
        "checked_at": stamp(datetime.now(timezone.utc)), "rows": len(rows),
        "data_through_exclusive": metadata["spec"]["endTimeExclusive"],
        "threshold_usd": str(threshold), "contracted_cost_usd": None,
        "threshold_reached": None, "data_lag": "Current month is provisional; provider data lags at least 24 hours, rounded to daily granularity",
        "cost_unit": cost_unit, "unit_reference": "https://docs.temporal.io/cloud/billing-api",
    }
    if not rows:
        summary["detail"] = "The provider returned headers but no charge records; this does not establish a zero bill"
        return summary
    accounts, currencies = set(), set()
    total = Decimal(0)
    latest = None
    for row in rows:
        accounts.add(row["BillingAccountID"])
        currencies.add(row["BillingCurrency"].strip())
        if utc(row["BillingPeriodStart"]) != start or utc(row["BillingPeriodEnd"]) != end:
            raise ValueError("A charge belongs to a different billing month")
        charge_start, charge_end = utc(row["ChargePeriodStart"]), utc(row["ChargePeriodEnd"])
        if charge_start < start or charge_end > end or charge_end <= charge_start:
            raise ValueError("A charge has invalid current-month period bounds")
        latest = max(latest, charge_end) if latest else charge_end
        cost, unit_price, quantity = (Decimal(row[name]) for name in ("ContractedCost", "ContractedUnitPrice", "PricingQuantity"))
        if not all(value.is_finite() for value in (cost, unit_price, quantity)):
            raise ValueError("Billing CSV has a non-finite cost or quantity")
        # Allow the provider's displayed cost rounding, without guessing its unit.
        tolerance = max(Decimal("0.01"), abs(cost) * Decimal("0.000001"))
        if abs(cost - unit_price * quantity) > tolerance:
            raise ValueError("ContractedCost does not reconcile with ContractedUnitPrice × PricingQuantity")
        total += cost
    if len(accounts) != 1 or "" in accounts:
        raise ValueError("Billing report does not identify one complete account scope")
    if not currencies or not currencies.issubset({"USD", "USD (cents)"}):
        raise ValueError("Billing CSV currency is unsupported; no conversion or zero was assumed")
    summary.update({"billing_currency": sorted(currencies), "raw_contracted_cost": str(total), "latest_charge_period_end": stamp(latest)})
    if "USD (cents)" in currencies and cost_unit != "cents":
        raise ValueError("Explicit cents currency conflicts with the configured cost unit")
    total_usd = total / 100 if cost_unit == "cents" else total
    summary.update({"status": "ok", "contracted_cost_usd": str(total_usd), "threshold_reached": total_usd >= threshold})
    return summary


def google_token():
    try:
        return request_json("GET", "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", {"Metadata-Flavor": "Google"})["access_token"]
    except (RuntimeError, KeyError):
        try:
            return subprocess.check_output(["gcloud", "auth", "application-default", "print-access-token"], text=True, stderr=subprocess.DEVNULL).strip()
        except (OSError, subprocess.CalledProcessError):
            raise RuntimeError("Google ADC is unavailable for explicitly requested metric publication") from None


def publish(project, values):
    now = stamp(datetime.now(timezone.utc))
    series = [{"metric": {"type": METRIC_PREFIX + name}, "resource": {"type": "global", "labels": {"project_id": project}},
               "metricKind": "GAUGE", "valueType": "DOUBLE", "points": [{"interval": {"endTime": now}, "value": {"doubleValue": value}}]} for name, value in values.items()]
    request_json("POST", f"https://monitoring.googleapis.com/v3/projects/{quote(project, safe='')}/timeSeries",
                 {"Authorization": "Bearer " + google_token(), "Content-Type": "application/json"}, {"timeSeries": series})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="Read provider billing only (the safe default)")
    mode.add_argument("--publish", action="store_true", help="Also write Cloud Monitoring metrics; used only by the deployed Job")
    parser.add_argument("--env-file", default=os.getenv("NORI_ENV_FILE"), help="Existing root env file; read in place without copying credentials")
    parser.add_argument("--report-id", help="Reuse a previously requested current-month report")
    parser.add_argument("--threshold-usd", default="10")
    parser.add_argument("--cost-unit", choices=["cents", "dollars"], default="cents", help="ContractedCost unit; default cents follows the published Temporal schema")
    parser.add_argument("--poll-seconds", type=int, default=180)
    parser.add_argument("--project", default=os.getenv("NORI_PROJECT_ID", "bot-111549"))
    args = parser.parse_args()
    if args.publish and args.report_id:
        parser.error("--publish requires a newly generated report; --report-id is read-only historical inspection")
    try:
        threshold = Decimal(args.threshold_usd)
        if not threshold.is_finite() or threshold <= 0 or not 1 <= args.poll_seconds <= 300:
            raise ValueError("Threshold must be positive and poll-seconds must be 1–300")
        now = datetime.now(timezone.utc)
        start = now.replace(day=1, hour=0, minute=0, second=0, microsecond=0)
        end = start.replace(year=start.year + (start.month == 12), month=start.month % 12 + 1)
        metadata = report(billing_key(args.env_file), start, end, args.report_id, args.poll_seconds)
        result = summarize(metadata, download_rows(metadata), start, end, threshold, args.cost_unit)
        print(json.dumps(result), flush=True)
        if result["status"] != "ok":
            if args.publish:
                publish(args.project, {"check_ok": 0.0})
            return 2
        if args.publish:
            publish(args.project, {"month_to_date_usd": float(result["contracted_cost_usd"]), "threshold_reached": float(result["threshold_reached"]), "check_ok": 1.0})
        return 0
    except (ValueError, KeyError, InvalidOperation, RuntimeError, TimeoutError, UnicodeError, TypeError, csv.Error, OSError) as error:
        # Only our bounded diagnostic is logged; never URL, key, or provider body.
        print(json.dumps({"status": "error", "error": str(error), "contracted_cost_usd": None}), file=sys.stderr, flush=True)
        if args.publish:
            try:
                publish(args.project, {"check_ok": 0.0})
            except RuntimeError:
                print('{"status":"metric_publication_failed"}', file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
