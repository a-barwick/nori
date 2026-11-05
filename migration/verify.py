#!/usr/bin/env python3
"""Run isolated migration checks and combine current, complete acceptance evidence."""
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


def source_hashes():
    # Include new implementation files, but never ignored credentials, build output or evidence.
    files = subprocess.check_output(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "core", "web", "chat", "migration", "Makefile", "scripts"],
        cwd=ROOT, text=True).splitlines()
    return {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest()
            for name in sorted(set(files)) if (ROOT / name).is_file()}


def evidence_paths(mode, hashes, acceptance):
    """Only changes to code exercised by a check invalidate its evidence."""
    runtime = {name for name in hashes if name.startswith(("core/internal/", "core/cmd/")) or name in ("core/go.mod", "core/go.sum")}
    if mode == "baseline":
        return runtime | {s["file"] for s in PLAN["baseline_suites"]}
    if mode == "browser":
        return runtime | {name for name in hashes if name.startswith(("web/", "chat/"))} | {"migration/contract.ts"}
    if mode == "provider":
        return runtime | {"core/e2e/shared-service-live.test.js", "core/e2e/shared-service-harness.js", "migration/provider-evidence.py"}
    if acceptance == ["shared-budget"]:
        return runtime | {"migration/shared-budget.ts", "core/e2e/shared-service-harness.js"}
    return runtime | {s["file"] for s in PLAN.get("integration_suites", [])} | {"core/e2e/shared-service-harness.js", "migration/contract.ts"}


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
    parser.add_argument("mode", choices=("prep", "baseline", "integration", "browser", "contract", "readiness", "doctor"))
    parser.add_argument("--evidence", action="append", type=Path, default=[], help="Current verifier report to include in readiness; repeat for each check group")
    parser.add_argument("--url", help="Explicit isolated loopback service or Next browser origin; creates test conversations")
    parser.add_argument("--suite", choices=[s["id"] for s in PLAN["baseline_suites"]], help="Run one baseline suite; never counts as the full baseline")
    args = parser.parse_args()
    if args.suite and args.mode != "baseline":
        parser.error("--suite is only valid with baseline")
    if args.evidence and args.mode != "readiness":
        parser.error("--evidence is only valid with readiness")
    if (args.mode in ("contract", "browser")) != bool(args.url):
        parser.error("contract and browser require --url; other modes do not accept it")
    directory = ROOT / ".artifacts/migration" / (time.strftime("%Y%m%d-%H%M%S") + "-" + uuid.uuid4().hex[:8])
    directory.mkdir(parents=True)
    report = {"mode": args.mode, "status": "blocked", "migration_ready": False, "checks": [],
              "plan_sha256": hashlib.sha256((ROOT / "migration/plan.json").read_bytes()).hexdigest(),
              "scope": "Isolated local verification; no production deployment", "suite": args.suite,
              "acceptance": []}
    report["source_hashes"] = source_hashes()
    try:
        report["commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        report["dirty"] = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True).strip())
        if args.mode == "readiness":
            covered = set()
            for path in args.evidence:
                evidence = json.loads(path.read_text())
                paths = evidence_paths(evidence.get("mode"), report["source_hashes"], evidence.get("acceptance"))
                valid = evidence.get("status") == "passed" and all(
                    evidence.get("source_hashes", {}).get(name) == report["source_hashes"].get(name) for name in paths)
                valid = valid and evidence.get("plan_sha256") == report["plan_sha256"]
                valid = valid and evidence.get("mode") in ("baseline", "integration", "browser", "provider")
                valid = valid and bool(evidence.get("checks")) and all(check.get("status") == "passed" for check in evidence["checks"])
                if not valid:
                    raise ValueError(f"Incomplete, stale or unsupported evidence: {path}")
                covered.update(evidence.get("acceptance", []))
            report["checks"] = [{"id": c["id"], "status": "passed" if c["id"] in covered else "not_verified",
                                 "reason": None if c["id"] in covered else c["remaining"]} for c in PLAN["acceptance"]]
            report["migration_ready"] = all(c["status"] == "passed" for c in report["checks"])
            report["status"] = "passed" if report["migration_ready"] else "blocked"
            report["reason"] = "All acceptance evidence is current." if report["migration_ready"] else "Complete service, client, recovery and provider evidence is required. Fixture results cannot certify migration readiness."
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
        elif args.mode in ("contract", "browser"):
            from urllib.parse import urlsplit
            target = urlsplit(args.url)
            if target.scheme != "http" or target.hostname != "127.0.0.1" or target.path not in ("", "/") or target.query or target.fragment or target.username or target.password:
                raise ValueError("Target must be a loopback HTTP origin")
            if args.mode == "browser":
                env = {**environment(), "CHAT_URL": args.url.rstrip("/") + "/chat/index.html", "CHAT_REPORT_DIR": str(directory / "browser")}
                if os.environ.get("CHAT_BROWSER_EXECUTABLE"):
                    env["CHAT_BROWSER_EXECUTABLE"] = os.environ["CHAT_BROWSER_EXECUTABLE"]
                auth = run(["node", "chat/scripts/proxy-auth.mjs"], {**env, "NORI_PROXY_URL": args.url.rstrip("/")}, directory / "proxy.log")
                report["checks"].append(auth)
                if auth["status"] != "passed":
                    raise ValueError("Authenticated proxy verification failed")
                result = run(["node", "chat/scripts/smoke.mjs"], env, directory / "browser.log")
                if result["status"] == "passed":
                    browser = json.loads((directory / "browser/report.json").read_text())
                    if not browser.get("passed") or browser.get("fixture") is not False:
                        raise ValueError("Browser acceptance requires passing real-service evidence for both clients")
                    report["acceptance"] = ["browser-journey", "two-clients", "authorization"]
            else:
                result = run(["bun", "--no-env-file", "test", "migration/contract.test.ts"], {**environment(), "MIGRATION_CONTRACT_URL": args.url}, directory / "contract.log", tests=True)
            report["checks"].append(result)
            report["status"] = result["status"]
            report["scope"] = "Isolated deterministic service and proxy verification; no real-provider or production evidence."
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
            if args.mode == "integration" and os.environ.get("NORI_RECOVERY_OLD_BINARY"):
                env["NORI_RECOVERY_OLD_BINARY"] = os.environ["NORI_RECOVERY_OLD_BINARY"]
            for tool in ("bun", "go", "temporal"):
                if not shutil.which(tool):
                    raise ValueError(f"Missing executable: {tool}")
            suites = PLAN["baseline_suites"] if args.mode == "baseline" else PLAN.get("integration_suites", [])
            if not suites:
                raise ValueError("No integration suites are registered")
            for suite in suites:
                if args.suite and args.suite != suite["id"]:
                    continue
                result = run(["bun", "--no-env-file", "test", suite["file"]], {**env, suite["flag"]: "1"}, directory / f"{args.mode}-{suite['id']}.log", timeout=900, tests=True)
                result["suite"] = suite["id"]
                report["checks"].append(result)
                print(f"Suite {suite['id']}: {result['status']}", flush=True)
                if result["status"] != "passed":
                    break
            expected = 1 if args.suite else len(suites)
            report["status"] = "passed" if len(report["checks"]) == expected and all(c["status"] == "passed" for c in report["checks"]) else "failed"
            report["full_baseline"] = args.mode == "baseline" and not args.suite and report["status"] == "passed"
            if report["status"] == "passed":
                report["acceptance"] = ["assistant-regression"] if report["full_baseline"] else sorted({a for s in suites for a in s.get("acceptance", [])})
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        report["reason"] = str(error)
    path = directory / "report.json"
    path.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))
    print(f"Evidence: {path}")
    return 0 if report["status"] == "passed" else 2


if __name__ == "__main__":
    sys.exit(main())
