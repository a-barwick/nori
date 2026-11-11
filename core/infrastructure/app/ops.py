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
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen

PROJECT = "bot-111549"
NUMBER = "8273801370"
REGION = "us-central1"
ZONE = "us-central1-a"
ENVIRONMENT = os.environ.get("NORI_ENVIRONMENT", "production")
if ENVIRONMENT not in {"production", "sandbox"}:
    raise RuntimeError("NORI_ENVIRONMENT must be production or sandbox")
PREFIX = "nori" if ENVIRONMENT == "production" else "nori-sandbox"
WEB = PREFIX + "-web"
VM = PREFIX + "-computer"
DEPLOYMENT = PREFIX + "-task"
TASK_QUEUE = PREFIX + "-tasks"
BILLING = "010FFA-6C77F1-18FF6D"
SPEND_CAPS = {"Cloud Run": "ec91d69c-af6f-4582-8a57-fc5d74df3922", "Agent Platform": "c288b003-c839-4ef0-9327-7ae10d3e762b"}
ROOT = Path(__file__).resolve().parent
TF = ROOT / "terraform"
STATE_ROOT = Path.home() / ".local/state/nori/bot-111549"
if ENVIRONMENT == "sandbox":
    STATE_ROOT /= "sandbox"
# The state path and the GCS prefix (nori/poc) keep their original names. Renaming them would orphan existing state.
STATE = STATE_ROOT / "poc/terraform.tfstate"
TEMPORAL_TF = ROOT.parent / "temporal/terraform"
TEMPORAL_STATE = STATE.parent.parent / "temporal/terraform.tfstate"
TOKEN = None
SSL_CONTEXT = ssl.create_default_context(cafile="/etc/ssl/cert.pem") if sys.platform == "darwin" and Path("/etc/ssl/cert.pem").is_file() else ssl.create_default_context()


class APIError(RuntimeError):
    def __init__(self, method, url, status):
        # Responses can contain IAP OAuth settings. Report the resource and status only.
        super().__init__(f"{method} {url.split('?')[0]} failed: HTTP {status}")
        self.status = status


def command(args, **kwargs):
    return subprocess.check_output(args, text=True, timeout=900, **kwargs).strip()


def gcloud(*args):
    return command(["gcloud", "--project", PROJECT, "--quiet", *args])


def api(method, url, body=None, allow404=False, refresh=True):
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
        if error.code == 401 and refresh:
            TOKEN = None
            return api(method, url, body, allow404, refresh=False)
        if allow404 and error.code == 404:
            return None
        raise APIError(method, url, error.code) from None


def state_args(state):
    backend = os.environ.get("NORI_TF_BACKEND", "local")
    if backend not in {"local", "gcs"}:
        raise RuntimeError("NORI_TF_BACKEND must be local or gcs")
    return [] if backend == "gcs" else ["-state=" + str(state)]


def terraform_outputs(directory, state):
    result = command(["terraform", f"-chdir={directory}", "output", *state_args(state), "-json"])
    return {key: output["value"] for key, output in json.loads(result).items()}


def config():
    value = terraform_outputs(TF, STATE)
    expected = "nori-poc" if ENVIRONMENT == "production" else "nori-sandbox"
    if value.get("firestore_database") != expected or value.get("artifact_bucket") != PROJECT + "-" + expected:
        raise RuntimeError("Application state does not match the selected environment")
    if ENVIRONMENT == "sandbox" and (value.get("environment") != "sandbox" or value.get("computer_instance") != VM):
        raise RuntimeError("Sandbox state has not been initialized and reviewed")
    return value


def runtime_settings():
    """Capacity settings shared with Terraform, read from this checkout, not old state."""
    settings = json.loads((ROOT / "runtime-settings.json").read_text())
    if (set(settings) != {"minInstances", "maxInstances", "containerConcurrency", "resourceLimits"} or
            type(settings["minInstances"]) is not int or settings["minInstances"] != 0 or
            type(settings["maxInstances"]) is not int or settings["maxInstances"] != 1 or
            type(settings["containerConcurrency"]) is not int or settings["containerConcurrency"] < 1 or
            not isinstance(settings["resourceLimits"], dict) or set(settings["resourceLimits"]) != {"cpu", "memory"} or
            not all(isinstance(value, str) and value for value in settings["resourceLimits"].values())):
        raise RuntimeError("Invalid runtime-settings.json: require minInstances=0, maxInstances=1, positive concurrency and CPU/memory limits")
    return settings


def runtime_deployment(runtime):
    """Snapshot only fields the release owns; normalize API list order and omitted zero."""
    deployment = runtime["spec"].get("deploymentSpec", {})
    result = {key: deployment.get(key, 0 if key == "minInstances" else None) for key in runtime_settings()}
    for key in ("env", "secretEnv"):
        result[key] = sorted(deployment.get(key, []), key=lambda entry: entry["name"])
    return result


def patch_runtime(c, image, deployment):
    base = runtime_resource(c)
    mask = ",".join(["spec.containerSpec.imageUri", *("spec.deploymentSpec." + key for key in deployment)])
    operation(api("PATCH", base + "?updateMask=" + mask,
                  {"spec": {"containerSpec": {"imageUri": image}, "deploymentSpec": deployment}}), "vertex")
    current = api("GET", base)
    expected = runtime_deployment({"spec": {"deploymentSpec": deployment}})
    if current["spec"]["containerSpec"]["imageUri"] != image or runtime_deployment(current) != expected:
        raise RuntimeError("Runtime image, environment or capacity settings do not match the release")


def temporal_config():
    value = terraform_outputs(TEMPORAL_TF, TEMPORAL_STATE)
    expected = "nori-poc-runtime" if ENVIRONMENT == "production" else "nori-sandbox-runtime"
    if value.get("runner_service_account") != expected + "@" + PROJECT + ".iam.gserviceaccount.com":
        raise RuntimeError("Worker state does not match the selected environment")
    return value


# The hosted Temporal Cloud namespace that production and sandbox workers use (their Terraform and runtime env agree).
TEMPORAL_ADDRESS = "quickstart-nori.qtjow.tmprl.cloud:7233"
TEMPORAL_NAMESPACE = "quickstart-nori.qtjow"


def temporal_env():
    env = os.environ.copy()
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
    # A developer's .env or shell points TEMPORAL_ADDRESS/NAMESPACE at a local stack; a release must never follow it.
    # Only the release-specific variables can override the hosted namespace.
    env["TEMPORAL_ADDRESS"] = os.environ.get("NORI_RELEASE_TEMPORAL_ADDRESS") or TEMPORAL_ADDRESS
    env["TEMPORAL_NAMESPACE"] = os.environ.get("NORI_RELEASE_TEMPORAL_NAMESPACE") or TEMPORAL_NAMESPACE
    for local_only in ("TEMPORAL_LOCAL", "TEMPORAL_TLS_DISABLE_HOST_VERIFICATION"):
        env.pop(local_only, None)
    return env


class VersionNotFound(RuntimeError):
    pass


def temporal_command(*args):
    # Authentication is passed through the child environment, never argv/logs.
    result = subprocess.run(["temporal", "--output=json", "--command-timeout=60s", *args], env=temporal_env(), text=True, capture_output=True, timeout=65)
    if result.returncode:
        # Confirmed CLI response for a nonexistent registered version. Other
        # failures (including auth and unavailable service) remain failures.
        if args[:3] == ("worker", "deployment", "describe-version") and result.stderr.strip().endswith(": Worker Deployment Version not found"):
            raise VersionNotFound("Worker deployment version is not registered")
        raise RuntimeError("Temporal " + " ".join(args[:3]) + " failed; exit " + str(result.returncode))
    return json.loads(result.stdout) if result.stdout.strip() else {}


def temporal(*args):
    return temporal_command("worker", "deployment", *args)


def version_info(build, missing_ok=False):
    try:
        result = temporal("describe-version", "--deployment-name=" + DEPLOYMENT, "--build-id=" + build)
    except VersionNotFound:
        if missing_ok:
            return None
        raise
    return result.get("workerDeploymentVersionInfo", result.get("worker_deployment_version_info", result))


def compute_config(info):
    return (info or {}).get("computeConfig", (info or {}).get("compute_config"))


def compatible_builds(t):
    services = production_workers(t)
    builds = set(services)
    deployment = temporal("describe", "--name=" + DEPLOYMENT)
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
    running = temporal_command("workflow", "list", '--query=ExecutionStatus = "Running" AND TaskQueue = "' + TASK_QUEUE + '"')
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
        if versioning.get("versioningOverride") or version.get("deploymentName") != DEPLOYMENT or version.get("buildId") not in builds:
            raise RuntimeError("Open workflow uses an incompatible or unknown worker build: " + execution["workflowId"])


def temporal_zero(t):
    failures = []
    for pool in t["worker_pools"].values():
        try:
            url = "https://run.googleapis.com/v2/" + pool["resource"]
            current = api("GET", url)
            if not current.get("reconciling") and current.get("scaling", {}).get("manualInstanceCount", 0) == 0 and current.get("observedGeneration") == current.get("generation") and current.get("terminalCondition", {}).get("state") == "CONDITION_SUCCEEDED":
                continue
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


def production_workers(t):
    services = t.get("production_worker_services", {})
    if not services or not t.get("worker_wake_queue"):
        raise RuntimeError("No " + ENVIRONMENT + " request-worker release is configured; candidate builds and retained pools cannot open admission")
    return services


def worker_queues(t):
    names = set(t.get("worker_wake_queues", {}).values())
    names.update(t[key] for key in ("worker_wake_queue", "candidate_event_queue") if t.get(key))
    return sorted(names)


def queue_state(names, paused):
    failures = []
    wanted = "PAUSED" if paused else "RUNNING"
    for name in names:
        try:
            url = "https://cloudtasks.googleapis.com/v2/" + name
            if api("GET", url).get("state") == wanted:
                continue
            api("POST", url + (":pause" if paused else ":resume"), {})
            if api("GET", url).get("state") != wanted:
                raise RuntimeError("Wake queue did not reach " + wanted + ": " + name)
        except Exception as error:
            failures.append(str(error))
    if failures:
        raise RuntimeError("; ".join(failures))


def wait_worker_requests(t):
    # Pausing a native queue does not interrupt its dispatched HTTP requests.
    # The worker responds only after SDK drain. A failed/ambiguous delivery
    # needs native stopped/response log inspection, not an assumed VM stop.
    end = time.monotonic() + 780
    while True:
        active = []
        ambiguous = []
        for name in worker_queues(t):
            token = ""
            while True:
                query = {"pageSize": 1000}
                if token:
                    query["pageToken"] = token
                page = api("GET", "https://cloudtasks.googleapis.com/v2/" + name + "/tasks?" + urlencode(query))
                for task in page.get("tasks", []):
                    attempt = task.get("lastAttempt", {})
                    if not attempt.get("dispatchTime"):
                        continue
                    if not attempt.get("responseTime"):
                        active.append(task["name"])
                    elif attempt.get("responseStatus", {}).get("code", 0):
                        ambiguous.append(task["name"])
                token = page.get("nextPageToken")
                if not token:
                    break
        if ambiguous:
            raise RuntimeError("Worker delivery failed; inspect native stopped/response logs before stopping the computer: " + ", ".join(ambiguous))
        if not active:
            return
        if time.monotonic() >= end:
            raise RuntimeError("Worker requests have not drained; computer retained for cleanup: " + ", ".join(active))
        time.sleep(3)


def drain_workers(t):
    failures = []
    try:
        queue_state(worker_queues(t), True)
    except Exception as error:
        failures.append(str(error))
    # Remove the native scaler configuration first; a zero-count PATCH alone
    # races WCI. Pinned workflows and their retained pools are not deleted.
    for build in t["worker_pools"]:
        try:
            if not compute_config(version_info(build, missing_ok=True)):
                continue
            temporal("update-version-compute-config", "--deployment-name=" + DEPLOYMENT, "--build-id=" + build, "--remove")
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
    if not failures:
        try:
            wait_worker_requests(t)
        except Exception as error:
            failures.append(str(error))
    if failures:
        raise RuntimeError("Worker pause incomplete: " + "; ".join(failures))


def verify_worker_compatibility(t):
    compatible_builds(t)
    for build in t["worker_pools"]:
        if compute_config(version_info(build, missing_ok=True)):
            raise RuntimeError("Retained pool scaler is still enabled; pause before request-worker rollout: " + build)
    temporal_zero(t)


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


def runtime_resource(c):
    return f"https://{REGION}-aiplatform.googleapis.com/v1beta1/" + c["runtime_name"]


def web_resource():
    return f"https://run.googleapis.com/v2/projects/{PROJECT}/locations/{REGION}/services/{WEB}"


def metadata(changes):
    current = api("GET", vm_url())["metadata"]
    values = {entry["key"]: entry["value"] for entry in current.get("items", [])}
    values.update(changes)
    operation(api("POST", vm_url() + "/setMetadata", {"fingerprint": current["fingerprint"], "items": [{"key": k, "value": v} for k, v in values.items()]}))


def firestore(c, path):
    return f"https://firestore.googleapis.com/v1/projects/{PROJECT}/databases/{quote(c['firestore_database'], safe='')}/documents/{path}"


def control(c, paused):
    # Omitted expires_at in the update mask removes the obsolete admission field.
    return api("PATCH", firestore(c, "system/control") + "?currentDocument.exists=true&updateMask.fieldPaths=paused&updateMask.fieldPaths=expires_at", {"fields": {"paused": {"booleanValue": paused}}})


def execution_open(c, workers):
    control_doc = api("GET", firestore(c, "system/control"))
    if control_doc.get("fields", {}).get("paused", {}).get("booleanValue") is not False:
        return False
    if not all(
        api("GET", "https://cloudtasks.googleapis.com/v2/" + name).get("state") == "RUNNING"
        for name in (c["conversation_queue"], workers["worker_wake_queue"])
    ):
        return False
    vm = api("GET", vm_url())
    return any(item["key"] == "computer-paused" and item["value"] == "false" for item in vm["metadata"].get("items", []))


def pause(c, worker_config=None):
    failures = []

    def attempt(action):
        try:
            return action()
        except Exception as error:
            failures.append(str(error))

    # Fence delivery before paused handlers preserve successor wakes. Queue
    # creation remains available while paused; only dispatch is disabled.
    attempt(lambda: queue_state([c["conversation_queue"]], True))
    workers = worker_config if worker_config is not None else attempt(temporal_config)
    if workers is not None:
        attempt(lambda: queue_state(worker_queues(workers), True))
    attempt(lambda: control(c, True))
    if workers is not None:
        attempt(lambda: drain_workers(workers))
    attempt(lambda: metadata({"computer-paused": "true"}))
    if not failures:
        attempt(stop_vm)
    if failures:
        raise RuntimeError("Pause incomplete; computer stop withheld when request drain is uncertain: " + "; ".join(failures))
    print("Paused: admission closed, conversation and worker queues paused, native scalers removed, retained pools at zero, computer stopped; waiting tasks preserved.")
    return workers


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
    workers = temporal_config()
    # Fence delivery before changing admission; accepted pending wakes remain
    # durable until routing is verified and the production queue is resumed.
    queue_state([c["conversation_queue"], *worker_queues(workers)], True)
    control(c, True)
    try:
        verify_worker_compatibility(workers)
        metadata({"computer-paused": "false"})
        control(c, False)
        api("POST", c["runtime_url"].rstrip("/") + "/api/resume", {})
        queue_state([workers["worker_wake_queue"]], False)
        queue_state([c["conversation_queue"]], False)
    except Exception:
        pause(c, workers)
        raise
    print("Unpaused: tasks may run; the computer starts only when a tool needs it.")


def publish_computer(c, binary):
    binary = Path(binary).resolve()
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    object_name = "releases/computer-agent-" + digest
    gcloud("storage", "cp", str(binary), f"gs://{c['artifact_bucket']}/{object_name}")
    # Use Terraform's startup source to avoid drift on the next plan.
    metadata({"computer-release-object": c["artifact_bucket"] + "/" + object_name, "computer-release-sha256": digest,
              "startup-script": (ROOT / "computer-startup.sh").read_text()})
    return digest


def deploy_runtime(c, workers, image):
    required = ("conversation_queue", "conversation_wake_service_account", "outer_model")
    if any(not c.get(key) for key in required):
        raise RuntimeError("Apply the conversation queue, IAM and secret-reference infrastructure before deploying the runtime")
    current = api("GET", runtime_resource(c))
    spec = current.get("spec", {}).get("deploymentSpec", {})
    env = {entry["name"]: entry for entry in spec.get("env", [])}
    env.update({key: {"name": key, "value": value} for key, value in {
        "NORI_WORKER_SERVICES": json.dumps(production_workers(workers)),
        "NORI_WORKER_WAKE_QUEUE": workers["worker_wake_queue"],
        "NORI_AGENT_MODEL": c["outer_model"],
        "NORI_WAKE_QUEUE": c["conversation_queue"],
        "NORI_WAKE_SERVICE_ACCOUNT": c["conversation_wake_service_account"],
        "NORI_RUNTIME_URL": c["runtime_url"],
        "TEMPORAL_ADDRESS": TEMPORAL_ADDRESS,
        "TEMPORAL_NAMESPACE": TEMPORAL_NAMESPACE,
        "TEMPORAL_TASK_QUEUE": TASK_QUEUE,
        "TEMPORAL_DEPLOYMENT_NAME": DEPLOYMENT,
        "TEMPORAL_API_KEY_SECRET_VERSION": f"projects/{PROJECT}/secrets/nori-temporal-api-key/versions/latest",
    }.items()})
    env.pop("NORI_EXECUTION_MODE", None)
    env.pop("OPENAI_API_KEY", None)
    # Connected accounts (credential store, OAuth callback, send defaults, push) come from the application state's
    # outputs; a state applied before they existed leaves the runtime as it is.
    env.update({key: {"name": key, "value": value} for key, value in c.get("connected_accounts_env", {}).items()})
    secret_env = {entry["name"]: entry for entry in spec.get("secretEnv", [])}
    secret_env.update({key: {"name": key, "secretRef": {"secret": secret, "version": "latest"}}
                       for key, secret in c.get("connected_accounts_secret_env", {}).items()})
    secret_env["OPENAI_API_KEY"] = {"name": "OPENAI_API_KEY", "secretRef": {
        "secret": "nori-sandbox-openai-api-key" if ENVIRONMENT == "sandbox" else "nori-openai-api-key", "version": "latest"}}
    if ENVIRONMENT == "sandbox":
        env["NORI_ENVIRONMENT"] = {"name": "NORI_ENVIRONMENT", "value": "sandbox"}
        env.pop("OPENROUTER_API_KEY", None)
        env.pop("OPENROUTER_MANAGEMENT_KEY", None)
        secret_env.pop("OPENROUTER_MANAGEMENT_KEY", None)
        secret_env["OPENROUTER_API_KEY"] = {"name": "OPENROUTER_API_KEY", "secretRef": {"secret": "nori-sandbox-openrouter-api-key", "version": "latest"}}
    else:
        env["MODEL_NAME"] = {"name": "MODEL_NAME", "value": "gpt-6.1-terra"}
        env["NORI_JEV_ENABLED"] = {"name": "NORI_JEV_ENABLED", "value": "false"}
        for name in ("OPENROUTER_API_KEY", "OPENROUTER_MANAGEMENT_KEY", "JEV_API_KEY"):
            env.pop(name, None)
            secret_env.pop(name, None)
    patch_runtime(c, image, {**runtime_settings(), "env": list(env.values()), "secretEnv": list(secret_env.values())})


def deploy_web(image):
    return gcloud("run", "services", "update", WEB, "--region=" + REGION, "--image=" + image)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["bootstrap-identities", "publish-computer", "deploy-runtime", "deploy-web", "unpause", "pause", "clear-deadline", "status", "spend-caps"])
    parser.add_argument("value", nargs="?")
    args = parser.parse_args()
    if args.action in {"pause", "unpause"} and ENVIRONMENT == "production":
        raise RuntimeError(f"Run make {args.action} from the repo root to take the production operation lock.")
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
        if not args.value:
            parser.error("publish-computer needs a built binary path")
        digest = publish_computer(c, args.value)
        print("Computer release " + digest + "; restart the stopped host on next use to install.")
    elif args.action == "deploy-runtime":
        if not args.value:
            parser.error("deploy-runtime needs an immutable image URI")
        deploy_runtime(c, temporal_config(), args.value)
        print("Runtime image, environment and capacity settings updated and verified.")
    elif args.action == "deploy-web":
        if not args.value:
            parser.error("deploy-web needs an immutable image URI")
        print(deploy_web(args.value))
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
    except (RuntimeError, TimeoutError, subprocess.SubprocessError) as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
