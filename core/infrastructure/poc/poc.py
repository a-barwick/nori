#!/usr/bin/env python3
"""Thin operator commands for the native GCP resources; no resident controller."""
import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import subprocess
import ssl
import sys
import time
from urllib.error import HTTPError
from urllib.parse import quote
from urllib.request import Request, urlopen
from zoneinfo import ZoneInfo

PROJECT = "bot-111549"
NUMBER = "8273801370"
REGION = "us-central1"
ZONE = "us-central1-a"
OWNER = "austinbarwickdavis@gmail.com"
WEB = "nori-web"
VM = "nori-computer"
IAP_SA = f"service-{NUMBER}@gcp-sa-iap.iam.gserviceaccount.com"
BILLING = "010FFA-6C77F1-18FF6D"
SPEND_CAPS = {"Cloud Run": "ec91d69c-af6f-4582-8a57-fc5d74df3922", "Agent Platform": "c288b003-c839-4ef0-9327-7ae10d3e762b"}
ROOT = Path(__file__).resolve().parent
TF = ROOT / "terraform"
STATE = Path.home() / ".local/state/nori/bot-111549/poc/terraform.tfstate"
TOKEN = None
SSL_CONTEXT = ssl.create_default_context(cafile="/etc/ssl/cert.pem") if sys.platform == "darwin" and Path("/etc/ssl/cert.pem").is_file() else ssl.create_default_context()


def command(args, **kwargs):
    return subprocess.check_output(args, text=True, **kwargs).strip()


def gcloud(*args):
    return command(["gcloud", "--project", PROJECT, "--quiet", *args])


def api(method, url, body=None, allow404=False):
    global TOKEN
    if TOKEN is None:
        TOKEN = gcloud("auth", "print-access-token")
    data = None if body is None else json.dumps(body).encode()
    req = Request(url, data=data, method=method, headers={"Authorization": "Bearer " + TOKEN, "Content-Type": "application/json", "x-goog-user-project": PROJECT})
    try:
        with urlopen(req, timeout=135, context=SSL_CONTEXT) as res:
            data = res.read()
            return json.loads(data) if data else {}
    except HTTPError as error:
        if allow404 and error.code == 404:
            return None
        # Never dump a response that might contain IAP OAuth settings.
        raise RuntimeError(f"{method} {url.split('?')[0]} failed: HTTP {error.code}") from None


def config():
    return {k: v["value"] for k, v in json.loads(command(["terraform", f"-chdir={TF}", "output", "-state=" + str(STATE), "-json"])).items()}


def stamp(value):
    return value.astimezone(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def deadline():
    now = datetime.now(timezone.utc)
    cutoff = now.astimezone(ZoneInfo("America/Los_Angeles")).replace(hour=22, minute=0, second=0, microsecond=0)
    if now >= cutoff:
        raise RuntimeError("22:00 America/Los_Angeles cutoff has passed; no resume or paid start allowed")
    return stamp(min(now + timedelta(hours=3), cutoff))


def operation(result, service="compute"):
    if not result.get("name"):
        return result
    if service == "compute":
        url = result.get("selfLink") or f"https://compute.googleapis.com/compute/v1/projects/{PROJECT}/zones/{ZONE}/operations/{result['name']}"
        done = lambda j: j.get("status") == "DONE"
    elif service == "serviceusage":
        url = "https://serviceusage.googleapis.com/v1beta1/" + result["name"]
        done = lambda j: j.get("done")
    else:
        url = f"https://{REGION}-aiplatform.googleapis.com/v1beta1/{result['name']}"
        done = lambda j: j.get("done")
    end = time.monotonic() + 900
    while not done(result):
        if time.monotonic() > end:
            raise RuntimeError("Cloud operation exceeded 15 minutes; inspect before retrying")
        time.sleep(3)
        result = api("GET", url)
    if result.get("error"):
        raise RuntimeError("Cloud operation failed; inspect native operation " + result["name"])
    return result


def vm_url():
    return f"https://compute.googleapis.com/compute/v1/projects/{PROJECT}/zones/{ZONE}/instances/{VM}"


def metadata(changes):
    current = api("GET", vm_url())["metadata"]
    values = {entry["key"]: entry["value"] for entry in current.get("items", [])}
    values.update(changes)
    operation(api("POST", vm_url() + "/setMetadata", {"fingerprint": current["fingerprint"], "items": [{"key": k, "value": v} for k, v in values.items()]}))


def firestore(c, path):
    return f"https://firestore.googleapis.com/v1/projects/{PROJECT}/databases/{quote(c['firestore_database'], safe='')}/documents/{path}"


def control(c, paused, expires=None):
    fields = {"paused": {"booleanValue": paused}, "expires_at": {"stringValue": expires or stamp(datetime.now(timezone.utc))}}
    return api("PATCH", firestore(c, "system/control"), {"fields": fields})


def policy(kind, role, member, expires=None):
    if kind == "project":
        base = f"https://cloudresourcemanager.googleapis.com/v1/projects/{PROJECT}"
        current = api("POST", base + ":getIamPolicy", {"options": {"requestedPolicyVersion": 3}})
    elif kind == "run":
        base = f"https://run.googleapis.com/v2/projects/{PROJECT}/locations/{REGION}/services/{WEB}"
        current = api("GET", base + ":getIamPolicy?options.requestedPolicyVersion=3")
    elif kind == "iap":
        base = f"https://iap.googleapis.com/v1/projects/{NUMBER}/iap_web/cloud_run-{REGION}/services/{WEB}"
        current = api("POST", base + ":getIamPolicy", {"options": {"requestedPolicyVersion": 3}})
    bindings = []
    for binding in current.get("bindings", []):
        if binding["role"] == role:
            binding["members"] = [m for m in binding.get("members", []) if m != member]
        if binding.get("members"):
            bindings.append(binding)
    if expires:
        bindings.append({"role": role, "members": [member], "condition": {"title": "nori-poc-lease", "expression": f"request.time < timestamp('{expires}')"}})
    current.update(version=3, bindings=bindings)
    api("POST", base + ":setIamPolicy", {"policy": current})


def grants(c, expires=None):
    # Close owner access first on pause; open it last on resume.
    specs = [("project", c["query_role"], "serviceAccount:" + c["web_service_account"]), ("run", "roles/run.invoker", "serviceAccount:" + IAP_SA), ("iap", "roles/iap.httpsResourceAccessor", "user:" + OWNER)]
    if expires is None:
        specs.reverse()
    failures = []
    for kind, role, member in specs:
        try:
            policy(kind, role, member, expires)
        except Exception as error:
            failures.append(str(error))
    if failures:
        raise RuntimeError("; ".join(failures))


def denied(c):
    checks = [(c["web_service_account"], "//aiplatform.googleapis.com/" + c["runtime_name"], "aiplatform.reasoningEngines.query"), (IAP_SA, f"//run.googleapis.com/projects/{PROJECT}/locations/{REGION}/services/{WEB}", "run.routes.invoke"), (OWNER, f"//iap.googleapis.com/projects/{NUMBER}/iap_web/cloud_run-{REGION}/services/{WEB}", "iap.webServiceVersions.accessViaIAP")]
    end = time.monotonic() + 300
    while True:
        states = []
        for principal, resource, permission in checks:
            result = api("POST", "https://policytroubleshooter.googleapis.com/v3/iam:troubleshoot", {"accessTuple": {"principal": principal, "fullResourceName": resource, "permission": permission, "conditionContext": {"request": {"receiveTime": stamp(datetime.now(timezone.utc))}}}})
            states.append(result.get("overallAccessState"))
        if all(state == "CANNOT_ACCESS" for state in states):
            return
        if time.monotonic() > end:
            raise RuntimeError("Invocation denial not verified: " + repr(states) + "; inspect inherited IAM before claiming paused")
        time.sleep(5)


def cancel_active(c):
    owner_path = "owners/" + quote(OWNER, safe="")
    owner = api("GET", firestore(c, owner_path), allow404=True)
    run_id = (owner or {}).get("fields", {}).get("active_run", {}).get("stringValue")
    if not run_id:
        return
    run_path = owner_path + "/runs/" + quote(run_id, safe="")
    current = api("GET", firestore(c, run_path), allow404=True)
    if not current or current.get("fields", {}).get("status", {}).get("stringValue") != "running":
        return
    # The pause watcher also cancels work. Mark durable cancellation before invoking.
    api("PATCH", firestore(c, run_path) + "?updateMask.fieldPaths=cancel_requested", {"fields": {"cancel_requested": {"booleanValue": True}}})
    try:
        api("POST", c["runtime_url"].rstrip("/") + "/api/cancel", {"id": run_id})
    except Exception as error:
        print("Cancellation endpoint unavailable; waiting for bounded active run: " + str(error), file=sys.stderr)
    end = time.monotonic() + 130
    while time.monotonic() < end:
        run = api("GET", firestore(c, run_path), allow404=True)
        if run and run.get("fields", {}).get("status", {}).get("stringValue") in ("completed", "failed", "cancelled"):
            return
        time.sleep(1)
    raise RuntimeError("Active run did not become terminal; stop VM and inspect before claiming paused")


def pause(c):
    failures = []
    # Attempt every closure even if one fails. Never report success on partial closure.
    for step in (lambda: control(c, True), lambda: cancel_active(c), lambda: grants(c), lambda: metadata({"computer-paused": "true"}), lambda: stop_vm(), lambda: denied(c)):
        try:
            step()
        except Exception as error:
            failures.append(str(error))
    if failures:
        raise RuntimeError("Pause incomplete: " + "; ".join(failures))
    print("Paused: durable control closed, active run terminal, invocation denied, retained computer TERMINATED.")


def stop_vm():
    state = api("GET", vm_url())
    if state["status"] != "TERMINATED":
        operation(api("POST", vm_url() + "/stop", {}))
    if api("GET", vm_url())["status"] != "TERMINATED":
        raise RuntimeError("Computer did not stop")


def resume(c):
    vm_deadline = deadline()
    expires = stamp(datetime.fromisoformat(vm_deadline.replace("Z", "+00:00")) - timedelta(seconds=20))
    if datetime.now(timezone.utc) >= datetime.fromisoformat(expires.replace("Z", "+00:00")):
        raise RuntimeError("Not enough time remains before cutoff for cancellation cleanup")
    # Verify a usable custom OAuth client exists without printing credentials.
    settings = api("GET", f"https://iap.googleapis.com/v1/projects/{NUMBER}:iapSettings")
    oauth = settings.get("accessSettings", {}).get("oauthSettings", {})
    if not oauth.get("clientId"):
        raise RuntimeError("Custom IAP OAuth client is not configured; consumer Gmail access remains closed")
    state = api("GET", vm_url())
    if state["status"] != "TERMINATED":
        raise RuntimeError("Resume requires a stopped computer; pause/reconcile first")
    # Cloud-side STOP deadline is installed before invocation grants can wake anything.
    operation(api("POST", vm_url() + "/setScheduling", {"automaticRestart": False, "onHostMaintenance": "TERMINATE", "instanceTerminationAction": "STOP", "terminationTime": vm_deadline}))
    metadata({"computer-paused": "false"})
    try:
        control(c, False, expires)
        grants(c, expires)
    except Exception:
        pause(c)
        raise
    print("Resumed until " + expires + ". Computer remains stopped until a tool needs it.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["deadline", "bootstrap-identities", "publish-computer", "deploy-runtime", "deploy-web", "resume", "pause", "status", "spend-caps"])
    parser.add_argument("value", nargs="?")
    args = parser.parse_args()
    if args.action == "deadline":
        print(deadline()); return
    if args.action == "bootstrap-identities":
        for service in ("aiplatform.googleapis.com", "iap.googleapis.com", "run.googleapis.com"):
            result = operation(api("POST", f"https://serviceusage.googleapis.com/v1beta1/projects/{NUMBER}/services/{service}:generateServiceIdentity", {}), "serviceusage")
            print(service + ": " + result.get("response", {}).get("email", "identity ready"))
        return
    if args.action == "spend-caps":
        for label, cap_id in SPEND_CAPS.items():
            result = api("GET", f"https://billingbudgets.googleapis.com/v1/billingAccounts/{BILLING}/budgets/{cap_id}")
            print(json.dumps({"service": label, "budget_id": cap_id, "spend_cap": result.get("spendCap")}))
        return
    c = config()
    if args.action == "publish-computer":
        if not args.value: parser.error("publish-computer needs a built binary path")
        binary = Path(args.value).resolve()
        digest = hashlib.sha256(binary.read_bytes()).hexdigest()
        object_name = "releases/computer-agent-" + digest
        print(gcloud("storage", "cp", str(binary), f"gs://{c['artifact_bucket']}/{object_name}"))
        metadata({"computer-release-object": c["artifact_bucket"] + "/" + object_name, "computer-release-sha256": digest})
        print("Computer release " + digest + "; restart the stopped host on next use to install.")
    elif args.action == "deploy-runtime":
        if not args.value: parser.error("deploy-runtime needs an immutable image URI")
        name = c["runtime_name"]
        operation(api("PATCH", f"https://{REGION}-aiplatform.googleapis.com/v1beta1/{name}?updateMask=spec.containerSpec.imageUri", {"spec": {"containerSpec": {"imageUri": args.value, "port": 8080}}}), "vertex")
        print("Runtime image updated.")
    elif args.action == "deploy-web":
        if not args.value: parser.error("deploy-web needs an immutable image URI")
        print(gcloud("run", "services", "update", WEB, "--region=" + REGION, "--image=" + args.value))
    elif args.action == "resume":
        resume(c)
    elif args.action == "pause":
        pause(c)
    elif args.action == "status":
        vm = api("GET", vm_url())
        print(json.dumps({"control": api("GET", firestore(c, "system/control"), allow404=True), "computer": {"status": vm["status"], "scheduling": vm["scheduling"]}, "web_url": c["web_url"]}, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError) as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
