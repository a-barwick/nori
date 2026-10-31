#!/usr/bin/env python3
"""Run feature work with a native capped key and existing Firestore budget."""
import argparse
import json
import os
from pathlib import Path
import subprocess

import keys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env", required=True, help="Existing mode-600 main checkout root .env")
    parser.add_argument("--task", required=True, help="Same logical chat ID for every paid command and retry")
    parser.add_argument("--repository", type=Path, default=Path.cwd(), help="Feature worktree for progress checks")
    parser.add_argument("--sandbox", action="store_true", help="Publish the same bounded key to the existing sandbox secret")
    parser.add_argument("--status", action="store_true")
    parser.add_argument("--close", action="store_true")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    env_path = Path(args.env).resolve()
    env = keys.read_env(env_path)
    scope = keys.scope_id("feature:" + args.task)
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    keys.require(command or args.status or args.close, "Provide a command after --, or use --status")
    if args.status:
        print(json.dumps(keys.status(scope)))
        return 0
    management = env.get("OPENROUTER_MANAGEMENT_KEY", "")
    keys.require(management, "Missing OPENROUTER_MANAGEMENT_KEY in main root .env")
    os.environ["OPENROUTER_MANAGEMENT_KEY"] = management
    try:
        if args.close:
            keys.close_ci(scope)
            print(json.dumps({"scope": scope, "closed": True}))
            return 0
        status = keys.ensure_feature(args.task, env_path, args.repository.resolve())
    finally:
        os.environ.pop("OPENROUTER_MANAGEMENT_KEY", None)
    print(json.dumps(status), flush=True)
    keys.require(status["ready"], "Feature allowance is unavailable")
    key = keys.read_env(env_path).get("NORI_SPEND_LEASE_" + scope.upper())
    keys.require(key, "Scoped key is missing; do not substitute a shared key")
    child = dict(os.environ)
    for name in list(child):
        if name.startswith("NORI_SPEND_") or name in ("OPENROUTER_API_KEY", "OPENROUTER_MANAGEMENT_KEY",
                "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "OPENROUTER_BASE_URL"):
            child.pop(name)
    child.update(NORI_SPEND_SCOPE=scope, NORI_SPEND_PROFILE="feature", NORI_SPEND_DATABASE=keys.DATABASE,
                 OPENROUTER_BASE_URL="https://openrouter.ai/api/v1", OPENROUTER_API_KEY=key)
    if args.sandbox:
        child["NORI_SPEND_KEY_SECRET_VERSION"] = keys.publish_sandbox(scope, key)
    return subprocess.call(command, env=child)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (keys.Denied, OSError, ValueError, subprocess.TimeoutExpired) as error:
        print(str(error) if isinstance(error, keys.Denied) else "Spend-scoped command failed; no credentials were printed")
        raise SystemExit(1)
