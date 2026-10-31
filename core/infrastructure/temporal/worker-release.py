#!/usr/bin/env python3
"""Prepare an immutable worker release and a reviewable plan; never deploy it."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parent
CORE = ROOT.parents[1]
STATE = Path.home() / ".local/state/nori/bot-111549/temporal/terraform.tfstate"
REGISTRY = "us-central1-docker.pkg.dev/bot-111549/nori-poc/worker"


def run(args, **kwargs):
    return subprocess.check_output(args, text=True, **kwargs).strip()


def source_digest():
    digest = hashlib.sha256()
    files = [CORE / "go.mod", CORE / "go.sum", ROOT / "Dockerfile.worker"]
    for directory in (CORE / "cmd", CORE / "internal"):
        files.extend(p for p in directory.rglob("*") if p.is_file() and p.suffix == ".go")
    for path in sorted(files):
        digest.update(str(path.relative_to(CORE)).encode() + b"\0" + path.read_bytes() + b"\0")
    return digest.hexdigest()


def save(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as out:
        json.dump(value, out, indent=2)
        out.write("\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--build-id", required=True)
    parser.add_argument("--temporal-deployment", default="nori-task")
    parser.add_argument("--temporal-task-queue", default="nori-tasks")
    parser.add_argument("--firestore-database", choices=["nori-poc", "nori-worker-candidate"], default="nori-poc")
    parser.add_argument("--poll-window-seconds", type=int, default=240, help="1..240; shorter slices support isolated drain verification")
    parser.add_argument("--wake-queue", default="nori-worker", help="shared production queue or a separate candidate queue")
    parser.add_argument("--builder", help="OCI-capable Docker Buildx builder (for example, a docker-container builder)")
    parser.add_argument("--image", help="already built immutable registry image URI; omit to prepare a local OCI archive")
    parser.add_argument("--var-file", action="append", type=Path, default=[], help="additional Terraform configuration, e.g. the optional spend alert settings")
    parser.add_argument("--state", type=Path, default=STATE)
    parser.add_argument("--output", type=Path, help="new owner-only directory; defaults beside external Terraform state")
    args = parser.parse_args()
    if os.environ.get("NORI_ENVIRONMENT", "production") != "production":
        parser.error("This legacy local helper is production-only; use the hosted nori-sandbox trigger")
    if os.environ.get("NORI_TF_BACKEND") == "gcs":
        parser.error("This local preparation helper cannot manage migrated state; use the hosted nori-main-prepare and nori-release triggers")
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,39}", args.build_id):
        parser.error("build-id must be lowercase DNS-safe and at most 40 characters")
    target = {"deployment_name": args.temporal_deployment, "task_queue": args.temporal_task_queue, "wake_queue_name": args.wake_queue, "poll_window_seconds": args.poll_window_seconds, "firestore_database": args.firestore_database}
    if not all(re.fullmatch(r"[a-z0-9][a-z0-9-]{0,127}", value) for value in (args.temporal_deployment, args.temporal_task_queue)) or not re.fullmatch(r"nori-worker(?:-[a-z0-9][a-z0-9-]{0,50})?", args.wake_queue):
        parser.error("use DNS-safe Temporal names and a nori-worker queue name")
    if not 1 <= args.poll_window_seconds <= 240:
        parser.error("poll-window-seconds must be between 1 and 240")
    production = (args.temporal_deployment == "nori-task", args.temporal_task_queue == "nori-tasks", args.wake_queue == "nori-worker")
    if any(production) and not all(production):
        parser.error("candidate releases must isolate deployment, Temporal task queue and wake queue together")
    if all(production) and (args.poll_window_seconds != 240 or args.firestore_database != "nori-poc"):
        parser.error("production must retain poll=240 seconds and database=nori-poc")
    if not any(production) and args.firestore_database != "nori-worker-candidate":
        parser.error("isolated candidates require --firestore-database nori-worker-candidate")
    releases = json.loads((ROOT / "terraform/worker-releases.json").read_text())
    if args.build_id in releases["worker_images"] or args.build_id in releases.get("request_worker_images", {}):
        parser.error("build-id already retained; create a new immutable version")
    if args.image and not re.fullmatch(re.escape(REGISTRY) + r"@sha256:[a-f0-9]{64}", args.image):
        parser.error("image must be the worker registry URI with a sha256 digest")
    for var_file in args.var_file:
        if not var_file.is_file():
            parser.error("var-file does not exist: " + str(var_file))
    output = (args.output or args.state.parent / "releases" / args.build_id).resolve()
    output.mkdir(parents=True, mode=0o700, exist_ok=False)
    os.chmod(output, 0o700)
    fingerprint = source_digest()
    image = args.image
    archive = None
    archive_digest = None
    if image is None:
        # This creates only a local OCI artifact. Its digest is the later publish
        # identity; upload this exact archive preserving digests before apply.
        archive = output / "worker.oci.tar"
        metadata = output / "image-metadata.json"
        epoch = run(["git", "show", "-s", "--format=%ct", "HEAD"], cwd=CORE)
        builder = ["--builder=" + args.builder] if args.builder else []
        run(["docker", "buildx", "build", *builder, "--platform=linux/amd64", "--provenance=false", "--sbom=false",
             "--build-arg=SOURCE_DATE_EPOCH=" + epoch, "--metadata-file=" + str(metadata),
             "--output=type=oci,dest=" + str(archive), "--file=" + str(ROOT / "Dockerfile.worker"), str(CORE)])
        os.chmod(archive, 0o600)
        os.chmod(metadata, 0o600)
        digest = json.loads(metadata.read_text()).get("containerimage.digest", "")
        if not re.fullmatch(r"sha256:[a-f0-9]{64}", digest):
            raise RuntimeError("Builder did not return an immutable image digest")
        image = REGISTRY + "@" + digest
        with archive.open("rb") as source:
            archive_digest = hashlib.file_digest(source, "sha256").hexdigest()
    if source_digest() != fingerprint:
        raise RuntimeError("Worker sources changed during preparation; prepare again after edits settle")
    releases.setdefault("request_worker_images", {})[args.build_id] = image
    releases.setdefault("request_worker_targets", {})[args.build_id] = target
    manifest = output / "worker-releases.json"
    save(manifest, releases)
    release = {"host": "service", "build_id": args.build_id, "target": target, "image": image, "source_sha256": fingerprint,
               "source_commit": run(["git", "rev-parse", "HEAD"], cwd=CORE),
               "source_dirty": bool(run(["git", "status", "--porcelain"], cwd=CORE)),
               "oci_archive": str(archive) if archive else None, "archive_sha256": archive_digest,
               "state": str(args.state.resolve()), "manifest": str(manifest),
               "additional_var_files": [str(path.resolve()) for path in args.var_file],
               "plan": str(output / "release.plan"), "applied": False, "promoted": False}
    save(output / "release.json", release)
    plan = output / "release.plan"
    try:
        result = run(["terraform", "-chdir=" + str(ROOT / "terraform"), "plan", "-no-color", "-input=false",
                      "-state=" + str(args.state.resolve()), "-var-file=" + str(manifest),
                      *["-var-file=" + str(path.resolve()) for path in args.var_file], "-out=" + str(plan)])
    finally:
        if plan.exists():
            os.chmod(plan, 0o600)
    (output / "plan.txt").write_text(result + "\n")
    os.chmod(output / "plan.txt", 0o600)
    print(json.dumps({"prepared": str(output), "image": image, "plan": str(plan),
                      "next": "Review plan.txt. Publishing, applying this exact plan, registering and promoting remain separate authorized actions."}, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, subprocess.CalledProcessError) as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
