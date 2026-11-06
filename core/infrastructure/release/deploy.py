#!/usr/bin/env python3
"""Deploy Nori to production from this Mac. See core/infrastructure/README.md.

A deploy never opens admission, so nothing runs on new code until `make unpause`.
That makes every failure safe to roll back automatically. An interrupted deploy
keeps its progress; running `make deploy` again resumes it.
"""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import socket
import subprocess
import sys
import tempfile
import time
import uuid
from urllib.parse import quote, urlencode

ROOT = Path(__file__).resolve().parents[3]
CORE = ROOT / "core"
OWNER = "austinbarwickdavis@gmail.com"
PROJECT = "bot-111549"
REGISTRY = "us-central1-docker.pkg.dev/bot-111549/nori-poc"
STATE_BUCKET = "bot-111549-nori-ci-state"
LOCK = "nori/deploy.lock"
TERMINAL = {"completed", "cancelled", "failed"}
CAPABILITY_SECRETS = ("nori-brave-search-api-key", "nori-google-calendar-client-id", "nori-google-calendar-client-secret", "nori-google-calendar-refresh-token", "nori-google-calendar-account")
# Steps a rollback undoes. Images and the new worker service stay for the retry.
UNDONE_BY_ROLLBACK = {"computer", "runtime", "web", "register", "current", "verify"}

os.environ["NORI_TF_BACKEND"] = "gcs"
os.environ["NORI_ENVIRONMENT"] = "production"
os.environ.setdefault("NORI_ENV_FILE", str(ROOT / ".env"))
sys.dont_write_bytecode = True
_spec = importlib.util.spec_from_file_location("nori_ops", CORE / "infrastructure/app/ops.py")
ops = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ops)
DIR = ops.STATE_ROOT / "deploy"
STATE = DIR / "state.json"


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def log(message):
    print(f"[{time.strftime('%H:%M:%S')}] {message}", flush=True)


def run(args, **kwargs):
    """Stdout stays private. Failures surface only Error lines, with tokens redacted."""
    result = subprocess.run(args, capture_output=True, text=True, **kwargs)
    if result.returncode:
        clean = re.sub(r"\x1b\[[0-9;]*m", "", result.stderr)
        titles = [line.strip(" │") for line in clean.splitlines() if line.strip(" │").startswith(("Error:", "ERROR:"))]
        detail = re.sub(r"(ya29\.[\w.-]+|eyJ[\w-]+\.[\w-]+\.[\w-]+)", "[redacted]", "; ".join(titles[:3])[:1500])
        raise RuntimeError(f"{Path(args[0]).name} {args[1] if len(args) > 1 else ''} failed (exit {result.returncode})" + (": " + detail if detail else ""))
    return result.stdout.strip()


def load_state():
    return json.loads(STATE.read_text()) if STATE.exists() else None


def save_state(state):
    # A crash leaves either the complete old state or the complete new one.
    DIR.mkdir(parents=True, mode=0o700, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", dir=DIR, delete=False) as stream:
        json.dump(state, stream, indent=2)
    Path(stream.name).replace(STATE)


# Deploy lock: threads and machines share production, so only one deploys at a time.
def lock_url(upload=False):
    if upload:
        return f"https://storage.googleapis.com/upload/storage/v1/b/{STATE_BUCKET}/o?" + urlencode({"uploadType": "media", "name": LOCK, "ifGenerationMatch": 0})
    return f"https://storage.googleapis.com/storage/v1/b/{STATE_BUCKET}/o/{quote(LOCK, safe='')}"


def acquire_lock(commit):
    body = {"commit": commit, "host": socket.gethostname(), "pid": os.getpid(), "owner": uuid.uuid4().hex,
            "since": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    try:
        return ops.api("POST", lock_url(upload=True), body)["generation"]
    except RuntimeError as error:
        if "HTTP 412" not in str(error):
            raise
        raise RuntimeError("Another deploy holds the lock, including for this commit. Run make deploy-unlock to inspect it; recover only after its owner has stopped.") from None


def release_lock(generation):
    # A recovered/replaced lock belongs to someone else, even for the same commit.
    ops.api("DELETE", lock_url() + "?" + urlencode({"ifGenerationMatch": generation}), allow404=True)


def refuse_open_work(c):
    """Never interrupt running work; preserve failed, already-attempted input."""
    root = ops.firestore(c, "owners/" + quote(OWNER, safe=""))
    control = ops.api("GET", root + "/conversation/control", allow404=True)
    if control:
        f = control.get("fields", {})
        count = lambda key: int(f.get(key, {}).get("integerValue", "0"))
        # A failed turn consumes its attempt but leaves user_routed behind.
        # While paused, its saved input can survive a repair deployment. It is
        # retried only after a user follow-up; never mark it routed here.
        paused = ops.api("GET", ops.firestore(c, "system/control"))["fields"]["paused"].get("booleanValue", False)
        routed_or_waiting = count("user_received") == count("user_routed") or (paused and count("protocol_version") == 2)
        require(count("received") == count("consumed") and routed_or_waiting and not f.get("active", {}).get("stringValue"),
                "Nori has unprocessed messages. Wait for them to settle, then deploy again.")
    cursor = ""
    while True:
        query = [("pageSize", "300"), ("mask.fieldPaths", "status"), ("mask.fieldPaths", "id")] + ([("pageToken", cursor)] if cursor else [])
        page = ops.api("GET", root + "/runs?" + urlencode(query), allow404=True) or {}
        for doc in page.get("documents", []):
            status = doc.get("fields", {}).get("status", {}).get("stringValue")
            require(status in TERMINAL, f"Task {doc['fields'].get('id', {}).get('stringValue')} is still {status}. Wait for it or stop it, then deploy again.")
        cursor = page.get("nextPageToken", "")
        if not cursor:
            return


def init_terraform():
    # The app stack's directory is `app`, but its remote state prefix is still `nori/poc`. Changing the prefix would orphan the state.
    for name, prefix in (("app", "nori/poc"), ("temporal", "nori/temporal")):
        directory = CORE / "infrastructure" / name / "terraform"
        (directory / "release_backend_override.tf").write_text('terraform {\n  backend "gcs" {}\n}\n')
        run(["terraform", f"-chdir={directory}", "init", "-input=false", "-reconfigure", f"-backend-config=bucket={STATE_BUCKET}", f"-backend-config=prefix={prefix}"])
        require(json.loads(run(["terraform", f"-chdir={directory}", "output", "-json"])), f"{name} Terraform state is empty; refusing to continue")


def build_image(name, file, context, commit, extra=()):
    tag = f"{REGISTRY}/{name}:{commit}"
    existing = subprocess.run(["gcloud", "--project", PROJECT, "artifacts", "docker", "images", "describe", tag, "--format=value(image_summary.digest)"], capture_output=True, text=True)
    if existing.returncode == 0 and existing.stdout.strip():
        log(f"{name} image already built for this commit")
        return f"{REGISTRY}/{name}@{existing.stdout.strip()}"
    log(f"Building {name} image")
    metadata, output = DIR / f"{name}-image.json", DIR / f"{name}-build.log"
    with output.open("w") as stream:
        result = subprocess.run(["docker", "buildx", "build", "--platform", "linux/amd64", "--provenance=false", "--sbom=false", "--progress=plain",
                                 "--build-arg", "SENTRY_RELEASE=" + commit, "--metadata-file", str(metadata), "--push", "-t", tag, "-f", str(file), *extra, str(context)],
                                stdout=stream, stderr=subprocess.STDOUT)
    require(result.returncode == 0, f"{name} image build failed; see {output}")
    return f"{REGISTRY}/{name}@{json.loads(metadata.read_text())['containerimage.digest']}"


def web_image(commit):
    """Every production web image uploads maps with the existing CI credential."""
    token = None
    try:
        value = run(["gcloud", "--project", PROJECT, "secrets", "versions", "access", "latest", "--secret=nori-sentry-auth-token"])
        require(value, "The source-map upload credential is empty")
        fd, token = tempfile.mkstemp(dir=DIR)
        with os.fdopen(fd, "w") as stream:
            stream.write(value)
        extra = ("--build-arg", "SENTRY_ORG=austin-barwick", "--build-arg", "SENTRY_PROJECT=nori-web", "--secret", f"id=sentry_auth_token,src={token}")
        return build_image("web", ROOT / "web/Dockerfile", ROOT, commit, extra)
    finally:
        if token:
            os.unlink(token)


def build_all(state):
    commit = state["commit"]
    git_ok = not run(["git", "-C", str(ROOT), "status", "--porcelain", "--", "core", "web", "Makefile"])
    require(git_ok, "core/ or web/ has uncommitted changes")
    with ThreadPoolExecutor(2) as pool:
        def go_images():
            return (build_image("runtime", CORE / "infrastructure/Dockerfile", CORE, commit),
                    build_image("worker", CORE / "infrastructure/temporal/Dockerfile.worker", CORE, commit))
        go, web = pool.submit(go_images), pool.submit(web_image, commit)
        state["runtime"], state["worker"] = go.result()
        state["web"] = web.result()
    binary = DIR / "computer-agent"
    run(["go", "build", "-trimpath", "-o", str(binary), "./cmd/computer-agent"], cwd=CORE, env={**os.environ, "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"})
    state["computer_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()


def read_previous(c):
    control = ops.api("GET", ops.firestore(c, "system/control"))
    vm = ops.api("GET", ops.vm_url())
    require(control.get("fields", {}).get("paused", {}).get("booleanValue") is True and vm["status"] == "TERMINATED", "Nori must be paused with its computer stopped")
    web = ops.api("GET", f"https://run.googleapis.com/v2/projects/{PROJECT}/locations/us-central1/services/{ops.WEB}")
    runtime = ops.api("GET", "https://us-central1-aiplatform.googleapis.com/v1beta1/" + c["runtime_name"])
    items = {item["key"]: item["value"] for item in vm["metadata"].get("items", [])}
    return {"web": web["template"]["containers"][0]["image"], "runtime": runtime["spec"]["containerSpec"]["imageUri"],
            "runtime_deployment": ops.runtime_deployment(runtime), "worker": current_worker(),
            "computer": {key: items[key] for key in ("computer-release-object", "computer-release-sha256")}}


def current_worker():
    routing = ops.temporal("describe", "--name=" + ops.DEPLOYMENT).get("routingConfig", {})
    require(not routing.get("rampingVersionPercentage", 0), "A Temporal ramp is active; finish it before deploying")
    build = routing.get("currentVersionBuildID") or routing.get("currentDeploymentVersion", {}).get("buildId")
    require(build, "Cannot identify the Current Temporal worker")
    return build


def capability_secrets_ready():
    """A secret reference without a version would stop the worker from starting."""
    for secret in CAPABILITY_SECRETS:
        try:
            enabled = run(["gcloud", "--project", PROJECT, "secrets", "versions", "list", secret, "--filter=state=enabled", "--limit=1", "--format=value(name)"])
        except RuntimeError:
            enabled = ""
        if not enabled.strip():
            log(f"{secret} has no enabled version; web search and calendar stay unavailable in this release")
            return False
    return True


def worker_vars(c, build, image):
    """Terraform state is the worker inventory, so every retained worker is carried forward."""
    state = json.loads(run(["terraform", f"-chdir={ops.TEMPORAL_TF}", "show", "-json"]))
    resources = state["values"]["root_module"]["resources"]
    outputs = {key: value["value"] for key, value in state["values"]["outputs"].items()}
    images = lambda kind, name: {r["index"]: r["values"]["template"][0]["containers"][0]["image"] for r in resources if r["type"] == kind and r["name"] == name}
    services = images("google_cloud_run_v2_service", "worker")
    targets = {key: {k: v for k, v in value.items() if v is not None} for key, value in outputs["request_worker_targets"].items()}
    require(set(targets) == set(services), "Terraform worker outputs are incomplete; refresh the state first")
    # A new worker keeps the Current worker's model. Only Austin changes models.
    target = dict(targets[current_worker()])
    target["capabilities"] = capability_secrets_ready()
    require(target["deployment_name"] == ops.DEPLOYMENT and target["firestore_database"] == "nori-poc", "Current worker target is not production")
    require(services.get(build, image) == image and targets.get(build, target) == target, "This build already exists with a different image or target")
    services[build] = image
    targets.setdefault(build, target)
    return {"worker_images": images("google_cloud_run_v2_worker_pool", "worker"), "request_worker_images": services, "request_worker_targets": targets,
            "scalable_build_ids": outputs["scalable_build_ids"], "spend_alert": outputs["spend_alert_config"],
            "observability": outputs["observability_config"], "runtime_invoke_root": c["runtime_url"]}


def route_only_update(change, build):
    """A retained worker may only learn the new worker's route."""
    computed = {"conditions", "create_time", "creator", "delete_time", "effective_annotations", "effective_labels", "etag", "expire_time", "generation",
                "last_modifier", "latest_created_revision", "latest_ready_revision", "observed_generation", "reconciling", "terminal_condition",
                "terraform_labels", "traffic_statuses", "uid", "update_time", "uri", "urls"}
    before, after = copy.deepcopy(change["before"]), copy.deepcopy(change["after"])
    routes = []
    for value in (before, after):
        for key in computed:
            value.pop(key, None)
        template = value["template"][0]
        template.pop("revision", None)
        for container in template["containers"]:
            container.pop("build_info", None)
            env = {item["name"]: item for item in container["env"]}
            routes.append(json.loads(env.pop("NORI_WORKER_SERVICES")["value"]))
            container["env"] = sorted(env.values(), key=lambda item: item["name"])
    return len(routes) == 2 and set(routes[1]) - set(routes[0]) == {build} and {k: routes[1][k] for k in routes[0]} == routes[0] and before == after


def check_plan(plan, build):
    """Refuse anything beyond adding this worker, above all changes to retained data."""
    allowed = {f'google_cloud_run_v2_service.worker["{build}"]', f'google_cloud_run_v2_service_iam_member.worker_invoke["{build}"]'}
    created = []
    for resource in plan.get("resource_changes", []):
        actions = resource["change"]["actions"]
        if actions in (["no-op"], ["read"]):
            continue
        if resource["type"] == "google_cloud_run_v2_service" and resource["name"] == "worker" and actions == ["update"] and route_only_update(resource["change"], build):
            continue
        require(resource["address"] in allowed and actions == ["create"], f"Terraform plan would {'/'.join(actions)} {resource['address']}. Nothing was applied; review this by hand.")
        created.append(resource["address"])
    return created


def plan_worker(c, build, image):
    variables, plan_file = DIR / "worker-vars.json", DIR / "worker.plan"
    variables.write_text(json.dumps(worker_vars(c, build, image), indent=2))
    run(["terraform", f"-chdir={ops.TEMPORAL_TF}", "plan", "-input=false", "-lock-timeout=60s", f"-var-file={variables}", f"-out={plan_file}"])
    plan_file.chmod(0o600)
    return check_plan(json.loads(run(["terraform", f"-chdir={ops.TEMPORAL_TF}", "show", "-json", str(plan_file)])), build), plan_file


def ops_command(*args):
    run([sys.executable, str(ops.ROOT / "ops.py"), *args], cwd=CORE)


def register_worker(c, build):
    """Have the worker check in with Temporal. A paused system refuses to run any task on this request."""
    def registered():
        info = ops.version_info(build, missing_ok=True) or {}
        kinds = {str(q.get("type", "")).lower().removeprefix("task_queue_type_") for q in info.get("taskQueuesInfos", []) if q.get("name") == ops.TASK_QUEUE}
        return {"workflow", "activity"} <= kinds
    if registered():
        return
    workers = ops.temporal_config()
    queue, root = workers["worker_wake_queue"], ops.production_workers(workers)[build]
    require(ops.api("GET", "https://cloudtasks.googleapis.com/v2/" + queue).get("state") == "PAUSED", "Worker registration requires a paused queue")
    name = f"{queue}/tasks/nori-deploy-{uuid.uuid4().hex}"
    ops.api("POST", "https://cloudtasks.googleapis.com/v2/" + queue + "/tasks", {"task": {"name": name, "dispatchDeadline": "720s", "httpRequest": {
        "url": root + "/run", "httpMethod": "POST", "headers": {"Content-Type": "application/json"},
        "body": base64.b64encode(b'{"registration_only":true}').decode(),
        "oidcToken": {"serviceAccountEmail": c["conversation_wake_service_account"], "audience": root}}}})
    ops.api("POST", "https://cloudtasks.googleapis.com/v2/" + name + ":run", {})
    end, retry_after, delay = time.monotonic() + 300, time.monotonic() + 5, 5
    try:
        while time.monotonic() < end:
            task = ops.api("GET", "https://cloudtasks.googleapis.com/v2/" + name, allow404=True)
            attempt = (task or {}).get("lastAttempt", {})
            code = attempt.get("responseStatus", {}).get("code", 0)
            require(not attempt.get("responseTime") or code in (0, 7), f"Worker registration failed (delivery code {code})")
            # A new invoker permission can take a moment to apply; only that is retried.
            if attempt.get("responseTime") and code == 7 and time.monotonic() >= retry_after:
                ops.api("POST", "https://cloudtasks.googleapis.com/v2/" + name + ":run", {})
                delay = min(30, delay * 2)
                retry_after = time.monotonic() + delay
            if task is None and registered():
                return
            time.sleep(2)
        raise RuntimeError("Worker registration did not finish within 5 minutes")
    finally:
        task = ops.api("GET", "https://cloudtasks.googleapis.com/v2/" + name, allow404=True)
        if task and task.get("lastAttempt", {}).get("responseTime"):
            ops.api("DELETE", "https://cloudtasks.googleapis.com/v2/" + name, allow404=True)


def verify(c, state):
    previous = read_previous(c)  # Also proves Nori is still paused with its computer stopped.
    require(previous["web"] == state["web"] and previous["runtime"] == state["runtime"], "Live images do not match this deploy")
    require(all(previous["runtime_deployment"][key] == value for key, value in ops.runtime_settings().items()), "Live runtime capacity settings do not match this deploy")
    require(previous["worker"] == state["build"], "Temporal Current is not the new worker")
    require(previous["computer"]["computer-release-sha256"] == state["computer_sha256"], "The computer points at a different release")
    web = ops.api("GET", f"https://run.googleapis.com/v2/projects/{PROJECT}/locations/us-central1/services/{ops.WEB}")
    require(web.get("terminalCondition", {}).get("state") == "CONDITION_SUCCEEDED", "The web service is not ready")
    ops.compatible_builds(ops.temporal_config())  # The same check unpause makes.
    end = time.monotonic() + 180
    while True:
        try:
            ops.api("POST", c["runtime_url"].rstrip("/") + "/api/healthz", {})
            return
        except (RuntimeError, OSError) as error:
            if time.monotonic() > end:
                raise RuntimeError(f"The runtime is not healthy: {error}") from None
            time.sleep(5)


def rollback(c, state):
    """Nothing has run on the new code while paused, so restoring all of it is safe."""
    previous = state.get("previous")
    attempted = set(state["done"]) | set(state.get("started", []))
    if "pause" in attempted:
        # Pause can fail after changing admission or queues. Reconcile it before
        # touching releases, and verify live state before reporting recovery.
        ops.pause(c)
        read_previous(c)
    if previous and UNDONE_BY_ROLLBACK & attempted:
        log("Restoring the previous release")
        if "runtime_deployment" in previous:
            ops.patch_runtime(c, previous["runtime"], previous["runtime_deployment"])
        else:
            # Allow recovery of progress saved before runtime settings were captured.
            ops_command("deploy-runtime", previous["runtime"])
        ops_command("deploy-web", previous["web"])
        ops.metadata(previous["computer"])
        ops.temporal("set-current-version", "--deployment-name=" + ops.DEPLOYMENT, "--build-id=" + previous["worker"], "--allow-no-pollers", "--yes")
        restored = read_previous(c)
        require(all(restored.get(key) == value for key, value in previous.items()), "The previous release could not be verified")
    # A retry pauses and reads the live release again, in case Nori was used in between.
    state["done"] = [step for step in state["done"] if step not in UNDONE_BY_ROLLBACK and step != "pause"]
    state.pop("previous", None)
    state.pop("started", None)
    state["status"] = "rolled_back"
    save_state(state)


def interrupted(_signal, _frame):
    raise RuntimeError("Deploy interrupted")


def deploy(_args):
    commit = run(["git", "-C", str(ROOT), "rev-parse", "HEAD"])
    run(["git", "-C", str(ROOT), "fetch", "-q", "origin", "main"])
    require(commit == run(["git", "-C", str(ROOT), "rev-parse", "origin/main"]), "Deploys run from the latest main; pull or merge first")
    generation = acquire_lock(commit)
    try:
        deploy_locked(commit)
    finally:
        release_lock(generation)


def deploy_locked(commit):
    # Read shared progress only after acquiring the production lock.
    state = load_state()
    if state and state["status"] == "running":
        require(state["commit"] == commit, f"A deploy of {state['commit'][:10]} was interrupted. Check out that commit to resume it, or run make deploy-rollback.")
        log("Resuming the interrupted deploy")
    elif not state or state["commit"] != commit or state["status"] == "finished":
        state = {"commit": commit, "build": "rel-" + commit[:12], "done": []}
    state["status"] = "running"
    DIR.mkdir(parents=True, mode=0o700, exist_ok=True)
    save_state(state)
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)

    def step(name, action):
        if name not in state["done"]:
            log(name.capitalize())
            if name in UNDONE_BY_ROLLBACK or name in {"pause", "worker"}:
                state.setdefault("started", []).append(name)
                save_state(state)
            action()
            state["done"].append(name)
            save_state(state)

    c = None
    try:
        ops.runtime_settings()  # Refuse invalid capacity policy before building or changing production.
        step("check", lambda: run(["make", "check"], cwd=ROOT))
        step("build", lambda: build_all(state))
        init_terraform()
        c = ops.config()
        if "pause" not in state["done"]:
            refuse_open_work(c)
        step("pause", lambda: ops.pause(c))
        if "previous" not in state:
            state["previous"] = read_previous(c)
            save_state(state)

        def apply_worker():
            created, plan_file = plan_worker(c, state["build"], state["worker"])
            log("Terraform will add: " + (", ".join(created) or "nothing new"))
            run(["terraform", f"-chdir={ops.TEMPORAL_TF}", "apply", "-input=false", str(plan_file)])
            plan_file.unlink()
        step("worker", apply_worker)
        step("computer", lambda: ops_command("publish-computer", str(DIR / "computer-agent")))
        step("runtime", lambda: ops_command("deploy-runtime", state["runtime"]))
        step("web", lambda: ops_command("deploy-web", state["web"]))
        step("register", lambda: (ops.temporal_unpause(), register_worker(c, state["build"])))
        step("current", lambda: ops.temporal("set-current-version", "--deployment-name=" + ops.DEPLOYMENT, "--build-id=" + state["build"], "--allow-no-pollers", "--yes"))
        step("verify", lambda: verify(c, state))
    except Exception as error:
        log(f"Deploy failed: {error}")
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        attempted = set(state["done"]) | set(state.get("started", []))
        touched = "pause" in attempted
        restored = bool(state.get("previous") and UNDONE_BY_ROLLBACK & attempted)
        try:
            rollback(c, state)
        except Exception as recovery_error:
            raise RuntimeError(f"Production may be partially changed; recovery failed: {recovery_error}. Keep Nori closed. Run make pause, inspect make deploy-status, then make deploy-rollback before reopening.") from None
        outcome = ("Verified the previous release and left Nori paused." if restored else
                   "Pause was attempted; Nori is now verified paused. No release switch was attempted.") if touched else "Production was not changed."
        raise RuntimeError(outcome + " Fix the cause and run make deploy again; finished steps are reused.") from None
    state["status"] = "finished"
    save_state(state)
    log(f"Deployed {commit[:10]}: worker {state['build']}. Nori is paused; run make unpause to use it.")


def check(_args):
    """A read-only rehearsal: real Terraform plan and live reads, with the Current image as a stand-in."""
    commit = run(["git", "-C", str(ROOT), "rev-parse", "HEAD"])
    log("Local check")
    run(["make", "check"], cwd=ROOT)
    log("Runtime capacity settings: " + json.dumps(ops.runtime_settings(), sort_keys=True))
    log("Terraform state")
    DIR.mkdir(parents=True, mode=0o700, exist_ok=True)
    init_terraform()
    c = ops.config()
    log("Open work")
    refuse_open_work(c)
    log("Worker plan")
    current = current_worker()
    state = json.loads(run(["terraform", f"-chdir={ops.TEMPORAL_TF}", "show", "-json"]))
    image = next(r["values"]["template"][0]["containers"][0]["image"] for r in state["values"]["root_module"]["resources"]
                 if r["type"] == "google_cloud_run_v2_service" and r["name"] == "worker" and r["index"] == current)
    created, plan_file = plan_worker(c, "rel-" + commit[:12], image)
    plan_file.unlink()
    log("The plan only adds: " + ", ".join(created))
    log("Runtime health")
    ops.api("POST", c["runtime_url"].rstrip("/") + "/api/healthz", {})
    log("Ready to deploy. Nothing was changed.")


def status(_args):
    init_terraform()
    c = ops.config()
    control = ops.api("GET", ops.firestore(c, "system/control"))
    saved = load_state() or {}
    print(json.dumps({"paused": control.get("fields", {}).get("paused", {}).get("booleanValue"), "computer": ops.api("GET", ops.vm_url())["status"],
                      "current_worker": current_worker(), "last_deploy": {key: saved.get(key) for key in ("commit", "build", "status", "done")}}, indent=2))


def manual_rollback(_args):
    generation = acquire_lock("rollback")
    try:
        state = load_state()
        require(state and state["status"] == "running", "No interrupted deploy to roll back")
        init_terraform()
        c = ops.config()
        rollback(c, state)
        log("Recovery completed. Check make deploy-status before reopening.")
    finally:
        release_lock(generation)


def unlock(args):
    metadata = ops.api("GET", lock_url(), allow404=True)
    if not metadata:
        log("No deploy lock exists")
        return
    generation = metadata["generation"]
    holder = ops.api("GET", lock_url() + "?" + urlencode({"alt": "media", "generation": generation}))
    print(json.dumps({"generation": generation, "holder": holder}, indent=2))
    require(args.lock_generation == generation, "Confirm the owner has stopped, then run: python3 core/infrastructure/release/deploy.py unlock --lock-generation " + generation)
    if holder.get("host") == socket.gethostname() and holder.get("pid"):
        try:
            os.kill(int(holder["pid"]), 0)
        except ProcessLookupError:
            pass
        else:
            raise RuntimeError("The local lock owner is still running; refusing to unlock")
    release_lock(generation)
    log("Abandoned lock removed. Resume make deploy or run make deploy-rollback.")


def smoke(_args):
    """Send one real browser task through production. It costs model money and appears in Austin's conversation."""
    init_terraform()
    c = ops.config()
    refuse_open_work(c)
    base = c["runtime_url"].rstrip("/") + "/api/"
    def call(operation, body):
        for attempt in range(20):  # The runtime may be starting from zero.
            try:
                return ops.api("POST", base + operation, body)
            except RuntimeError as error:
                if not re.search(r"HTTP (429|503)$", str(error)) or attempt == 19:
                    raise
                time.sleep(3)
    session = call("session", {})
    require(not session.get("paused"), "Nori is paused; run make unpause first")
    before = {r.get("id") for r in session.get("runs", [])}
    input_id = str(uuid.uuid4())
    call("turn", {"id": input_id, "message": "Start exactly one task: navigate to https://example.org using the browser, capture a screenshot, then finish with a short "
                  "description. Do not ask questions, run shell commands, fill forms, or start other tasks. Include this identifier in the task request: nori-smoke-" + input_id})
    log("Message sent; waiting for the task")
    end = time.monotonic() + 600
    while time.monotonic() < end:
        session = call("session", {})
        failed = [t for t in session.get("traces", []) if input_id in t.get("input_ids", []) and t.get("status") == "error"]
        require(not failed, "Nori failed before starting the task: " + str(failed[0].get("error")) if failed else "")
        new = [r for r in session.get("runs", []) if r.get("id") not in before]
        if new and new[0].get("status") in TERMINAL:
            require(new[0]["status"] == "completed", f"The task ended as {new[0]['status']}")
            steps = call("trace", {"run_id": new[0]["id"]}).get("steps", [])
            names = {s.get("name") for s in steps if s.get("status") == "completed"}
            require({"browser_navigate", "browser_screenshot"} <= names, "The task finished without using the browser")
            log(f"Passed: task {new[0]['id']} browsed and took a screenshot. Run make pause when you are done.")
            return
        time.sleep(3)
    raise RuntimeError("The task did not finish within 10 minutes")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    commands = {"deploy": deploy, "check": check, "status": status, "rollback": manual_rollback, "unlock": unlock, "smoke": smoke}
    parser.add_argument("command", choices=commands)
    parser.add_argument("--lock-generation", help="Observed generation of an abandoned lock, for unlock only")
    args = parser.parse_args()
    os.umask(0o077)
    try:
        commands[args.command](args)
    except Exception as error:
        print("Failed: " + (str(error) if isinstance(error, RuntimeError) else type(error).__name__ + ": " + str(error)), file=sys.stderr)
        raise SystemExit(1)


if __name__ == "__main__":
    main()
