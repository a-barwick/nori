#!/usr/bin/env python3
"""Preparation and baseline checks. No paid inference or deployment commands."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
PLAN = json.loads((ROOT / "migration/plan.json").read_text())


def environment():
    # Do not inherit project credentials or integration configuration into tests.
    names = ("PATH", "HOME", "TMPDIR", "JAVA_HOME", "GOROOT", "GOPATH", "GOCACHE", "GOMODCACHE")
    return {name: os.environ[name] for name in names if name in os.environ}


def endpoint(name):
    value = os.environ.get(name, "")
    match = re.fullmatch(r"127\.0\.0\.1:([0-9]{1,5})", value)
    if not match or not 0 < int(match[1]) < 65536:
        raise ValueError(f"{name} must be an explicit 127.0.0.1:port")
    with socket.create_connection(("127.0.0.1", int(match[1])), timeout=2):
        pass
    return value


def run(command, env, log, cwd=ROOT, timeout=600, tests=False):
    with log.open("w") as output:
        try:
            # New process group lets timeout clean up only processes started by this command.
            process = subprocess.Popen(command, cwd=cwd, env=env, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
            try:
                code = process.wait(timeout=timeout)
            except (subprocess.TimeoutExpired, KeyboardInterrupt):
                import signal
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
                return {"command": command, "status": "failed", "reason": "Timed out or interrupted", "log": str(log)}
        except OSError as error:
            return {"command": command, "status": "blocked", "reason": str(error), "log": str(log)}
    text = re.sub(r"\x1b\[[0-9;]*m", "", log.read_text())
    passed = re.search(r"\b([0-9]+) pass\b", text)
    skips = re.search(r"\b([1-9][0-9]*) skip\b", text)
    valid = code == 0 and (not tests or (passed and int(passed[1]) > 0 and not skips))
    return {"command": command, "status": "passed" if valid else "failed", "exit_code": code,
            "reason": "Zero skips and at least one passing test required" if tests and not valid else None, "log": str(log)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("prep", "baseline", "contract", "readiness", "doctor"))
    parser.add_argument("--url", help="Draft contract only: explicit isolated loopback service; creates test conversations")
    parser.add_argument("--suite", choices=[s["id"] for s in PLAN["baseline_suites"]], help="Run one baseline suite; never counts as the full baseline")
    args = parser.parse_args()
    if args.suite and args.mode != "baseline":
        parser.error("--suite is only valid with baseline")
    if (args.mode == "contract") != bool(args.url):
        parser.error("contract requires --url; other modes do not accept it")
    directory = ROOT / ".artifacts/migration" / (time.strftime("%Y%m%d-%H%M%S") + "-" + uuid.uuid4().hex[:8])
    directory.mkdir(parents=True)
    report = {"mode": args.mode, "status": "blocked", "migration_ready": False, "checks": [],
              "plan_sha256": hashlib.sha256((ROOT / "migration/plan.json").read_bytes()).hexdigest(),
              "scope": "Local preparation only; no production or real-provider evidence", "suite": args.suite}
    sources = {}
    for folder in (ROOT / "migration", ROOT / "chat"):
        for path in sorted(folder.rglob("*")):
            if path.is_file() and not any(part in ("node_modules", "dist", "__pycache__") for part in path.parts) and path.suffix not in (".pyc",):
                sources[str(path.relative_to(ROOT))] = hashlib.sha256(path.read_bytes()).hexdigest()
    report["source_hashes"] = sources
    try:
        report["commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        report["dirty"] = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True).strip())
        if args.mode == "readiness":
            report["checks"] = [{"id": c["id"], "status": "not_verified", "reason": c["remaining"]} for c in PLAN["acceptance"]]
            report["reason"] = "The migration is not implemented. Fixture and baseline results cannot certify it."
        elif args.mode == "doctor":
            for tool in ("bun", "go", "temporal", "java"):
                report["checks"].append({"id": tool, "status": "passed" if shutil.which(tool) else "blocked"})
            for name in ("FIRESTORE_EMULATOR_HOST", "TEMPORAL_ADDRESS"):
                try:
                    endpoint(name)
                    report["checks"].append({"id": name, "status": "passed"})
                except (ValueError, OSError) as error:
                    report["checks"].append({"id": name, "status": "blocked", "reason": str(error)})
            report["status"] = "passed" if all(c["status"] == "passed" for c in report["checks"]) else "blocked"
        elif args.mode == "contract":
            from urllib.parse import urlsplit
            target = urlsplit(args.url)
            if target.scheme != "http" or target.hostname != "127.0.0.1" or target.path not in ("", "/") or target.query or target.fragment or target.username or target.password:
                raise ValueError("Contract target must be a loopback HTTP origin")
            result = run(["bun", "--no-env-file", "test", "migration/contract.test.ts"], {**environment(), "MIGRATION_CONTRACT_URL": args.url}, directory / "contract.log", tests=True)
            report["checks"].append(result)
            report["status"] = result["status"]
            report["scope"] = "Draft HTTP contract only; target must advertise isolated-test with deterministic providers. Not full migration acceptance."
        elif args.mode == "prep":
            ids = [c["id"] for c in PLAN["acceptance"]]
            if len(set(ids)) != len(ids) or not ids:
                raise ValueError("Acceptance IDs must be unique and nonempty")
            for phase in PLAN["phases"]:
                if not phase["acceptance"] or any(i not in ids for i in phase["acceptance"]):
                    raise ValueError("Every phase must reference known acceptance checks")
            for suite in PLAN["baseline_suites"]:
                if not (ROOT / suite["file"]).is_file():
                    raise ValueError("A baseline suite is missing")
            commands = [
                (["bun", "--no-env-file", "test", "migration/contract.test.ts", "migration/verifier.test.ts"], True),
                (["bun", "--no-env-file", "run", "--cwd", "chat", "check"], False),
                (["bun", "--no-env-file", "run", "--cwd", "chat", "build"], False),
            ]
            for index, (command, tests) in enumerate(commands):
                result = run(command, environment(), directory / f"prep-{index}.log", tests=tests)
                report["checks"].append(result)
                if result["status"] != "passed":
                    break
            report["status"] = "passed" if len(report["checks"]) == len(commands) and all(c["status"] == "passed" for c in report["checks"]) else "failed"
        else:
            env = environment()
            env["FIRESTORE_EMULATOR_HOST"] = endpoint("FIRESTORE_EMULATOR_HOST")
            env["TEMPORAL_ADDRESS"] = endpoint("TEMPORAL_ADDRESS")
            for tool in ("bun", "go", "temporal"):
                if not shutil.which(tool):
                    raise ValueError(f"Missing executable: {tool}")
            for suite in PLAN["baseline_suites"]:
                if args.suite and args.suite != suite["id"]:
                    continue
                result = run(["bun", "--no-env-file", "test", suite["file"]], {**env, suite["flag"]: "1"}, directory / f"baseline-{suite['id']}.log", timeout=900, tests=True)
                result["suite"] = suite["id"]
                report["checks"].append(result)
                print(f"Suite {suite['id']}: {result['status']}", flush=True)
                if result["status"] != "passed":
                    break
            expected = 1 if args.suite else len(PLAN["baseline_suites"])
            report["status"] = "passed" if len(report["checks"]) == expected and all(c["status"] == "passed" for c in report["checks"]) else "failed"
            report["full_baseline"] = not args.suite and report["status"] == "passed"
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        report["reason"] = str(error)
    path = directory / "report.json"
    path.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))
    print(f"Evidence: {path}")
    return 0 if report["status"] == "passed" else 2


if __name__ == "__main__":
    sys.exit(main())
