#!/usr/bin/env python3
"""Deploy GitHub release artifacts; GitHub owns ordering, credentials and concurrency."""
import argparse
import base64
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time
from urllib.error import HTTPError
from urllib.parse import quote
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parent
PROJECT = "bot-111549"
REGISTRY = "us-central1-docker.pkg.dev/bot-111549/nori-poc"
PIPELINE_SECONDS = 1800
SMOKE_SECONDS = 600
RECOVERY_SECONDS = 300
SHA = re.compile(r"[a-f0-9]{40}")
BUILD = re.compile(r"[1-9][0-9]*-[1-9][0-9]*")


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def safe_error(error):
    return str(error) if isinstance(error, RuntimeError) else type(error).__name__


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


poc = load_module("nori_poc", ROOT.parent / "poc/poc.py")


def run(args, **kwargs):
    # JSON/state and credential stdout stay private. Surface only diagnostic
    # titles, with known credentials and token-shaped strings redacted.
    process = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, **kwargs)
    try:
        output, stderr = process.communicate(timeout=poc.remaining(900))
    except BaseException:
        process.send_signal(signal.SIGINT)
        try:
            process.communicate(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate()
        raise
    if process.returncode:
        clean = re.sub(r"\x1b\[[0-9;]*m", "", stderr)
        titles = [line.strip(" │") for line in clean.splitlines()
                  if line.strip(" │").startswith(("Error:", "ERROR:", "Release failed:"))]
        detail = "; ".join(titles[:3])[:1500]
        for key, value in os.environ.items():
            if len(value) >= 8 and re.search(r"TOKEN|PASSWORD|SECRET|API_KEY|CREDENTIAL", key, re.I):
                detail = detail.replace(value, "[redacted]")
        detail = re.sub(r"(?:ya29\.[A-Za-z0-9._-]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)", "[redacted]", detail)
        raise RuntimeError(f"{Path(args[0]).name} {args[1] if len(args) > 1 else ''} failed (exit {process.returncode})" +
                           (": " + detail if detail else "; inspect this step's configuration and cloud operation status"))
    return output.strip()


def write_json(path, value):
    # Recovery must see either the complete old report or the complete new one.
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, prefix="." + path.name + ".", delete=False) as stream:
            temporary = Path(stream.name)
            json.dump(value, stream, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        temporary.replace(path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def image(value, component):
    require(isinstance(value, str) and re.fullmatch(re.escape(REGISTRY + "/" + component) + r"@sha256:[a-f0-9]{64}", value),
            f"{component} must be an immutable digest in the Nori registry")
    return value


def validate_manifest(value):
    require(value.get("schema") == 2 and SHA.fullmatch(value.get("commit", "")), "Invalid release source commit")
    require(BUILD.fullmatch(value.get("build_id", "")), "Invalid GitHub run identity")
    for component in ("web", "runtime", "worker"):
        image(value.get(component), component)
    digest = value.get("computer_sha256", "")
    require(re.fullmatch(r"[a-f0-9]{64}", digest), "Invalid computer binary SHA256")
    require(value.get("computer_object") == "computer/" + digest, "Unexpected computer object")
    return value


def manifest(args):
    require(SHA.fullmatch(args.commit or ""), "Use the full main commit SHA")
    require(BUILD.fullmatch(args.build_id or ""), "Use GitHub RUN_ID-RUN_ATTEMPT")
    digest = hashlib.sha256(args.computer.read_bytes()).hexdigest()
    return validate_manifest({"schema": 2, "commit": args.commit, "build_id": args.build_id,
                              "web": args.web, "runtime": args.runtime, "worker": args.worker,
                              "computer_sha256": digest, "computer_object": "computer/" + digest})


class CloudError(RuntimeError):
    def __init__(self, message, status):
        super().__init__(message)
        self.status = status


class Cloud:
    def __init__(self):
        self.bucket = os.environ["NORI_RELEASE_BUCKET"]
        self.region = os.environ.get("NORI_BUILD_REGION", "us-central1")
        self.token = None
        self.token_time = 0

    def request(self, method, url, body=None, content_type="application/json", missing=False):
        if not self.token or time.monotonic() - self.token_time > 1200:
            self.token = run(["gcloud", "--quiet", "--project=" + PROJECT, "auth", "print-access-token"])
            self.token_time = time.monotonic()
        data = body if isinstance(body, bytes) else None if body is None else json.dumps(body).encode()
        req = Request(url, data=data, method=method, headers={"Authorization": "Bearer " + self.token,
                      "Content-Type": content_type})
        try:
            with urlopen(req, timeout=poc.remaining(60)) as response:
                return response.read()
        except HTTPError as error:
            if missing and error.code == 404:
                return None
            raise CloudError(f"Cloud {method} failed: HTTP {error.code}", error.code) from None

    def api(self, method, url, body=None):
        return json.loads(self.request(method, url, body) or b"{}")

    def object_url(self, name):
        return f"https://storage.googleapis.com/storage/v1/b/{quote(self.bucket, safe='')}/o/{quote(name, safe='')}"

    def upload_binary(self, path, name):
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        existing = self.request("GET", self.object_url(name) + "?alt=media", missing=True)
        if existing is None:
            try:
                run(["gcloud", "--quiet", "--project=" + PROJECT, "storage", "cp", str(path),
                     "gs://" + self.bucket + "/" + name, "--if-generation-match=0"])
            except RuntimeError:
                existing = self.request("GET", self.object_url(name) + "?alt=media")
        if existing is not None:
            require(hashlib.sha256(existing).hexdigest() == digest, "Existing immutable computer artifact differs")


def init_backends():
    require(os.environ.get("NORI_TF_BACKEND") == "gcs", "Hosted release requires reviewed GCS state migration")
    bucket = os.environ["NORI_STATE_BUCKET"]
    require(re.fullmatch(r"[a-z0-9][a-z0-9.-]+", bucket), "Invalid state bucket")
    # Overrides exist only in the isolated build checkout. No migration is performed.
    for name in ("poc", "temporal"):
        directory = ROOT.parent / name / "terraform"
        (directory / "release_backend_override.tf").write_text('terraform {\n  backend "gcs" {}\n}\n')
        run(["terraform", "-chdir=" + str(directory), "init", "-input=false", "-reconfigure",
             "-backend-config=bucket=" + bucket, "-backend-config=prefix=nori/" + ("sandbox/" if poc.ENVIRONMENT == "sandbox" else "") + name])
        outputs = json.loads(run(["terraform", "-chdir=" + str(directory), "output", "-json"]))
        require(outputs, f"Migrated {name} state is missing; refusing an empty backend")


def temporal_key():
    # Existing Secret Manager credential, never persisted or logged.
    os.environ["TEMPORAL_API_KEY"] = run(["gcloud", "--quiet", "--project=" + PROJECT, "secrets", "versions", "access", "latest", "--secret=nori-temporal-api-key"])
    require(os.environ["TEMPORAL_API_KEY"], "Existing Temporal credential is empty")
    os.environ["TEMPORAL_ADDRESS"] = "quickstart-nori.qtjow.tmprl.cloud:7233"
    os.environ["TEMPORAL_NAMESPACE"] = "quickstart-nori.qtjow"


def current_worker():
    routing = poc.temporal("describe", "--name=" + poc.DEPLOYMENT).get("routingConfig", {})
    require(not routing.get("rampingVersionPercentage", 0), "A Temporal ramp is active; finish it before release")
    build = routing.get("currentVersionBuildID") or routing.get("currentDeploymentVersion", {}).get("buildId")
    require(build and re.fullmatch(r"[a-z0-9][a-z0-9-]{0,39}", build), "Cannot identify Current Temporal build")
    return build


def snapshot(config):
    control = poc.api("GET", poc.firestore(config, "system/control"))
    vm = poc.api("GET", poc.vm_url())
    require(control.get("fields", {}).get("paused", {}).get("booleanValue") is True and vm["status"] == "TERMINATED",
            "Release requires Nori already paused and its computer stopped")
    web = poc.api("GET", f"https://run.googleapis.com/v2/projects/{PROJECT}/locations/us-central1/services/{poc.WEB}")
    runtime = poc.api("GET", "https://us-central1-aiplatform.googleapis.com/v1beta1/" + config["runtime_name"])
    runtime_env = {entry["name"]: entry.get("value") for entry in runtime["spec"]["deploymentSpec"].get("env", [])}
    require(runtime_env.get("FIRESTORE_DATABASE_ID") == config["firestore_database"] and
            runtime_env.get("COMPUTER_INSTANCE") == poc.VM and runtime_env.get("TEMPORAL_TASK_QUEUE") == poc.TASK_QUEUE,
            "Runtime bindings do not match the selected environment")
    web_env = {entry["name"]: entry.get("value") for entry in web["template"]["containers"][0].get("env", [])}
    require(web_env.get("NORI_RUNTIME_URL") == config["runtime_url"], "Web points at a different environment runtime")
    scaling = runtime["spec"]["deploymentSpec"]
    require(scaling.get("minInstances", 0) == 0 and scaling.get("maxInstances") == 1, "Runtime must remain min 0 / max 1")
    for scaling in (web.get("scaling", {}), web["template"].get("scaling", {})):
        require(scaling.get("minInstanceCount", 0) == 0 and scaling.get("maxInstanceCount") == 1, "Web must remain min 0 / max 1")
    metadata = {v["key"]: v["value"] for v in vm["metadata"].get("items", [])}
    return {"web": image(web["template"]["containers"][0]["image"], "web"),
            "runtime": image(runtime["spec"]["containerSpec"]["imageUri"], "runtime"), "worker": current_worker(),
            "computer": {key: metadata[key] for key in ("computer-release-object", "computer-release-sha256")}}


def deploy_image(component, value):
    run([sys.executable, str(poc.ROOT / "poc.py"), "deploy-" + component, value])


def warm_worker(build_id, workers, config, request_id, record):
    """One tracked request registers the SDK and returns only after its drain."""
    def registered(info):
        types = {str(q.get("type", "")).lower().removeprefix("task_queue_type_")
                 for q in info.get("taskQueuesInfos", []) if q.get("name") == poc.TASK_QUEUE}
        return {"workflow", "activity"}.issubset(types)
    info = poc.version_info(build_id, missing_ok=True) or {}
    if registered(info):
        return
    queue = workers["worker_wake_queue"]
    root = poc.production_workers(workers)[build_id]
    name = queue + "/tasks/nori-release-" + request_id
    record(registration_task=name)
    # The queue stays fenced; run dispatches only this content-free wake. Closing
    # admission on any failure lets the normal pause path verify request drain.
    require(poc.api("GET", "https://cloudtasks.googleapis.com/v2/" + queue).get("state") == "PAUSED", "Worker registration requires a paused queue")
    poc.api("POST", "https://cloudtasks.googleapis.com/v2/" + queue + "/tasks", {"task": {
        "name": name, "dispatchDeadline": "720s", "httpRequest": {"url": root + "/run", "httpMethod": "POST",
        "headers": {"Content-Type": "application/json"},
        "body": base64.b64encode(b'{"registration_only":true}').decode(),
        "oidcToken": {"serviceAccountEmail": config["conversation_wake_service_account"], "audience": root}}}})
    poc.api("POST", "https://cloudtasks.googleapis.com/v2/" + name + ":run", {})
    end = time.monotonic() + poc.remaining(300)
    retry_after, retry_delay, attempts = time.monotonic() + 5, 5, 1
    finished = False
    while time.monotonic() < end:
        if not finished:
            task = poc.api("GET", "https://cloudtasks.googleapis.com/v2/" + name, allow404=True)
            finished = task is None
            attempt = (task or {}).get("lastAttempt", {})
            code = attempt.get("responseStatus", {}).get("code", 0)
            if attempt.get("responseTime") and code not in (0, 7):
                # This is our registration-only task, which cannot execute user
                # work. Remove its completed failed delivery so pause is usable.
                poc.api("DELETE", "https://cloudtasks.googleapis.com/v2/" + name)
                raise RuntimeError(f"Worker registration failed (delivery code {code}); its completed request was removed")
            # A new invoker binding can lag Terraform completion. Only retry a
            # completed permission denial; never overlap an admitted request.
            if (attempt.get("responseTime") and attempt.get("responseStatus", {}).get("code") == 7 and
                    time.monotonic() >= retry_after):
                poc.api("POST", "https://cloudtasks.googleapis.com/v2/" + name + ":run", {})
                attempts += 1
                record(registration_attempts=attempts)
                retry_delay = min(30, retry_delay * 2)
                retry_after = time.monotonic() + retry_delay
        if finished:
            info = poc.version_info(build_id, missing_ok=True) or {}
            if registered(info):
                return
        time.sleep(2)
    task = poc.api("GET", "https://cloudtasks.googleapis.com/v2/" + name, allow404=True)
    if task and task.get("lastAttempt", {}).get("responseTime"):
        poc.api("DELETE", "https://cloudtasks.googleapis.com/v2/" + name)
    raise RuntimeError("Worker registration did not finish; completed attempts removed, active requests must drain")


def cleanup_registration(report, workers):
    """Remove only this release's settled request; never discard an active one."""
    name = report.get("registration_task")
    if not name:
        return
    expected = workers["worker_wake_queue"] + "/tasks/nori-release-" + report["build_id"]
    require(name == expected, "Registration cleanup task does not belong to this release and environment")
    url = "https://cloudtasks.googleapis.com/v2/" + name
    task = poc.api("GET", url, allow404=True)
    if task and task.get("lastAttempt", {}).get("responseTime"):
        poc.api("DELETE", url, allow404=True)


def worker_url(build_id):
    prefix = "nori-task" if poc.ENVIRONMENT == "production" else "nori-sb"
    return f"https://{prefix}-{build_id}-8273801370.us-west1.run.app"


def route_only_update(change, build_id):
    # Google provider 8.8.1 computed status fields are not configuration. Retain
    # every configurable field, including image, identity, limits and other env.
    computed = {"conditions", "create_time", "creator", "delete_time", "effective_annotations", "effective_labels", "etag",
                "expire_time", "generation", "last_modifier", "latest_created_revision", "latest_ready_revision", "observed_generation",
                "reconciling", "terminal_condition", "terraform_labels", "traffic_statuses", "uid", "update_time", "uri", "urls"}
    before, after = copy.deepcopy(change["before"]), copy.deepcopy(change["after"])
    routes, scopes = [], []
    for value in (before, after):
        for key in computed:
            value.pop(key, None)
        template = value["template"][0]
        template.pop("revision", None)  # Provider-generated revision identifier.
        for container in template["containers"]:
            container.pop("build_info", None)
            env = {item["name"]: item for item in container["env"]}
            routes.append(json.loads(env.pop("NORI_WORKER_SERVICES")["value"]))
            scope = env.get("NORI_SPEND_SCOPE", {}).get("value")
            if (poc.ENVIRONMENT == "sandbox" and env.get("NORI_SPEND_PROFILE", {}).get("value") == "ci"
                    and scope and re.fullmatch(r"[a-f0-9]{64}", scope)):
                scopes.append(env.pop("NORI_SPEND_SCOPE")["value"])
            container["env"] = sorted(env.values(), key=lambda item: item["name"])
    valid_scope = not scopes or (len(scopes) == 2 and scopes[1] == os.environ.get("NORI_SPEND_SCOPE"))
    return (valid_scope and len(routes) == 2 and routes[1] == {**routes[0], build_id: worker_url(build_id)} and before == after)


def validate_worker_plan(plan, build_id):
    allowed = {f'google_cloud_run_v2_service.worker["{build_id}"]',
               f'google_cloud_run_v2_service_iam_member.worker_invoke["{build_id}"]'}
    for resource in plan.get("resource_changes", []):
        require(not resource.get("previous_address"), "Review and apply Terraform address moves separately before deployment")
        change = resource["change"]
        actions = change["actions"]
        if actions in (["no-op"], ["read"]):
            continue
        if resource.get("type") == "google_cloud_run_v2_service" and resource.get("name") == "worker" and actions == ["update"]:
            require(route_only_update(change, build_id), "Retained worker update changes more than the added route")
            continue
        require(resource["address"] in allowed and actions == ["create"],
                "Worker plan changes an existing or unrelated resource: " + resource["address"])


def worker_plan(candidate, directory):
    state = json.loads(run(["terraform", "-chdir=" + str(poc.TEMPORAL_TF), "show", "-json"]))
    resources = state.get("values", {}).get("root_module", {}).get("resources", [])
    pools = {resource["index"]: resource["values"]["template"][0]["containers"][0]["image"]
             for resource in resources if resource.get("type") == "google_cloud_run_v2_worker_pool"}
    services = {resource["index"]: resource["values"]["template"][0]["containers"][0]["image"]
                for resource in resources if resource.get("type") == "google_cloud_run_v2_service" and resource.get("name") == "worker"}
    outputs = {key: value["value"] for key, value in state.get("values", {}).get("outputs", {}).items()}
    targets = outputs.get("request_worker_targets", {})
    require(set(targets) == set(services), "Worker routing outputs are incomplete; refresh the retained Terraform state")
    require("scalable_build_ids" in outputs, "Retained worker scaling configuration is missing")
    # Terraform is the sole worker inventory. The checked-in billing config is
    # used only while upgrading older state that predates its output; the saved
    # plan guard still refuses any change to billing or retained infrastructure.
    baseline = json.loads((ROOT.parent / "temporal/terraform/worker-releases.json").read_text())
    retained = {"worker_images": pools, "request_worker_images": services,
                "request_worker_targets": targets, "scalable_build_ids": outputs["scalable_build_ids"],
                "spend_alert": outputs.get("spend_alert_config", baseline.get("spend_alert") if poc.ENVIRONMENT == "production" else None)}
    build_id = "gh-" + candidate["commit"][:10] + "-" + hashlib.sha256(candidate["build_id"].encode()).hexdigest()[:12]
    target = {"deployment_name": poc.DEPLOYMENT, "task_queue": poc.TASK_QUEUE,
              "wake_queue_name": "nori-worker" if poc.ENVIRONMENT == "production" else "nori-sandbox-worker",
              "poll_window_seconds": 240, "firestore_database": "nori-poc" if poc.ENVIRONMENT == "production" else "nori-sandbox"}
    if poc.ENVIRONMENT == "sandbox":
        target.update(spend_scope=os.environ["NORI_SPEND_SCOPE"])
        # Retained CI workers share this run's capped lease during paused updates.
        # Old images validate on startup and cannot restart with a closed lease.
        for retained_target in targets.values():
            if retained_target.get("spend_scope"):
                retained_target["spend_scope"] = os.environ["NORI_SPEND_SCOPE"]
    existing = retained.get("request_worker_targets", {}).get(build_id, target)
    existing = {key: value for key, value in existing.items() if value is not None}
    require(build_id not in pools and retained.get("request_worker_images", {}).get(build_id, candidate["worker"]) == candidate["worker"] and
            existing == target, "A retained worker build cannot change image or routing target")
    retained.setdefault("request_worker_images", {})[build_id] = candidate["worker"]
    retained.setdefault("request_worker_targets", {})[build_id] = target
    manifest_file = directory / "worker-releases.json"
    write_json(manifest_file, retained)
    plan_file = directory / "worker.plan"
    run(["terraform", "-chdir=" + str(poc.TEMPORAL_TF), "plan", "-input=false", "-lock-timeout=30s",
         "-var-file=" + str(manifest_file), "-var=environment=" + poc.ENVIRONMENT,
         "-var=runtime_invoke_root=" + os.environ["TF_VAR_runtime_invoke_root"], "-out=" + str(plan_file)])
    plan_file.chmod(0o600)
    plan = json.loads(run(["terraform", "-chdir=" + str(poc.TEMPORAL_TF), "show", "-json", str(plan_file)]))
    validate_worker_plan(plan, build_id)
    return build_id, retained, plan_file


def spend_keys():
    keys = load_module("nori_spend_keys", ROOT.parent / "spend/keys.py")
    keys.cloud_api = Cloud().api
    return keys


def managed_ci_call(method, value):
    """Only CI reads the management credential, entirely in process memory."""
    previous = os.environ.get("OPENROUTER_MANAGEMENT_KEY")
    if not previous:
        os.environ["OPENROUTER_MANAGEMENT_KEY"] = run([
            "gcloud", "--quiet", "--project=" + PROJECT, "secrets", "versions", "access", "latest",
            "--secret=nori-ci-spend-management-key"])
    try:
        return getattr(spend_keys(), method)(value)
    finally:
        if previous is None:
            os.environ.pop("OPENROUTER_MANAGEMENT_KEY", None)
        else:
            os.environ["OPENROUTER_MANAGEMENT_KEY"] = previous


def ci_lease(candidate):
    value = managed_ci_call("ensure_ci", candidate["commit"])
    require(value.get("profile") == "ci" and value.get("limit_usd") == 0.1 and value.get("ready") is True and
            re.fullmatch(r"[a-f0-9]{64}", value.get("scope", "")), "CI spend scope is not ready with its $0.10 allowance")
    version = str(value.get("secret_version", ""))
    require(version.isdigit() and int(version) > 0, "CI native-key secret version is invalid")
    os.environ["NORI_SPEND_SCOPE"] = value["scope"]
    os.environ["NORI_SPEND_KEY_SECRET_VERSION"] = version
    os.environ["NORI_SPEND_PROFILE"] = "ci"


def close_ci_lease(report):
    if poc.ENVIRONMENT == "sandbox" and report.get("spend_lease_requested") is True:
        scope = report.get("spend_scope") or spend_keys().ci_scope(report["commit"])
        managed_ci_call("close_ci", scope)
        report["spend_closed"] = True


def cancelled(_signum, _frame):
    raise RuntimeError("Release cancelled")


def github_identity(candidate):
    """Fail before cloud access; IAM independently enforces this workflow identity."""
    expected = {"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "a-barwick/nori",
                "GITHUB_REF": "refs/heads/main", "GITHUB_SHA": candidate["commit"],
                "GITHUB_WORKFLOW_REF": "a-barwick/nori/.github/workflows/release.yml@refs/heads/main"}
    for key, value in expected.items():
        require(os.environ.get(key) == value, "Release identity mismatch: " + key)
    require(os.environ.get("GITHUB_EVENT_NAME") in {"push", "workflow_dispatch"}, "Release requires a trusted main event")
    require(candidate["build_id"] == os.environ.get("GITHUB_RUN_ID", "") + "-" + os.environ.get("GITHUB_RUN_ATTEMPT", ""),
            "Release artifact belongs to a different GitHub run or attempt; use Re-run all jobs")


def idle_sandbox(smoke, config, record):
    """Inspect outstanding work without admitting old, potentially unguarded code."""
    try:
        smoke.preflight(config, time.monotonic() + poc.remaining(120))
        return
    except smoke.SmokeError as error:
        if poc.ENVIRONMENT != "sandbox" or not str(error).startswith("Preflight refused:"):
            raise
    raise RuntimeError("Sandbox contains pending work; keep admission closed and use targeted Stop or a reviewed guarded-image migration before retrying")


def release(candidate, binary, smoke_mode, report_path):
    end = time.time() + PIPELINE_SECONDS
    os.environ["NORI_OPERATION_DEADLINE"] = str(end - RECOVERY_SECONDS)
    report = {"commit": candidate["commit"], "candidate": candidate, "environment": poc.ENVIRONMENT,
              "build_id": candidate["build_id"], "phase": "preflight", "result": "pending"}
    config, before, changed, admitted, error = None, None, False, False, None

    def record(**values):
        write_json(report_path, {**report, **values})
        report.update(values)
        print(json.dumps({"phase": report["phase"], "result": report["result"], "report": str(report_path)}), flush=True)

    def evidence(event):
        record(smoke=[*report.get("smoke", []), event])

    try:
        record()
        init_backends()
        temporal_key()
        config = poc.config()
        workers = poc.temporal_config()
        if poc.ENVIRONMENT == "sandbox":
            record(spend_lease_requested=True)
            ci_lease(candidate)
            record(spend_scope=os.environ["NORI_SPEND_SCOPE"])
        poc.production_workers(workers)
        os.environ["TF_VAR_observability"] = json.dumps(workers.get("observability_config", {}))
        os.environ["TF_VAR_environment"] = poc.ENVIRONMENT
        os.environ["TF_VAR_runtime_invoke_root"] = config["runtime_url"]
        smoke = load_module("nori_smoke", ROOT / "smoke.py")
        account = "nori-ci-sandbox" if poc.ENVIRONMENT == "sandbox" else "nori-ci-deploy"
        smoke_config = {**config, "project": PROJECT, "temporal_deployment": poc.DEPLOYMENT,
                        "iap_service_account": account + "@bot-111549.iam.gserviceaccount.com"}
        if poc.ENVIRONMENT == "sandbox":
            smoke_config["spend_check"] = poc.spend_status
        # Never interrupt or replay user work to make a release pass. An idle
        # session can be paused automatically; a busy session needs a later run.
        idle_sandbox(smoke, smoke_config, record)
        record(mutating=True)
        poc.pause(config)
        before = snapshot(config)
        smoke.preflight(smoke_config, time.monotonic() + poc.remaining(120))
        record(previous=before)
        with tempfile.TemporaryDirectory(prefix="nori-release-") as tmp:
            directory = Path(tmp)
            worker, retained, plan = worker_plan(candidate, directory)
            require(snapshot(config) == before, "Deployment changed during preparation")
            record(phase="deploy", worker_build=worker)
            run(["terraform", "-chdir=" + str(poc.TEMPORAL_TF), "apply", "-input=false", str(plan)])
            workers = poc.temporal_config()
            publisher = Cloud()
            publisher.bucket = config["artifact_bucket"]
            object_name = "releases/computer-agent-" + candidate["computer_sha256"]
            publisher.upload_binary(binary, object_name)
            changed = True
            poc.metadata({"computer-release-object": publisher.bucket + "/" + object_name,
                          "computer-release-sha256": candidate["computer_sha256"]})
            deploy_image("runtime", candidate["runtime"])
            deploy_image("web", candidate["web"])
            if poc.ENVIRONMENT == "sandbox":
                poc.verify_spend_binding({**config, "expected_worker_build": worker})
            smoke_config["baseline_ids"] = smoke.preflight(smoke_config, time.monotonic() + poc.remaining(120))
            record(phase="verify")
            available = poc.remaining(SMOKE_SECONDS)
            require(available > 30, "Insufficient time remains for verification and shutdown")
            deadline = time.monotonic() + available
            os.environ["NORI_OPERATION_DEADLINE"] = str(time.time() + available)
            poc.temporal_unpause()
            warm_worker(worker, workers, config, candidate["build_id"], record)
            poc.temporal("set-current-version", "--deployment-name=" + poc.DEPLOYMENT,
                         "--build-id=" + worker, "--allow-no-pollers", "--yes")
            smoke_config["expected_worker_build"] = worker
            if smoke_mode == "full":
                admitted = True
                poc.unpause(config)
                smoke.run(smoke_config, deadline, evidence)
            else:
                # Production admission stays closed; registration refuses task
                # execution and health checks never submit a user task.
                smoke.health(smoke_config, deadline, evidence)
            report["result"] = "passed"
    except Exception as failure:
        error = failure
        report.update(result="failed", failure=safe_error(failure))
    finally:
        release_error = error
        os.environ["NORI_OPERATION_DEADLINE"] = str(end)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        if report.get("mutating") is True or report.get("sandbox_recovery") is True:
            try:
                cleanup_registration(report, workers)
            except Exception as failure:
                error = error or failure
                report.update(result="failed", registration_cleanup_failure=safe_error(failure))
            try:
                report["phase"] = "shutdown"
                poc.pause(config)
                report["compute_stopped"] = True
                # Once task execution was admitted, never roll code back across
                # possibly accepted workflow history or external side effects.
                if release_error and changed and not admitted and before:
                    deploy_image("runtime", before["runtime"])
                    deploy_image("web", before["web"])
                    poc.metadata(before["computer"])
                    poc.temporal("set-current-version", "--deployment-name=" + poc.DEPLOYMENT,
                                 "--build-id=" + before["worker"], "--allow-no-pollers", "--yes")
                    require(snapshot(config) == before, "Previous release could not be verified")
                    report["restored"] = True
            except Exception as failure:
                error = error or failure
                report.update(result="failed", cleanup_failure=safe_error(failure))
        try:
            close_ci_lease(report)
        except Exception as failure:
            error = error or failure
            report.update(result="failed", spend_closed=False, spend_close_failure=safe_error(failure))
        record(phase="finished" if not error else "failed")
        if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
            with open(summary, "a") as stream:
                stream.write(f"\n### {poc.ENVIRONMENT}: {report['result']}\n\n"
                             f"Commit `{candidate['commit']}` · run `{candidate['build_id']}`\n\n"
                             f"Compute stopped: `{report.get('compute_stopped', False)}`. "
                             f"Previous release restored: `{report.get('restored', False)}`.\n")
                if error:
                    stream.write("\nFailure: " + safe_error(error) + "\n")
    if error:
        raise RuntimeError(f"Release failed in {poc.ENVIRONMENT}; inspect {report_path}. {safe_error(error)}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    make = commands.add_parser("manifest", help="Generate the release artifact manifest locally")
    for key in ("commit", "build-id", "web", "runtime", "worker"):
        make.add_argument("--" + key, required=True)
    make.add_argument("--computer", type=Path, required=True)
    preview = commands.add_parser("inspect", help="Validate a manifest without cloud access")
    preview.add_argument("manifest", type=Path)
    plan = commands.add_parser("check-worker-plan", help="Reject destructive or unrelated worker changes")
    plan.add_argument("plan", type=Path)
    plan.add_argument("--build-id", required=True)
    deploy = commands.add_parser("deploy", help="Deploy the exact artifacts from this trusted GitHub run")
    deploy.add_argument("--manifest", type=Path, required=True)
    deploy.add_argument("--computer", type=Path, required=True)
    deploy.add_argument("--environment", choices=("sandbox", "production"), required=True)
    deploy.add_argument("--smoke", choices=("full", "health"), required=True)
    shutdown = commands.add_parser("shutdown", help="Finish compute cleanup after an interrupted GitHub deployment")
    shutdown.add_argument("--environment", choices=("sandbox", "production"), required=True)
    shutdown.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    if args.action == "manifest":
        print(json.dumps(manifest(args), indent=2))
    elif args.action == "inspect":
        candidate = validate_manifest(json.loads(args.manifest.read_text()))
        print(json.dumps({"valid": True, "commit": candidate["commit"], "pipeline_timeout_seconds": PIPELINE_SECONDS,
                          "smoke_timeout_seconds": SMOKE_SECONDS, "failure_policy": "pause_then_restore_before_smoke"}))
    elif args.action == "check-worker-plan":
        require(re.fullmatch(r"[a-z0-9][a-z0-9-]{0,39}", args.build_id), "Invalid worker build ID")
        validate_worker_plan(json.loads(args.plan.read_text()), args.build_id)
        print(json.dumps({"valid": True, "worker_build": args.build_id}))
    elif args.action == "shutdown":
        if not args.report.exists():
            print(json.dumps({"shutdown": "skipped", "reason": "No deployment report"}))
            return
        report = json.loads(args.report.read_text())
        candidate = validate_manifest(report.get("candidate", {}))
        github_identity(candidate)
        require(report.get("environment") == args.environment, "Shutdown report belongs to a different environment")
        require(report.get("commit") == candidate["commit"] and report.get("build_id") == candidate["build_id"],
                "Shutdown report release identity differs from its candidate")
        mutated = report.get("mutating") is True or report.get("sandbox_recovery") is True
        close_needed = args.environment == "sandbox" and report.get("spend_lease_requested") is True and report.get("spend_closed") is not True
        if not mutated and not close_needed:
            print(json.dumps({"shutdown": "skipped", "reason": "Deployment did not mutate this environment"}))
            return
        os.environ["NORI_ENVIRONMENT"] = args.environment
        global poc
        poc = load_module("nori_poc", ROOT.parent / "poc/poc.py")
        os.environ["NORI_OPERATION_DEADLINE"] = str(time.time() + 540)
        error = None
        if mutated:
            try:
                init_backends()
                temporal_key()
                config, workers = poc.config(), poc.temporal_config()
                try:
                    cleanup_registration(report, workers)
                except Exception as failure:
                    error = failure
                    report["registration_cleanup_failure"] = safe_error(failure)
                poc.pause(config)
                report["compute_stopped"] = True
            except Exception as failure:
                error = error or failure
                report["cleanup_failure"] = safe_error(failure)
        if close_needed:
            try:
                # Closing a deterministic scope never issues or reopens a key.
                close_ci_lease(report)
            except Exception as failure:
                error = error or failure
                report.update(spend_closed=False, spend_close_failure=safe_error(failure))
        write_json(args.report, report)
        if error:
            raise RuntimeError("Deployment shutdown incomplete: " + safe_error(error))
    else:
        candidate = validate_manifest(json.loads(args.manifest.read_text()))
        github_identity(candidate)
        require((args.environment, args.smoke) in {("sandbox", "full"), ("production", "health")},
                "Full task validation belongs only in sandbox; production uses read-only health")
        require(hashlib.sha256(args.computer.read_bytes()).hexdigest() == candidate["computer_sha256"],
                "Computer artifact checksum mismatch")
        os.environ["NORI_ENVIRONMENT"] = args.environment
        poc = load_module("nori_poc", ROOT.parent / "poc/poc.py")
        signal.signal(signal.SIGTERM, cancelled)
        signal.signal(signal.SIGINT, cancelled)
        report_path = args.manifest.parent / ("deployment-" + args.environment + ".json")
        release(candidate, args.computer, args.smoke, report_path)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("Release failed: " + safe_error(error), file=sys.stderr)
        raise SystemExit(1)
