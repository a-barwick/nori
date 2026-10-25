#!/usr/bin/env python3
"""Thin operator commands for the native GCP resources; no resident controller."""
import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import subprocess
import ssl
import sys
import time
from urllib.error import HTTPError
from urllib.parse import quote
from urllib.request import Request, urlopen

PROJECT = "bot-111549"
NUMBER = "8273801370"
REGION = "us-central1"
ZONE = "us-central1-a"
OWNER = "austinbarwickdavis@gmail.com"
WEB = "nori-web"
VM = "nori-computer"
BILLING = "010FFA-6C77F1-18FF6D"
SPEND_CAPS = {"Cloud Run": "ec91d69c-af6f-4582-8a57-fc5d74df3922", "Agent Platform": "c288b003-c839-4ef0-9327-7ae10d3e762b"}
ROOT = Path(__file__).resolve().parent
TF = ROOT / "terraform"
STATE = Path.home() / ".local/state/nori/bot-111549/poc/terraform.tfstate"
TEMPORAL_TF = ROOT.parent / "temporal/terraform"
TEMPORAL_STATE = STATE.parent.parent / "temporal/terraform.tfstate"
TEMPORAL_SESSION = TEMPORAL_STATE.parent / "session.tfvars.json"
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
    return stamp(datetime.now(timezone.utc) + timedelta(hours=3))


def temporal_config():
    return {k: v["value"] for k, v in json.loads(command(["terraform", f"-chdir={TEMPORAL_TF}", "output", "-state=" + str(TEMPORAL_STATE), "-json"])).items()}


def temporal_apply(session, build=None, enabled=False, verified=False):
    # Only session settings, never credentials, are saved outside the checkout.
    TEMPORAL_SESSION.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(TEMPORAL_SESSION, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as out:
        json.dump({"session": session, "active_build_id": build, "wci_enabled": enabled, "shutdown_verified": verified}, out)
    os.chmod(TEMPORAL_SESSION, 0o600)
    args = ["terraform", f"-chdir={TEMPORAL_TF}"]
    plan = TEMPORAL_STATE.parent / "session.plan"
    # Save and apply the same plan. Worker image changes remain a separate,
    # reviewed deployment; normal lifecycle uses the retained release manifest.
    print(command(args + ["plan", "-input=false", "-state=" + str(TEMPORAL_STATE), "-var-file=worker-releases.json", "-var-file=" + str(TEMPORAL_SESSION), "-out=" + str(plan)]))
    os.chmod(plan, 0o600)
    print(command(args + ["apply", "-input=false", str(plan)]))


def temporal_zero(t):
    failures = []
    for pool in t["worker_pools"].values():
        try:
            url = "https://run.googleapis.com/v2/" + pool["resource"]
            result = api("PATCH", url + "?updateMask=scaling.manualInstanceCount", {"scaling": {"manualInstanceCount": 0}})
            end = time.monotonic() + 600
            while not result.get("done"):
                if time.monotonic() > end:
                    raise RuntimeError("Pool scale-to-zero operation timed out: " + pool["name"])
                time.sleep(3)
                result = api("GET", "https://run.googleapis.com/v2/" + result["name"])
            if result.get("error"):
                raise RuntimeError("Pool scale-to-zero failed: " + pool["name"])
            current = api("GET", url)
            if current.get("reconciling") or current.get("scaling", {}).get("manualInstanceCount", 0) != 0 or current.get("observedGeneration") != current.get("generation") or current.get("terminalCondition", {}).get("state") != "CONDITION_SUCCEEDED":
                raise RuntimeError("Pool has not reconciled to zero: " + pool["name"])
        except Exception as error:
            failures.append(str(error))
    if failures:
        raise RuntimeError("; ".join(failures))


def temporal_access(t, active=None):
    sa = t["invoker_service_account"]
    end = time.monotonic() + 300
    while True:
        # IAM's full resource name uses lowercase workerpools; the owning Run
        # v2 API and Terraform IDs retain their camel-case workerPools path.
        decisions = {build: api("POST", "https://policytroubleshooter.googleapis.com/v3/iam:troubleshoot", {"accessTuple": {"principal": sa, "fullResourceName": "//run.googleapis.com/" + p["resource"].replace("/workerPools/", "/workerpools/"), "permission": "run.workerpools.update", "conditionContext": {"request": {"receiveTime": stamp(datetime.now(timezone.utc))}}}}).get("overallAccessState") for build, p in t["worker_pools"].items()}
        if all(decision == ("CAN_ACCESS" if build == active else "CANNOT_ACCESS") for build, decision in decisions.items()):
            return
        if time.monotonic() > end:
            raise RuntimeError("Effective WCI pool access not verified: " + repr(decisions))
        time.sleep(5)


def temporal_denied(t):
    sa = t["invoker_service_account"]
    state = api("GET", f"https://iam.googleapis.com/v1/projects/{PROJECT}/serviceAccounts/{sa}")
    if not state.get("disabled"):
        raise RuntimeError("Temporal WCI invoker is not disabled")
    temporal_access(t)


def temporal_pause():
    t = temporal_config()
    settings = t.get("session_settings") or (json.loads(TEMPORAL_SESSION.read_text()) if TEMPORAL_SESSION.exists() else {})
    failures = []
    # Independent fences still run if Terraform or an IAM verification fails.
    for step in (
        lambda: api("POST", f"https://iam.googleapis.com/v1/projects/{PROJECT}/serviceAccounts/{t['invoker_service_account']}:disable", {}),
        lambda: temporal_apply(settings.get("session"), settings.get("active_build_id")),
        lambda: temporal_zero(t),
        lambda: temporal_denied(t),
    ):
        try:
            step()
        except Exception as error:
            failures.append(str(error))
    if failures:
        raise RuntimeError("Temporal pause incomplete: " + "; ".join(failures))
    # Keep the scheduled backstop armed until shutdown and denial succeeded.
    temporal_apply(None)


def temporal_resume(session, build, verified):
    t = temporal_config()
    if not build or build not in t["worker_pools"]:
        raise RuntimeError("resume needs --build-id naming a deployed immutable Temporal pool")
    if not verified:
        raise RuntimeError("resume needs --shutdown-verified after the live Scheduler scale-to-zero proof")
    # Arm durable shutdown with WCI disabled before opening task admission.
    temporal_apply(session, build)
    temporal_zero(t)
    temporal_denied(t)
    for pool in t["worker_pools"].values():
        job = api("GET", "https://cloudscheduler.googleapis.com/v1/" + pool["shutdown_job"])
        scheduled = datetime.strptime(session["shutdown_at"], "%Y-%m-%dT%H:%M:%SZ")
        expected = scheduled.strftime("%M %H %d %m *")
        if job.get("state") != "ENABLED" or job.get("schedule") != expected or job.get("timeZone") != "Etc/UTC":
            raise RuntimeError("Cloud shutdown job not armed: " + pool["shutdown_job"])
    temporal_apply(session, build, enabled=True, verified=True)
    temporal_access(t, build)


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
    for step in (lambda: control(c, True), lambda: cancel_active(c), lambda: temporal_pause(), lambda: metadata({"computer-paused": "true"}), lambda: stop_vm()):
        try:
            step()
        except Exception as error:
            failures.append(str(error))
    if failures:
        raise RuntimeError("Pause incomplete: " + "; ".join(failures))
    print("Paused: durable control closed, active run terminal, task admission closed and WCI denied, every worker pool reconciled to zero, retained computer TERMINATED.")


def stop_vm():
    state = api("GET", vm_url())
    if state["status"] != "TERMINATED":
        operation(api("POST", vm_url() + "/stop", {}))
    if api("GET", vm_url())["status"] != "TERMINATED":
        raise RuntimeError("Computer did not stop")


def resume(c, build, verified):
    now = datetime.now(timezone.utc).replace(microsecond=0)
    hard = now + timedelta(hours=3)
    shutdown = (hard - timedelta(minutes=10)).replace(second=0)
    access = shutdown - timedelta(minutes=2)
    expires = stamp(access - timedelta(minutes=2))
    vm_deadline = stamp(hard)
    session = {"started_at": stamp(now), "access_expires_at": stamp(access), "shutdown_at": stamp(shutdown), "hard_deadline_at": vm_deadline}
    # Verify a usable custom OAuth client exists without printing credentials.
    settings = api("GET", f"https://iap.googleapis.com/v1/projects/{NUMBER}:iapSettings")
    oauth = settings.get("accessSettings", {}).get("oauthSettings", {})
    if not oauth.get("clientId"):
        raise RuntimeError("Custom IAP OAuth client is not configured; consumer Gmail access remains closed")
    state = api("GET", vm_url())
    if state["status"] != "TERMINATED":
        raise RuntimeError("Resume requires a stopped computer; pause/reconcile first")
    current = api("GET", firestore(c, "system/control"), allow404=True)
    if current and not current.get("fields", {}).get("paused", {}).get("booleanValue", True):
        raise RuntimeError("Resume requires paused durable control; pause/reconcile the previous session first")
    # Install the cloud-side STOP deadline before opening task admission.
    operation(api("POST", vm_url() + "/setScheduling", {"automaticRestart": False, "onHostMaintenance": "TERMINATE", "instanceTerminationAction": "STOP", "terminationTime": vm_deadline}))
    installed = api("GET", vm_url())["scheduling"]
    if installed.get("automaticRestart") or installed.get("instanceTerminationAction") != "STOP" or datetime.fromisoformat(installed["terminationTime"].replace("Z", "+00:00")) != hard:
        raise RuntimeError("VM cloud-side STOP deadline was not verified")
    try:
        temporal_resume(session, build, verified)
        metadata({"computer-paused": "false"})
        control(c, False, expires)
    except Exception:
        pause(c)
        raise
    print("Resumed until " + expires + "; WCI expires " + stamp(access) + ", worker shutdown " + stamp(shutdown) + ", VM STOP " + vm_deadline + ". Computer remains stopped until a tool needs it.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["deadline", "bootstrap-identities", "publish-computer", "deploy-runtime", "deploy-web", "resume", "pause", "status", "spend-caps"])
    parser.add_argument("value", nargs="?")
    parser.add_argument("--build-id", help="deployed immutable Temporal build to admit for this session")
    parser.add_argument("--shutdown-verified", action="store_true", help="attest the documented live Scheduler scale-to-zero proof succeeded")
    args = parser.parse_args()
    if args.action == "deadline":
        print(deadline()); return
    if args.action == "resume" and (not args.build_id or not args.shutdown_verified):
        parser.error("resume requires --build-id BUILD --shutdown-verified; complete the documented live shutdown proof first")
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
        base = f"https://{REGION}-aiplatform.googleapis.com/v1beta1/{name}"
        current = api("GET", base)
        env = {entry["name"]: entry for entry in current.get("spec", {}).get("deploymentSpec", {}).get("env", [])}
        env.update({key: {"name": key, "value": value} for key, value in {
            "NORI_EXECUTION_MODE": "temporal",
            "TEMPORAL_ADDRESS": "quickstart-nori.qtjow.tmprl.cloud:7233",
            "TEMPORAL_NAMESPACE": "quickstart-nori.qtjow",
            "TEMPORAL_TASK_QUEUE": "nori-tasks",
            "TEMPORAL_API_KEY_SECRET_VERSION": f"projects/{PROJECT}/secrets/nori-temporal-api-key/versions/latest",
        }.items()})
        operation(api("PATCH", base + "?updateMask=spec.containerSpec.imageUri,spec.deploymentSpec.env", {"spec": {"containerSpec": {"imageUri": args.value, "port": 8080}, "deploymentSpec": {"env": list(env.values())}}}), "vertex")
        print("Runtime image updated.")
    elif args.action == "deploy-web":
        if not args.value: parser.error("deploy-web needs an immutable image URI")
        print(gcloud("run", "services", "update", WEB, "--region=" + REGION, "--image=" + args.value))
    elif args.action == "resume":
        resume(c, args.build_id, args.shutdown_verified)
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
