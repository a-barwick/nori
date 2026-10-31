#!/usr/bin/env python3
"""Immutable Cloud Build releases. Cloud setup and state migration are separate."""
import argparse
import copy
from datetime import datetime
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
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parent
PROJECT = "bot-111549"
REGISTRY = "us-central1-docker.pkg.dev/bot-111549/nori-poc"
PIPELINE_SECONDS = 1800
SMOKE_SECONDS = 600
RECOVERY_SECONDS = 300
SHA = re.compile(r"[a-f0-9]{40}")
BUILD = re.compile(r"[a-f0-9-]{36}")
TAG = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")


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
    # Never include command output in exceptions: Terraform/state can contain secrets.
    process = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, **kwargs)
    try:
        output, _ = process.communicate(timeout=poc.remaining(900))
    except BaseException:
        process.send_signal(signal.SIGINT)
        try:
            process.communicate(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate()
        raise
    require(process.returncode == 0, f"{Path(args[0]).name} {args[1] if len(args) > 1 else ''} failed (exit {process.returncode})")
    return output.strip()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")
    path.chmod(0o600)


def image(value, component):
    require(isinstance(value, str) and re.fullmatch(re.escape(REGISTRY + "/" + component) + r"@sha256:[a-f0-9]{64}", value),
            f"{component} must be an immutable digest in the Nori registry")
    return value


def validate_manifest(value):
    require(value.get("schema") == 1 and SHA.fullmatch(value.get("commit", "")), "Invalid release source commit")
    require(BUILD.fullmatch(value.get("build_id", "")), "Invalid preparation build ID")
    for component in ("web", "runtime", "worker"):
        image(value.get(component), component)
    digest = value.get("computer_sha256", "")
    require(re.fullmatch(r"[a-f0-9]{64}", digest), "Invalid computer binary SHA256")
    require(value.get("computer_object") == "computer/" + digest, "Unexpected computer object")
    return value


def manifest(args):
    require(SHA.fullmatch(args.commit or ""), "Use the full main commit SHA")
    require(BUILD.fullmatch(args.build_id or ""), "Use the Cloud Build UUID")
    digest = hashlib.sha256(args.computer.read_bytes()).hexdigest()
    return validate_manifest({"schema": 1, "commit": args.commit, "build_id": args.build_id,
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

    def get(self, name, missing=False):
        value = self.request("GET", self.object_url(name) + "?alt=media", missing=missing)
        return None if value is None else json.loads(value)

    def put(self, name, value, generation=None):
        query = {"uploadType": "media", "name": name}
        if generation is not None:
            query["ifGenerationMatch"] = str(generation)
        url = f"https://storage.googleapis.com/upload/storage/v1/b/{quote(self.bucket, safe='')}/o?{urlencode(query)}"
        return self.api("POST", url, value)["generation"]

    def remove(self, name, generation):
        self.request("DELETE", self.object_url(name) + "?ifGenerationMatch=" + str(generation))

    def list(self, prefix):
        items, page = [], None
        while True:
            query = {"prefix": prefix, "fields": "items(name),nextPageToken"}
            if page:
                query["pageToken"] = page
            result = self.api("GET", f"https://storage.googleapis.com/storage/v1/b/{quote(self.bucket, safe='')}/o?{urlencode(query)}")
            items.extend(item["name"] for item in result.get("items", []))
            page = result.get("nextPageToken")
            if not page:
                return items

    def build(self, build_id):
        require(BUILD.fullmatch(build_id or ""), "Invalid Cloud Build ID")
        return self.api("GET", f"https://cloudbuild.googleapis.com/v1/projects/{PROJECT}/locations/{self.region}/builds/{build_id}")

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


def verify_build(candidate, build, trigger):
    validate_manifest(candidate)
    require(build.get("status") == "SUCCESS", "Preparation build did not succeed")
    require(build.get("buildTriggerId") == trigger, "Artifact build used an untrusted trigger")
    substitutions = build.get("substitutions", {})
    require(substitutions.get("BRANCH_NAME") == "main" and substitutions.get("COMMIT_SHA") == candidate["commit"],
            "Artifact build was not for this main commit")
    provenance = build.get("sourceProvenance", {})
    resolved = [provenance.get("resolvedRepoSource", {}).get("commitSha"),
                provenance.get("resolvedGitSource", {}).get("revision"),
                provenance.get("resolvedConnectedRepository", {}).get("revision")]
    require(candidate["commit"] in resolved, "Cloud Build source provenance does not match the release")
    results = build.get("results", {}).get("images", [])
    for component in ("web", "runtime", "worker"):
        repository, digest = candidate[component].split("@")
        require(any(item.get("digest") == digest and item.get("name", "").split(":")[0] == repository for item in results),
                f"Cloud Build did not attest the {component} image")


def trigger_id(cloud, name):
    return cloud.api("GET", f"https://cloudbuild.googleapis.com/v1/projects/{PROJECT}/locations/{cloud.region}/triggers/{name}")["id"]


def prepared_candidate(cloud, commit, build_id):
    require(SHA.fullmatch(commit or "") and BUILD.fullmatch(build_id or ""), "Invalid preparation identity")
    value = cloud.get("candidates/" + commit + "/" + build_id + ".json")
    validate_manifest(value)
    require(value["commit"] == commit and value["build_id"] == build_id, "Candidate path and identity differ")
    verify_build(value, cloud.build(build_id), trigger_id(cloud, "nori-main-prepare"))
    return value


def promoted_candidate(cloud, commit):
    require(SHA.fullmatch(commit or ""), "Use the full main commit SHA")
    candidates = []
    trigger = trigger_id(cloud, "nori-sandbox")
    for name in cloud.list("validated/" + commit + "/"):
        receipt = cloud.get(name)
        candidate = validate_manifest(receipt["candidate"])
        require(candidate["commit"] == commit and receipt.get("environment") == "sandbox", "Invalid sandbox receipt identity")
        require(receipt.get("result") == "passed" and receipt.get("phase") == "finished" and receipt.get("compute_stopped") is True,
                "Sandbox did not pass smoke and shutdown")
        checks = {event.get("check") for event in receipt.get("smoke", []) if event.get("status") == "passed"}
        require({"web_health", "browser_task"}.issubset(checks), "Sandbox smoke evidence is incomplete")
        build = cloud.build(receipt["build_id"])
        if build.get("status") != "SUCCESS":
            continue
        require(build.get("buildTriggerId") == trigger, "Sandbox used an untrusted trigger")
        require(build.get("serviceAccount", "").endswith("/nori-ci-sandbox@bot-111549.iam.gserviceaccount.com"), "Sandbox identity mismatch")
        substitutions = build.get("substitutions", {})
        require(substitutions.get("_SOURCE_COMMIT") == commit and substitutions.get("_PREPARE_BUILD_ID") == candidate["build_id"],
                "Sandbox build did not test these artifacts")
        require(prepared_candidate(cloud, commit, candidate["build_id"]) == candidate, "Sandbox artifact manifest changed")
        candidates.append((build.get("finishTime", ""), candidate))
    require(candidates, "No successful sandbox deployment exists for this main commit")
    return max(candidates, key=lambda pair: pair[0])[1]


def deployment_candidate(cloud, args, build):
    sandbox = poc.ENVIRONMENT == "sandbox"
    require(build.get("buildTriggerId") == trigger_id(cloud, "nori-sandbox" if sandbox else "nori-release"),
            "Deployment used an untrusted trigger")
    account = "nori-ci-sandbox" if sandbox else "nori-ci-deploy"
    require(build.get("serviceAccount", "").endswith("/" + account + "@bot-111549.iam.gserviceaccount.com"), "Deployment identity mismatch")
    substitutions = build.get("substitutions", {})
    if sandbox:
        require(substitutions.get("_SOURCE_COMMIT") == args.commit and substitutions.get("_PREPARE_BUILD_ID") == args.prepare_build,
                "Sandbox event identity mismatch")
        return prepared_candidate(cloud, args.commit, args.prepare_build)
    require(TAG.fullmatch(substitutions.get("TAG_NAME", "")), "Production requires a version tag such as v1.2.3")
    require(substitutions.get("COMMIT_SHA") == args.commit, "Release tag commit mismatch")
    provenance = build.get("sourceProvenance", {})
    require(args.commit in [provenance.get(key, {}).get(field) for key, field in
            (("resolvedRepoSource", "commitSha"), ("resolvedGitSource", "revision"), ("resolvedConnectedRepository", "revision"))],
            "Release tag source provenance mismatch")
    return promoted_candidate(cloud, args.commit)


def prepare(args, cloud):
    value = manifest(args)
    require(os.environ.get("BRANCH_NAME") == "main", "Only the main preparation trigger may publish candidates")
    require(os.environ.get("COMMIT_SHA") == value["commit"] and os.environ.get("BUILD_ID") == value["build_id"], "Build identity mismatch")
    cloud.upload_binary(args.computer, value["computer_object"])
    cloud.put("candidates/" + value["commit"] + "/" + value["build_id"] + ".json", value, 0)
    print(json.dumps({"prepared": value["commit"], "build_id": value["build_id"], "deployable_after": "Cloud Build SUCCESS"}))


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
    info = poc.version_info(build_id, missing_ok=True) or {}
    if any(queue.get("name") == poc.TASK_QUEUE for queue in info.get("taskQueueInfos", [])):
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
        "headers": {"Content-Type": "application/json"}, "body": "e30=",
        "oidcToken": {"serviceAccountEmail": config["conversation_wake_service_account"], "audience": root}}}})
    poc.api("POST", "https://cloudtasks.googleapis.com/v2/" + name + ":run", {})
    end = time.monotonic() + poc.remaining(180)
    while time.monotonic() < end:
        if poc.api("GET", "https://cloudtasks.googleapis.com/v2/" + name, allow404=True) is None:
            info = poc.version_info(build_id, missing_ok=True) or {}
            require(any(q.get("name") == poc.TASK_QUEUE for q in info.get("taskQueueInfos", [])), "Worker request did not register the expected task queue")
            return
        time.sleep(2)
    raise RuntimeError("Worker registration request did not finish; pause must verify drain")


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
    routes = []
    for value in (before, after):
        for key in computed:
            value.pop(key, None)
        template = value["template"][0]
        template.pop("revision", None)  # Provider-generated revision identifier.
        for container in template["containers"]:
            container.pop("build_info", None)
            env = {item["name"]: item for item in container["env"]}
            routes.append(json.loads(env.pop("NORI_WORKER_SERVICES")["value"]))
            container["env"] = sorted(env.values(), key=lambda item: item["name"])
    return (len(routes) == 2 and routes[1] == {**routes[0], build_id: worker_url(build_id)} and before == after)


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


def worker_plan(cloud, candidate, directory):
    retained = cloud.get("deployments/" + poc.ENVIRONMENT + "/worker-releases.json", missing=True)
    if retained is None:
        require(poc.ENVIRONMENT == "production", "Initialize the sandbox retained worker manifest during setup")
        retained = json.loads((ROOT.parent / "temporal/terraform/worker-releases.json").read_text())
    state = json.loads(run(["terraform", "-chdir=" + str(poc.TEMPORAL_TF), "show", "-json"]))
    resources = state.get("values", {}).get("root_module", {}).get("resources", [])
    pools = {resource["index"]: resource["values"]["template"][0]["containers"][0]["image"]
             for resource in resources if resource.get("type") == "google_cloud_run_v2_worker_pool"}
    services = {resource["index"]: resource["values"]["template"][0]["containers"][0]["image"]
                for resource in resources if resource.get("type") == "google_cloud_run_v2_service" and resource.get("name") == "worker"}
    targets = state.get("values", {}).get("outputs", {}).get("request_worker_targets", {}).get("value", {})
    require(pools == retained["worker_images"] and services == retained.get("request_worker_images", {}) and
            targets == retained.get("request_worker_targets", {}), "Retained worker manifest differs from state; reconcile before releasing")
    build_id = "ci-" + candidate["commit"][:12] + "-" + candidate["build_id"][:8]
    target = {"deployment_name": poc.DEPLOYMENT, "task_queue": poc.TASK_QUEUE,
              "wake_queue_name": "nori-worker" if poc.ENVIRONMENT == "production" else "nori-sandbox-worker",
              "poll_window_seconds": 240, "firestore_database": "nori-poc" if poc.ENVIRONMENT == "production" else "nori-sandbox"}
    require(build_id not in pools and retained.get("request_worker_images", {}).get(build_id, candidate["worker"]) == candidate["worker"] and
            retained.get("request_worker_targets", {}).get(build_id, target) == target, "A retained worker build cannot change image or routing target")
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


def cancelled(_signum, _frame):
    raise RuntimeError("Release cancelled")


def deployment_lock(cloud, prefix, build_id, report_path):
    while True:
        try:
            return cloud.put(prefix + "lock.json", {"build_id": build_id, "report": report_path}, 0)
        except CloudError as error:
            if error.status != 412 or poc.ENVIRONMENT != "sandbox":
                raise
            owner = cloud.get(prefix + "lock.json", missing=True)
            if owner is None:
                continue
            require(cloud.build(owner["build_id"]).get("status") in {"QUEUED", "WORKING", "PENDING"},
                    "Sandbox has an unresolved deployment lock; inspect its report")
            # Never steal or expire a lock. Native concurrent builds wait only
            # within their own forward-operation budget.
            time.sleep(min(5, poc.remaining(5)))


def release(cloud, candidate, build_id, started):
    end = started + PIPELINE_SECONDS - 30
    os.environ["NORI_OPERATION_DEADLINE"] = str(end - RECOVERY_SECONDS)
    report = {"commit": candidate["commit"], "candidate": candidate, "environment": poc.ENVIRONMENT,
              "build_id": build_id, "phase": "preflight", "result": "pending"}
    prefix = "deployments/" + poc.ENVIRONMENT + "/"
    report_path = prefix + build_id + ".json"
    lock = deployment_lock(cloud, prefix, build_id, report_path)
    config, mutating, changed, smoke_started, error = None, False, False, False, None
    def record(**values):
        report.update(values)
        cloud.put(report_path, report)
        print(json.dumps({"phase": report["phase"], "result": report["result"], "report": report_path}), flush=True)
    try:
        if poc.ENVIRONMENT == "sandbox":
            created = cloud.build(candidate["build_id"])["createTime"]
            report["source_created"] = created
            previous = cloud.get(prefix + "current.json", missing=True)
            if previous:
                timestamp = lambda value: datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()
                require(timestamp(created) >= timestamp(previous["source_created"]), "A newer sandbox preparation has already deployed")
        record()
        init_backends()
        temporal_key()
        config = poc.config()
        workers = poc.temporal_config()  # Refuse another environment before any mutation.
        poc.production_workers(workers)
        for queue in poc.worker_queue_names(workers, all_queues=True):
            require(poc.api("GET", "https://cloudtasks.googleapis.com/v2/" + queue).get("state") == "PAUSED", "Release requires all worker queues already paused")
        poc.wait_worker_requests(workers)
        os.environ["TF_VAR_environment"] = poc.ENVIRONMENT
        os.environ["TF_VAR_runtime_invoke_root"] = config["runtime_url"]
        smoke = load_module("nori_smoke", ROOT / "smoke.py")
        account = "nori-ci-sandbox" if poc.ENVIRONMENT == "sandbox" else "nori-ci-deploy"
        smoke_config = {**config, "project": PROJECT, "temporal_deployment": poc.DEPLOYMENT,
                        "iap_service_account": account + "@bot-111549.iam.gserviceaccount.com"}
        before = snapshot(config)
        smoke.preflight(smoke_config, time.monotonic() + poc.remaining(120))
        record(previous=before)
        with tempfile.TemporaryDirectory(prefix="nori-release-") as tmp:
            directory = Path(tmp)
            binary = directory / "computer-agent"
            run(["gcloud", "--quiet", "storage", "cp", "gs://" + cloud.bucket + "/" + candidate["computer_object"], str(binary)])
            require(hashlib.sha256(binary.read_bytes()).hexdigest() == candidate["computer_sha256"], "Computer artifact checksum mismatch")
            worker, retained, plan = worker_plan(cloud, candidate, directory)
            require(snapshot(config) == before, "Deployment changed during preparation")
            record(phase="deploy", retained_workers=retained, worker_build=worker)
            mutating = True
            run(["terraform", "-chdir=" + str(poc.TEMPORAL_TF), "apply", "-input=false", str(plan)])
            cloud.put(prefix + "worker-releases.json", retained)
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
            smoke_config["baseline_ids"] = smoke.preflight(smoke_config, time.monotonic() + poc.remaining(120))
            record(phase="smoke")
            available = poc.remaining(SMOKE_SECONDS)
            require(available > 30, "Insufficient time remains for smoke and shutdown")
            deadline = time.monotonic() + available
            os.environ["NORI_OPERATION_DEADLINE"] = str(time.time() + available)
            smoke_started = True  # Registration opens admission too; restoration ends here.
            poc.temporal_unpause()  # Fence queues and verify all retained pools have no scaler.
            poc.control(config, False)
            warm_worker(worker, workers, config, build_id, record)
            poc.temporal("set-current-version", "--deployment-name=" + poc.DEPLOYMENT, "--build-id=" + worker, "--allow-no-pollers", "--yes")
            poc.unpause(config)
            smoke_config["expected_worker_build"] = worker
            smoke.run(smoke_config, deadline, lambda event: record(smoke=[*report.get("smoke", []), event]))
            report["result"] = "passed"
    except Exception as failure:
        error = failure
        report.update(result="failed", failure=safe_error(failure))
    finally:
        os.environ["NORI_OPERATION_DEADLINE"] = str(end)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        if mutating:
            try:
                poc.pause(config)
                report["compute_stopped"] = True
                # Restore only before any smoke input or model activity was admitted.
                if error and changed and not smoke_started:
                    previous = report["previous"]
                    deploy_image("runtime", previous["runtime"])
                    deploy_image("web", previous["web"])
                    poc.metadata(previous["computer"])
                    poc.temporal("set-current-version", "--deployment-name=" + poc.DEPLOYMENT, "--build-id=" + previous["worker"], "--allow-no-pollers", "--yes")
                    require(snapshot(config) == previous, "Previous release could not be verified")
                    report["restored"] = True
            except Exception as failure:
                error = error or failure
                report.update(result="failed", unresolved=safe_error(failure))
        # Only a verified restoration can clear a failed deployment's lock.
        # Partial Terraform state and possibly accepted smoke work need an operator.
        report["phase"] = "restored" if report.get("restored") else "finished" if not error else "operator-action-required"
        os.environ["NORI_OPERATION_DEADLINE"] = str(started + PIPELINE_SECONDS - 5)
        record()
        if not error and poc.ENVIRONMENT == "sandbox":
            cloud.put(prefix + "current.json", report)
        if not error or not mutating or report.get("restored"):
            cloud.remove(prefix + "lock.json", lock)
    if not error and poc.ENVIRONMENT == "sandbox":
        cloud.put("validated/" + candidate["commit"] + "/" + build_id + ".json", report, 0)
    if error:
        raise RuntimeError("Release failed; inspect " + report_path + ". " + safe_error(error))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    for name in ("manifest", "prepare"):
        sub = commands.add_parser(name, help="Generate an offline manifest" if name == "manifest" else "Publish preparation artifacts")
        for key in ("commit", "build-id", "web", "runtime", "worker"):
            sub.add_argument("--" + key, required=True)
        sub.add_argument("--computer", type=Path, required=True)
    check_plan = commands.add_parser("check-worker-plan", help="Read-only validation of Terraform show -json for a request-worker release")
    check_plan.add_argument("plan", type=Path)
    check_plan.add_argument("--build-id", required=True)
    preview = commands.add_parser("inspect", help="Validate an artifact manifest locally; no cloud access")
    preview.add_argument("manifest", type=Path)
    release = commands.add_parser("release", help="Deploy a successful preparation for an explicitly selected main commit")
    release.add_argument("--commit", required=True)
    release.add_argument("--prepare-build", default="")
    release.add_argument("--check-only", action="store_true", help="Verify trigger, provenance and promotion evidence without changing cloud resources")
    args = parser.parse_args()
    os.umask(0o077)
    if args.action == "check-worker-plan":
        require(re.fullmatch(r"[a-z0-9][a-z0-9-]{0,39}", args.build_id), "Invalid worker build ID")
        validate_worker_plan(json.loads(args.plan.read_text()), args.build_id)
        print(json.dumps({"valid": True, "environment": poc.ENVIRONMENT, "worker_build": args.build_id}))
        return
    if args.action == "manifest":
        print(json.dumps(manifest(args), indent=2))
        return
    if args.action == "inspect":
        value = validate_manifest(json.loads(args.manifest.read_text()))
        print(json.dumps({"valid": True, "commit": value["commit"], "pipeline_timeout_seconds": PIPELINE_SECONDS,
                          "smoke_timeout_seconds": SMOKE_SECONDS, "failure_policy": "pause_then_restore_before_smoke",
                          "cloud_provenance_verified": False}, indent=2))
        return
    build_id = os.environ.get("BUILD_ID", "")
    require(BUILD.fullmatch(build_id), "Run mutating commands only through the configured Cloud Build triggers")
    os.environ["NORI_OPERATION_DEADLINE"] = str(time.time() + PIPELINE_SECONDS - 30)
    cloud = Cloud()
    if args.action == "prepare":
        prepare(args, cloud)
    else:
        signal.signal(signal.SIGTERM, cancelled)
        signal.signal(signal.SIGINT, cancelled)
        build = cloud.build(build_id)
        candidate = deployment_candidate(cloud, args, build)
        if args.check_only:
            print(json.dumps({"environment": poc.ENVIRONMENT, "candidate": candidate, "web": poc.WEB,
                              "computer": poc.VM, "task_queue": poc.TASK_QUEUE, "deployment": poc.DEPLOYMENT}, indent=2))
            return
        started = datetime.fromisoformat(build["startTime"].replace("Z", "+00:00")).timestamp()
        release(cloud, candidate, build_id, started)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Never dump API responses, child stderr, environment values or state.
        print("Release failed: " + safe_error(error), file=sys.stderr)
        raise SystemExit(1)
