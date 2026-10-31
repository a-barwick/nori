#!/usr/bin/env python3
"""Capped native OpenRouter keys; existing Firestore holds budgets, never keys."""
import base64
import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import ssl
import subprocess
import urllib.error
import urllib.parse
import urllib.request

PROJECT = "bot-111549"
DATABASE = "nori-sandbox"
WORKSPACE = "b30e5fe0-68dc-43dd-a9ce-094b9ad3019d"
ROOT = f"https://firestore.googleapis.com/v1/projects/{PROJECT}/databases/{DATABASE}/documents"
SCOPES = ROOT + "/development_spend_scopes"
POOL = ROOT + "/development_spend_pools/global"
KEY_SECRET = f"projects/{PROJECT}/secrets/nori-ci-spend-token"
SCALE = 1_000_000_000


class Denied(RuntimeError):
    pass


class APIError(Denied):
    def __init__(self, status):
        self.status = status
        super().__init__(f"Provider API returned HTTP {status}; no automatic retry")


def require(ok, message):
    if not ok:
        raise Denied(message)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def request(method, url, token, body=None):
    req = urllib.request.Request(url, method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
    ca = "/etc/ssl/cert.pem"
    context = ssl.create_default_context(cafile=ca if Path(ca).exists() else None)
    try:
        with urllib.request.build_opener(NoRedirect, urllib.request.HTTPSHandler(context=context)).open(req, timeout=30) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        raise APIError(error.code) from None
    except (OSError, ValueError):
        raise Denied("Provider API outcome unknown; inspect before retrying") from None


def cloud_api(method, url, body=None):
    # GitHub uses its existing WIF-authenticated Cloud.api in place of this.
    result = subprocess.run(["gcloud", "--quiet", "--project=" + PROJECT, "auth", "print-access-token"],
                            capture_output=True, text=True, timeout=30)
    require(result.returncode == 0 and result.stdout.strip(), "GCP login unavailable")
    return request(method, url, result.stdout.strip(), body)


def native(method, path, body=None):
    key = os.environ.get("OPENROUTER_MANAGEMENT_KEY", "")
    require(key, "Missing OPENROUTER_MANAGEMENT_KEY")
    return request(method, "https://openrouter.ai/api/v1" + path, key, body)


def read_env(path):
    require(path.is_file() and path.stat().st_mode & 0o077 == 0, "Use the existing mode-600 root .env")
    return {key.strip().removeprefix("export "): value.strip().strip("\"'")
            for line in path.read_text().splitlines() if "=" in line and not line.lstrip().startswith("#")
            for key, value in [line.split("=", 1)]}


def save_env(path, values):
    read_env(path)
    require(subprocess.run(["git", "check-ignore", "--quiet", str(path)], cwd=path.parent).returncode == 0,
            "Project root .env must be gitignored")
    with path.open("r+") as file:
        fcntl.flock(file, fcntl.LOCK_EX)
        lines = [line for line in file.read().splitlines() if line.split("=", 1)[0].strip() not in values]
        lines.extend(f"{key}={value}" for key, value in values.items())
        file.seek(0)
        file.write("\n".join(lines) + "\n")
        file.truncate()
        file.flush()
        os.fsync(file.fileno())


def now():
    return dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def fields(values):
    result = {}
    for key, value in values.items():
        kind = "booleanValue" if type(value) is bool else "integerValue" if type(value) is int else "timestampValue" if key in ("expires_at", "created_at") else "stringValue"
        result[key] = {kind: str(value) if type(value) is int else value}
    return result


def values(document):
    return {key: int(value["integerValue"]) if "integerValue" in value else next(iter(value.values()))
            for key, value in document.get("fields", {}).items()}


def scope_id(task):
    return hashlib.sha256(task.encode()).hexdigest()


def ci_scope(commit):
    require(re.fullmatch(r"[a-f0-9]{40}", commit), "CI needs a full source commit")
    return scope_id("ci:nori:" + commit + ":release")


def get_scope(scope):
    require(re.fullmatch(r"[a-f0-9]{64}", scope), "Invalid spend scope")
    return cloud_api("GET", SCOPES + "/" + scope)


def update(scope, changes, version=None):
    query = [("updateMask.fieldPaths", key) for key in changes]
    query.append(("currentDocument.updateTime", version)) if version else query.append(("currentDocument.exists", "true"))
    return cloud_api("PATCH", SCOPES + "/" + scope + "?" + urllib.parse.urlencode(query), {"fields": fields(changes)})


def status(scope):
    value = values(get_scope(scope))
    pool = values(cloud_api("GET", POOL))
    stamp = now()
    pending = pool.get("pending_nanos", 0)
    daily = pool.get("day_known_nanos", 0) if pool.get("day") == stamp[:10] else 0
    monthly = pool.get("month_known_nanos", 0) if pool.get("month") == stamp[:7] else 0
    ready = (value.get("issuance_state") == "ready" and not value.get("closed", True) and
             value.get("expires_at", "") > stamp and value.get("used_nanos", 0) < value.get("allowance_nanos", 0) and
             not pool.get("blocked", True) and 0 <= pending < 2 * SCALE and
             0 <= daily < 2 * SCALE - pending and 0 <= monthly < 20 * SCALE - pending)
    return {"scope": scope, "profile": value.get("profile"), "ready": ready,
            "limit_usd": value.get("allowance_nanos", 0) / SCALE, "accounted_usd": value.get("used_nanos", 0) / SCALE,
            "expires_at": value.get("expires_at"), "secret_version": value.get("secret_version")}


def ensure_pool():
    try:
        cloud_api("GET", POOL)
        return
    except Exception as error:
        if getattr(error, "status", None) != 404:
            raise
    # Initialize once only. A deleted ledger must never refresh existing tasks.
    existing = cloud_api("GET", SCOPES + "?pageSize=1")
    require(not existing.get("documents"), "Shared budget is missing for existing scopes; restore its ledger")
    stamp = now()
    try:
        cloud_api("POST", ROOT + "/development_spend_pools?documentId=global", {"fields": fields({
            "day": stamp[:10], "month": stamp[:7], "day_known_nanos": 0, "month_known_nanos": 0,
            "pending_nanos": 0, "blocked": False})})
    except Exception as error:
        if getattr(error, "status", None) != 409:
            raise


def check_workspace():
    response = native("GET", f"/workspaces/{WORKSPACE}/budgets")
    limits = {row["reset_interval"]: row["limit_usd"] for row in response.get("data", [])}
    require(response.get("include_byok_in_budgets") and 0 < limits.get("daily", 0) <= 2 and
            0 < limits.get("monthly", 0) <= 20, "Development workspace $2/day and $20/month caps are missing")


def ensure(task, profile, persist_key, fingerprint=""):
    require(profile in ("ci", "feature"), "Invalid spend profile")
    ensure_pool()
    scope = scope_id(task)
    try:
        document = get_scope(scope)
    except Exception as error:
        if getattr(error, "status", None) != 404:
            raise
        document = None
    if document is None:
        check_workspace()
        expires = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=24)).strftime("%Y-%m-%dT%H:%M:%SZ")
        value = {"profile": profile, "allowance_nanos": 100_000_000 if profile == "ci" else 250_000_000,
                 "used_nanos": 0, "closed": True, "created_at": now(), "expires_at": expires, "issuance_state": "creating",
                 "fingerprints": json.dumps([fingerprint] if fingerprint else [])}
        # Create-only claim prevents concurrent launchers minting fresh budgets.
        # A crash leaves it closed for reconciliation, never silently resets it.
        cloud_api("POST", SCOPES + "?documentId=" + scope, {"fields": fields(value)})
        result = native("POST", "/keys", {"name": "nori-dev:" + task[:180], "workspace_id": WORKSPACE,
            "limit": value["allowance_nanos"] / SCALE, "limit_reset": None,
            "include_byok_in_limit": True, "expires_at": expires})
        key_hash = result["data"]["hash"]
        update(scope, {"native_key_hash": key_hash})
        try:
            extra = persist_key(scope, result["key"])
            update(scope, {**extra, "issuance_state": "ready", "closed": False})
        except BaseException:
            native("PATCH", "/keys/" + key_hash, {"disabled": True})
            raise
    else:
        value = values(document)
        require(value.get("profile") == profile and value.get("issuance_state") == "ready", "Scope issuance incomplete; inspect before retrying")
        require(value["expires_at"] > now(), "Scope expired; retry cannot create a fresh allowance")
        require(value["used_nanos"] < value["allowance_nanos"], "Logical task allowance exhausted")
        native("PATCH", "/keys/" + value["native_key_hash"],
               {"limit": value["allowance_nanos"] / SCALE, "disabled": False})
        if value.get("closed"):
            update(scope, {"closed": False}, document["updateTime"])
    return status(scope)


def ensure_ci(commit):
    ci_scope(commit)  # Validate before any API calls.
    def persist(scope, key):
        response = cloud_api("POST", "https://secretmanager.googleapis.com/v1/" + KEY_SECRET + ":addVersion",
                             {"payload": {"data": base64.b64encode(key.encode()).decode()}})
        version = response["name"].rsplit("/", 1)[-1]
        require(version.isdigit(), "Invalid native secret version")
        return {"secret_version": version}
    return ensure("ci:nori:" + commit + ":release", "ci", persist)


def publish_sandbox(scope, key):
    """Bind the existing feature key to the retained sandbox runtime secret."""
    value = values(get_scope(scope))
    require(value.get("profile") == "feature" and status(scope)["ready"], "Sandbox needs a ready feature allowance")
    secret = f"projects/{PROJECT}/secrets/nori-sandbox-openrouter-api-key"
    cloud_api("GET", "https://secretmanager.googleapis.com/v1/" + secret)
    if value.get("secret_version"):
        return str(value["secret_version"])
    result = cloud_api("POST", "https://secretmanager.googleapis.com/v1/" + secret + ":addVersion",
                       {"payload": {"data": base64.b64encode(key.encode()).decode()}})
    version = result["name"].rsplit("/", 1)[-1]
    require(version.isdigit(), "Invalid sandbox secret version")
    update(scope, {"secret_version": version})
    return version


def close_ci(scope):
    try:
        value = values(get_scope(scope))
    except Exception as error:
        if getattr(error, "status", None) == 404:
            return
        raise
    update(scope, {"closed": True})
    if value.get("native_key_hash"):
        native("PATCH", "/keys/" + value["native_key_hash"], {"disabled": True})


def fingerprint(repository):
    def git(*args):
        return subprocess.check_output(["git", "-C", str(repository), *args], timeout=10, stderr=subprocess.DEVNULL)
    digest = hashlib.sha256(git("rev-parse", "HEAD") + git("diff", "HEAD", "--", ".", ":(exclude)*.md"))
    for raw in sorted(git("ls-files", "--others", "--exclude-standard", "-z").split(b"\0")):
        if raw:
            path = repository / os.fsdecode(raw)
            if path.suffix in (".go", ".py", ".ts", ".tsx", ".js", ".rs", ".sh", ".yaml", ".tf") and not path.is_symlink():
                require(path.stat().st_size <= 1_000_000, "Source file exceeds progress check limit")
                digest.update(raw + path.read_bytes())
    return digest.hexdigest()


def ensure_feature(task, env_path, repository):
    scope = scope_id("feature:" + task)
    mark = fingerprint(repository)
    try:
        document = get_scope(scope)
    except Exception as error:
        if getattr(error, "status", None) != 404:
            raise
        document = None
    if document:
        value = values(document)
        seen = json.loads(value.get("fingerprints", "[]"))
        if (value.get("profile") == "feature" and value.get("issuance_state") == "ready" and value["expires_at"] > now() and value["allowance_nanos"] < SCALE and
                value["used_nanos"] >= value["allowance_nanos"] * .7 and mark not in seen):
            allowance = value["allowance_nanos"] + 250_000_000
            # Optimistic precondition prevents two wrappers granting twice for
            # the same changed source. Native key remains no higher than $1.
            update(scope, {"allowance_nanos": allowance, "fingerprints": json.dumps([*seen, mark])}, document["updateTime"])
            native("PATCH", "/keys/" + value["native_key_hash"], {"limit": allowance / SCALE})
    def persist(scope, key):
        save_env(env_path, {"NORI_SPEND_LEASE_" + scope.upper(): key})
        return {}
    return ensure("feature:" + task, "feature", persist, mark)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Populate the existing CI management-key secret without printing credentials")
    parser.add_argument("--env", required=True, type=Path)
    parser.add_argument("action", choices=["install-ci-secret"])
    args = parser.parse_args()
    try:
        key = read_env(args.env).get("OPENROUTER_MANAGEMENT_KEY")
        require(key, "Missing OPENROUTER_MANAGEMENT_KEY in project root .env")
        secret = f"projects/{PROJECT}/secrets/nori-ci-spend-management-key"
        cloud_api("GET", "https://secretmanager.googleapis.com/v1/" + secret)
        response = cloud_api("POST", "https://secretmanager.googleapis.com/v1/" + secret + ":addVersion",
                             {"payload": {"data": base64.b64encode(key.encode()).decode()}})
        print(json.dumps({"management_secret_version": response["name"], "credential_value": "not printed"}))
    except (Denied, OSError, ValueError, subprocess.TimeoutExpired) as error:
        print(str(error) if isinstance(error, Denied) else "CI credential setup failed; inspect configuration")
        raise SystemExit(1)
