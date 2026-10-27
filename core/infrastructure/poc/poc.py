#!/usr/bin/env python3
"""Thin operator commands for the native GCP resources; no resident controller."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import shlex
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
WEB = "nori-web"
VM = "nori-computer"
BILLING = "010FFA-6C77F1-18FF6D"
SPEND_CAPS = {"Cloud Run": "ec91d69c-af6f-4582-8a57-fc5d74df3922", "Agent Platform": "c288b003-c839-4ef0-9327-7ae10d3e762b"}
ROOT = Path(__file__).resolve().parent
TF = ROOT / "terraform"
STATE = Path.home() / ".local/state/nori/bot-111549/poc/terraform.tfstate"
TEMPORAL_TF = ROOT.parent / "temporal/terraform"
TEMPORAL_STATE = STATE.parent.parent / "temporal/terraform.tfstate"
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


def temporal_config():
    return {k: v["value"] for k, v in json.loads(command(["terraform", f"-chdir={TEMPORAL_TF}", "output", "-state=" + str(TEMPORAL_STATE), "-json"])).items()}


def temporal_env():
    env = os.environ.copy()
    # Read the existing project env file in memory; never copy its credentials.
    env_file = Path(env.get("NORI_ENV_FILE", str(ROOT.parents[2] / ".env")))
    if env_file.is_file():
        for line in env_file.read_text().splitlines():
            words = shlex.split(line, comments=True)
            if words and words[0] == "export":
                words = words[1:]
            if len(words) == 1 and "=" in words[0]:
                key, value = words[0].split("=", 1)
                env.setdefault(key, value)
    if not env.get("TEMPORAL_API_KEY"):
        raise RuntimeError("TEMPORAL_API_KEY is missing; configure it in the project root .env (or select the existing file with NORI_ENV_FILE)")
    env.setdefault("TEMPORAL_ADDRESS", "quickstart-nori.qtjow.tmprl.cloud:7233")
    env.setdefault("TEMPORAL_NAMESPACE", "quickstart-nori.qtjow")
    return env


class VersionNotFound(RuntimeError):
    pass


def temporal_command(*args):
    # Authentication is passed through the child environment, never argv/logs.
    result = subprocess.run(["temporal", "--output=json", "--command-timeout=60s", *args], env=temporal_env(), text=True, capture_output=True)
    if result.returncode:
        # Confirmed CLI response for a nonexistent registered version. Other
        # failures (including auth and unavailable service) remain failures.
        if args[:3] == ("worker", "deployment", "describe-version") and result.stderr.strip().endswith(": Worker Deployment Version not found"):
            raise VersionNotFound("Worker deployment version is not registered")
        raise RuntimeError("Temporal " + " ".join(args[:3]) + " failed; exit " + str(result.returncode))
    return json.loads(result.stdout) if result.stdout.strip() else {}


def temporal(*args):
    return temporal_command("worker", "deployment", *args)


def version_config(build, pool, invoker):
    return ["--deployment-name=nori-task", "--build-id=" + build,
            "--gcp-cloud-run-project=" + PROJECT, "--gcp-cloud-run-region=us-west1",
            "--gcp-cloud-run-worker-pool=" + pool["name"],
            "--gcp-cloud-run-service-account=" + invoker,
            "--gcp-cloud-run-min-instances=0", "--gcp-cloud-run-max-instances=1",
            "--gcp-cloud-run-initial-instances=0", "--gcp-cloud-run-utilization-target=0.8",
            "--gcp-cloud-run-scale-down-stabilization-duration=90s"]


def version_info(build, missing_ok=False):
    try:
        result = temporal("describe-version", "--deployment-name=nori-task", "--build-id=" + build)
    except VersionNotFound:
        if missing_ok:
            return None
        raise
    return result.get("workerDeploymentVersionInfo", result.get("worker_deployment_version_info", result))


def compute_config(info):
    return (info or {}).get("computeConfig", (info or {}).get("compute_config"))


def compatible_builds(t):
    builds = set(t.get("scalable_build_ids", []))
    if not builds or not builds.issubset(t["worker_pools"]):
        raise RuntimeError("No valid sessionless worker release is configured; prepare and register a compatible build first")
    deployment = temporal("describe", "--name=nori-task")
    routing = deployment.get("routingConfig", {})
    current = routing.get("currentVersionBuildID") or routing.get("currentDeploymentVersion", {}).get("buildId")
    if current not in builds:
        raise RuntimeError("Current worker build is not marked compatible: " + str(current))
    ramp = routing.get("rampingVersionBuildID") or routing.get("rampingDeploymentVersion", {}).get("buildId")
    if routing.get("rampingVersionPercentage", 0) and ramp not in builds:
        raise RuntimeError("Ramping worker build is not marked compatible: " + str(ramp))
    # Visibility gives the candidates; Describe supplies the pinned version.
    # Unknown assignments and explicit overrides need review, never migration
    # or cancellation as a side effect of opening admission.
    running = temporal_command("workflow", "list", '--query=ExecutionStatus = "Running" AND TaskQueue = "nori-tasks"')
    if not isinstance(running, list):
        raise RuntimeError("Cannot verify open workflow compatibility")
    for item in running:
        execution = item["execution"]
        details = temporal_command("workflow", "describe", "--workflow-id=" + execution["workflowId"], "--run-id=" + execution["runId"])
        info = details.get("workflowExecutionInfo", {})
        status = info.get("status")
        if status in {"WORKFLOW_EXECUTION_STATUS_COMPLETED", "WORKFLOW_EXECUTION_STATUS_FAILED", "WORKFLOW_EXECUTION_STATUS_CANCELED", "WORKFLOW_EXECUTION_STATUS_TERMINATED", "WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW", "WORKFLOW_EXECUTION_STATUS_TIMED_OUT"}:
            continue
        if status != "WORKFLOW_EXECUTION_STATUS_RUNNING":
            raise RuntimeError("Cannot verify open workflow status: " + execution["workflowId"])
        versioning = info.get("versioningInfo", {})
        version = versioning.get("deploymentVersion", {})
        if not versioning:
            # A task queued before its first worker completed a workflow task
            # has no pinned code/history yet; it safely inherits compatible Current.
            history = temporal_command("workflow", "show", "--workflow-id=" + execution["workflowId"], "--run-id=" + execution["runId"])
            events = history.get("events") if isinstance(history, dict) else history
            queued_events = {"EVENT_TYPE_WORKFLOW_EXECUTION_STARTED", "EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED", "EVENT_TYPE_WORKFLOW_TASK_SCHEDULED", "EVENT_TYPE_WORKFLOW_TASK_STARTED", "EVENT_TYPE_WORKFLOW_TASK_TIMED_OUT", "EVENT_TYPE_WORKFLOW_TASK_FAILED"}
            if isinstance(events, list) and events and all(event.get("eventType") in queued_events for event in events):
                continue
        if versioning.get("versioningOverride") or version.get("deploymentName") != "nori-task" or version.get("buildId") not in builds:
            raise RuntimeError("Open workflow uses an incompatible or unknown worker build: " + execution["workflowId"])
    return builds


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


def temporal_pause():
    t = temporal_config()
    failures = []
    # Remove the native scaler configuration first; a zero-count PATCH alone
    # races WCI. Pinned workflows and their retained pools are not deleted.
    for build in t["worker_pools"]:
        try:
            if version_info(build, missing_ok=True) is None:
                continue # A newly staged zero-count pool may precede registration.
            temporal("update-version-compute-config", "--deployment-name=nori-task", "--build-id=" + build, "--remove")
            if compute_config(version_info(build, missing_ok=True)):
                raise RuntimeError("Worker compute configuration still present: " + build)
        except Exception as error:
            failures.append(str(error))
    # Attempt zeroing even when a fence failed, but never claim pause succeeded.
    try:
        temporal_zero(t)
        # Recheck after outstanding Cloud Run updates have reconciled.
        for build in t["worker_pools"]:
            if compute_config(version_info(build, missing_ok=True)):
                raise RuntimeError("Worker scaler was restored during pause: " + build)
        temporal_zero(t)
    except Exception as error:
        failures.append(str(error))
    if failures:
        raise RuntimeError("Worker pause incomplete: " + "; ".join(failures))


def temporal_unpause():
    t = temporal_config()
    builds = compatible_builds(t)
    # Legacy immutable workers cancel tasks on missing session expiry. Retain
    # their pools/images but never wake their binaries under the new control.
    for build, pool in t["worker_pools"].items():
        if build not in builds:
            if compute_config(version_info(build, missing_ok=True)):
                raise RuntimeError("Incompatible worker still has a scaler; pause before unpause: " + build)
            continue
        temporal("update-version-compute-config", *version_config(build, pool, t["invoker_service_account"]))
        config = compute_config(version_info(build)) or {}
        scaler = config.get("scalingGroups", {}).get("default", {}).get("scaler", {})
        if (scaler.get("minInstances"), scaler.get("maxInstances"), scaler.get("initialInstances")) != (0, 1, 0):
            raise RuntimeError("Worker scaling bounds not verified: " + build)


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


def control(c, paused):
    # Omitted expires_at in the update mask removes the obsolete admission field.
    return api("PATCH", firestore(c, "system/control") + "?updateMask.fieldPaths=paused&updateMask.fieldPaths=expires_at", {"fields": {"paused": {"booleanValue": paused}}})


def pause(c):
    failures = []
    # Admission closes before pool shutdown. Waiting tasks remain durable.
    for step in (lambda: control(c, True), temporal_pause, lambda: metadata({"computer-paused": "true"}), stop_vm):
        try:
            step()
        except Exception as error:
            failures.append(str(error))
    if failures:
        raise RuntimeError("Pause incomplete: " + "; ".join(failures))
    print("Paused: new work blocked, worker scalers removed, all retained pools at zero, computer stopped; waiting tasks preserved.")


def stop_vm():
    state = api("GET", vm_url())
    if state["status"] != "TERMINATED":
        operation(api("POST", vm_url() + "/stop", {}))
    if api("GET", vm_url())["status"] != "TERMINATED":
        raise RuntimeError("Computer did not stop")


def verify_no_deadline():
    scheduling = api("GET", vm_url())["scheduling"]
    if scheduling.get("terminationTime") or scheduling.get("maxRunDuration"):
        raise RuntimeError("Legacy VM termination schedule remains; run clear-deadline once while paused before unpause")
    if scheduling.get("automaticRestart"):
        raise RuntimeError("VM automatic restart must remain disabled")


def clear_deadline():
    if api("GET", vm_url())["status"] != "TERMINATED":
        raise RuntimeError("Clear the legacy deadline only on the stopped retained computer; pause first")
    # Google provider 8.8.1 marks termination_time ForceNew. Native scheduling
    # updates clear it in place; never replace this retained VM to remove it.
    print(gcloud("compute", "instances", "set-scheduling", VM, "--zone=" + ZONE,
                 "--clear-termination-time", "--clear-max-run-duration",
                 "--clear-instance-termination-action", "--no-restart-on-failure",
                 "--maintenance-policy=TERMINATE"))
    verify_no_deadline()
    print("Legacy VM deadline cleared in place; retained VM and disks preserved.")


def unpause(c):
    verify_no_deadline()
    # Keep application admission closed until native worker scaling is restored.
    control(c, True)
    try:
        temporal_unpause()
        metadata({"computer-paused": "false"})
        control(c, False)
        api("POST", c["runtime_url"].rstrip("/") + "/api/resume", {})
    except Exception:
        pause(c)
        raise
    print("Unpaused: tasks may run; the computer starts only when a tool needs it.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["bootstrap-identities", "publish-computer", "deploy-runtime", "deploy-web", "unpause", "pause", "clear-deadline", "status", "spend-caps"])
    parser.add_argument("value", nargs="?")
    args = parser.parse_args()
    if args.action == "bootstrap-identities":
        for service in ("aiplatform.googleapis.com", "iap.googleapis.com", "run.googleapis.com", "cloudtasks.googleapis.com"):
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
        required = ("conversation_queue", "conversation_wake_service_account", "outer_model")
        if any(not c.get(key) for key in required):
            raise RuntimeError("Apply the Milestone 2 queue, IAM and secret-reference infrastructure before deploying the runtime")
        name = c["runtime_name"]
        base = f"https://{REGION}-aiplatform.googleapis.com/v1beta1/{name}"
        current = api("GET", base)
        env = {entry["name"]: entry for entry in current.get("spec", {}).get("deploymentSpec", {}).get("env", [])}
        env.update({key: {"name": key, "value": value} for key, value in {
            "NORI_AGENT_MODEL": c["outer_model"],
            "NORI_AGENT_BASE_URL": "https://openrouter.ai/api/v1",
            "NORI_WAKE_QUEUE": c["conversation_queue"],
            "NORI_WAKE_SERVICE_ACCOUNT": c["conversation_wake_service_account"],
            "NORI_RUNTIME_URL": c["runtime_url"],
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
    elif args.action == "clear-deadline":
        clear_deadline()
    elif args.action == "unpause":
        unpause(c)
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
