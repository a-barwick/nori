#!/usr/bin/env python3
"""Bounded hosted release smoke; the caller must keep owner activity quiescent."""
import json
import signal
import subprocess
import time
import uuid
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

OWNER = "austinbarwickdavis@gmail.com"
TERMINAL = {"completed", "cancelled", "failed"}
METADATA = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"

class SmokeError(RuntimeError):
    pass

class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None

OPENER = build_opener(NoRedirect())

def _left(deadline):
    remaining = deadline - time.monotonic()
    if remaining <= 0: raise SmokeError("Smoke deadline exceeded")
    return remaining

def _request(url, deadline, body=None, token=None, metadata=False, expected=200):
    headers = {"Content-Type": "application/json"} if body is not None else {}
    if token:
        headers["Authorization"] = "Bearer " + token
    if metadata:
        headers["Metadata-Flavor"] = "Google"
    data = None if body is None else json.dumps(body).encode()
    refreshed = False
    while True:
        try:
            with OPENER.open(Request(url, data=data, headers=headers), timeout=min(15, _left(deadline))) as response:
                raw = response.read(4 * 1024 * 1024 + 1)
                if response.status != expected or len(raw) > 4 * 1024 * 1024: raise SmokeError("Unexpected or oversized smoke response")
                return response.headers, raw
        except HTTPError as error:
            if error.code == 401 and token and token.count(".") != 2 and not refreshed:
                headers["Authorization"] = "Bearer " + _token(deadline)
                refreshed = True
                continue
            if error.code not in (429, 503):
                raise SmokeError("Smoke endpoint returned HTTP " + str(error.code)) from None
        except (URLError, OSError, TimeoutError):
            pass
        time.sleep(min(2, _left(deadline)))

def _json(url, deadline, body=None, token=None, metadata=False, expected=200):
    _, data = _request(url, deadline, body, token, metadata, expected)
    try:
        result = json.loads(data)
        if not isinstance(result, dict):
            raise ValueError()
        return result
    except (ValueError, UnicodeDecodeError):
        raise SmokeError("Smoke endpoint returned invalid JSON") from None

def _token(deadline):
    token = _json(METADATA, deadline, metadata=True).get("access_token")
    if not isinstance(token, str) or not token: raise SmokeError("Build service account access token unavailable")
    return token

def _https(url):
    value = urlsplit(url)
    if value.scheme != "https" or not value.hostname or value.username or value.password or value.query or value.fragment: raise SmokeError("Smoke endpoint must be an HTTPS URL")
    return url.rstrip("/")

def _root(config):
    return ("https://firestore.googleapis.com/v1/projects/" + quote(config.get("project", "bot-111549"), safe="") +
            "/databases/" + quote(config["firestore_database"], safe="") + "/documents/owners/" + quote(OWNER, safe=""))

def _fields(doc):
    return doc.get("fields", {})

def _preflight(config, deadline):
    token, root = _token(deadline), _root(config)
    control = _fields(_json(root + "/conversation/control", deadline, token=token))
    try:
        received = int(control["received"]["integerValue"])
        consumed = int(control["consumed"]["integerValue"])
        user_received = int(control.get("user_received", {}).get("integerValue", "0"))
        user_routed = int(control.get("user_routed", {}).get("integerValue", "0"))
    except (ValueError, TypeError, KeyError):
        raise SmokeError("Conversation control is malformed") from None
    if received != consumed or user_received != user_routed or control.get("active", {}).get("stringValue"): raise SmokeError("Preflight refused: conversation has pending or active work")
    ids, cursor = set(), ""
    while True:
        params = [("pageSize", "300"), ("mask.fieldPaths", "status"), ("mask.fieldPaths", "id")]
        if cursor:
            params.append(("pageToken", cursor))
        page = _json(root + "/runs?" + urlencode(params), deadline, token=token)
        for doc in page.get("documents", []):
            fields = _fields(doc)
            run_id = fields.get("id", {}).get("stringValue")
            if not run_id or fields.get("status", {}).get("stringValue") not in TERMINAL: raise SmokeError("Preflight refused: an existing task is not terminal")
            ids.add(run_id)
        cursor = page.get("nextPageToken", "")
        if not cursor:
            return ids

def preflight(config, deadline):
    """Read Firestore without waking work, within the caller's deadline."""
    if signal.getitimer(signal.ITIMER_REAL)[0]:
        return _preflight(config, deadline)  # run() already owns the alarm.
    previous = signal.getsignal(signal.SIGALRM)
    def expired(*_):
        raise SmokeError("Preflight deadline exceeded")
    signal.signal(signal.SIGALRM, expired)
    signal.setitimer(signal.ITIMER_REAL, max(0.001, deadline - time.monotonic()))
    try:
        return _preflight(config, deadline)
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous)

def _post(base, operation, body, token, deadline, expected=200):
    return _json(base + "/api/" + operation, deadline, body, token, expected=expected)

def _owned(snapshot, input_id):
    result = set()
    for trace in snapshot.get("traces", []):
        for action in trace.get("actions", []):
            cmd = action.get("command") or {}
            run_id = cmd.get("run_id", "")
            if (action.get("status") == "accepted" and cmd.get("kind") == "start" and
                    input_id in cmd.get("source_input_ids", [])):
                try:
                    if str(uuid.UUID(run_id)) == run_id:
                        result.add(run_id)
                except (ValueError, TypeError, AttributeError):
                    raise SmokeError("Invalid smoke task identity") from None
    return result

def _iap_jwt(config, url, token, deadline):
    account = config["iap_service_account"]
    now = int(time.time())
    claims = {"iss": account, "sub": account, "aud": url, "iat": now, "exp": now + 600}
    endpoint = "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + quote(account, safe="") + ":signJwt"
    signed = _json(endpoint, deadline, {"payload": json.dumps(claims)}, token).get("signedJwt")
    if not isinstance(signed, str) or len(signed.split(".")) != 3: raise SmokeError("IAM did not return an IAP JWT")
    return signed

def run(config, deadline, evidence_callback=lambda _: None):
    """Run one browser task and preserve only its IDs as release evidence."""
    end = min(deadline, time.monotonic() + 600)
    work_end = end - 25  # Reserve time for best-effort targeted Stop.
    old_handler, old_timer = signal.getsignal(signal.SIGALRM), signal.getitimer(signal.ITIMER_REAL)
    if old_timer[0]: raise SmokeError("A process timer prevents a bounded smoke")
    def expired(*_):
        raise SmokeError("Smoke deadline exceeded")
    signal.signal(signal.SIGALRM, expired)
    signal.setitimer(signal.ITIMER_REAL, max(0.001, work_end - time.monotonic()))
    input_id, owned, submitted, token, base = str(uuid.uuid4()), set(), False, None, None
    try:
        before = config["baseline_ids"]
        token = _token(work_end)
        base, health = _https(config["runtime_url"]), _https(config["web_url"]) + "/api/health"
        iap = _iap_jwt(config, health, token, work_end)
        if _json(health, work_end, token=iap).get("status") != "ok": raise SmokeError("Web health failed")
        evidence_callback({"check": "web_health", "status": "passed"})
        session = _post(base, "session", {}, token, work_end)
        conversation = session.get("conversation", {})
        runs = session.get("runs", [])
        if (session.get("paused") or conversation.get("pending") or conversation.get("processing") or
                any(r.get("status") not in TERMINAL or r.get("id") not in before for r in runs)):
            raise SmokeError("Smoke requires an unpaused, idle session")
        message = ("Start exactly one task: navigate to https://example.org using the browser, capture a screenshot, "
                   "then finish with a short description. Do not ask questions, run shell commands, fill forms, "
                   "or start other tasks. Include this identifier in the task request: nori-release-smoke-" + input_id)
        evidence_callback({"check": "input_submitted", "status": "pending", "input_id": input_id})
        submitted = True
        _post(base, "turn", {"id": input_id, "message": message}, token, work_end, 202)
        while True:
            session = _post(base, "session", {}, token, work_end)
            owned |= _owned(session, input_id)
            if len(owned) > 1: raise SmokeError("Smoke created multiple tasks")
            conversation = session.get("conversation", {})
            if not conversation.get("pending") and not conversation.get("processing"):
                break
            time.sleep(min(2, _left(work_end)))
        if len(owned) != 1: raise SmokeError("Smoke did not create exactly one identifiable task")
        run_id = next(iter(owned))
        while True:
            session = _post(base, "session", {}, token, work_end)
            owned |= _owned(session, input_id)
            new = [r for r in session.get("runs", []) if r.get("id") not in before]
            if len(owned) != 1 or len(new) != 1 or new[0].get("id") != run_id: raise SmokeError("Concurrent or multiple tasks detected")
            task = new[0]
            if task.get("status") in TERMINAL:
                break
            if task.get("question"): raise SmokeError("Smoke task unexpectedly requested input")
            time.sleep(min(3, _left(work_end)))
        if task.get("status") != "completed": raise SmokeError("Smoke task did not complete")
        steps = _post(base, "trace", {"run_id": run_id}, token, work_end).get("steps", [])
        navigate = [s for s in steps if s.get("name") == "browser_navigate" and s.get("status") == "completed" and "example.org" in s.get("arguments", "")]
        screenshots = [s for s in steps if s.get("name") == "browser_screenshot" and s.get("status") == "completed"]
        artifacts = [a for a in task.get("artifacts", []) if a.get("kind") == "screenshot" and a.get("id")]
        if not navigate or not screenshots or not artifacts or any(s.get("status") == "failed" for s in steps): raise SmokeError("Browser trace or screenshot missing or failed")
        headers, image = _request(base + "/api/artifact", work_end, {"run_id": run_id, "artifact_id": artifacts[-1]["id"]}, token)
        if "image/png" not in headers.get("Content-Type", "").lower() or len(image) <= 100 or not image.startswith(b"\x89PNG\r\n\x1a\n"): raise SmokeError("Screenshot response is not a valid PNG")
        result = subprocess.run(["temporal", "--output=json", "--command-timeout=15s", "workflow", "describe", "--workflow-id=nori-task-" + run_id], capture_output=True, text=True, timeout=min(20, _left(work_end)))
        try:
            info = json.loads(result.stdout)["workflowExecutionInfo"]["versioningInfo"]
            version = info["deploymentVersion"]
            if result.returncode or info.get("versioningOverride") or version.get("deploymentName") != "nori-task" or version.get("buildId") != config["expected_worker_build"]:
                raise ValueError()
        except (ValueError, KeyError, TypeError):
            raise SmokeError("Smoke worker build could not be verified") from None
        evidence_callback({"check": "browser_task", "status": "passed", "task_id": run_id, "worker_build": config["expected_worker_build"], "screenshot_bytes": len(image)})
        return {"task_id": run_id, "worker_build": config["expected_worker_build"]}
    except Exception:
        signal.setitimer(signal.ITIMER_REAL, max(0.001, end - time.monotonic()))
        if submitted and token:
            for run_id in owned:
                try:
                    _post(base, "command", {"id": str(uuid.uuid4()), "run_id": run_id, "kind": "stop"}, token, end, 202)
                except Exception:
                    pass
            if time.monotonic() < end:
                signal.setitimer(signal.ITIMER_REAL, end - time.monotonic())
                evidence_callback({"check": "cleanup", "status": "unconfirmed", "input_id": input_id, "known_tasks": len(owned)})
        raise
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, old_handler)
