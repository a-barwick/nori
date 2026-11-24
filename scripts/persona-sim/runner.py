#!/usr/bin/env python3
"""Run real-life 30-day Nori persona simulations against an isolated local stack.

Features:
- Spins up an isolated ephemeral Postgres database (never touching production or the default
  `nori` database), OR seeds a dedicated test user in the local workbench
  (`--workbench-seed`).
- Runs `cmd/nori-sim` (real app.Service + worker.Pool + Postgres 18 + virtual clock) per persona
  so parallel personas never collide on virtual time.
- Connects a stateful per-persona MCP sandbox (`mcp_sandbox.py`) for realistic Email, Calendar,
  Docs, and Issues without external network side effects.
- Pairs each persona's fixed 30-day `month.json` narrative spine with an Adaptive In-Character
  Persona Responder LLM to handle unexpected clarifying questions or approval cards.
- Records full run bundles under `.sim-runs/<timestamp>-<tag>/<slug>/` and invokes `grader.py`.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import http.client
import itertools
import json
import mimetypes
import os
import socket
import subprocess
import sys
import tempfile
import threading
import time
from contextlib import ExitStack, closing
from datetime import datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import quote, urlparse
from uuid import uuid4
from zoneinfo import ZoneInfo

from grader import grade_run
from llm_client import AdaptivePersonaResponder, LLMClient, load_dotenv, resolve_dotenv_path
from mcp_sandbox import READ_ONLY_TOOL_NAMES, SandboxMCPServer

ROOT = Path(__file__).resolve().parents[2]
CORE = ROOT / "core"
PERSONAS_DIR = ROOT / "sim" / "personas"
RUNS_DIR = ROOT / ".sim-runs"


def pick_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


def run_cmd(*args: str, cwd: Path, env: dict[str, str] | None = None, timeout: int = 300) -> str:
    res = subprocess.run(args, cwd=cwd, env=env, timeout=timeout, text=True, capture_output=True)
    if res.returncode != 0:
        out = (res.stderr or res.stdout or "").strip()
        raise RuntimeError(f"{' '.join(args)} exited {res.returncode}: {out[:800]}")
    return res.stdout.strip()


def http_json(
    base_url: str,
    method: str,
    path: str,
    data: Any = None,
    raw_bytes: bytes | None = None,
    content_type: str | None = None,
    timeout: int = 120,
) -> tuple[int, Any]:
    parsed = urlparse(base_url)
    host = parsed.hostname or "127.0.0.1"
    port = parsed.port or (443 if parsed.scheme == "https" else 80)
    headers: dict[str, str] = {}
    body: bytes | None = None
    if raw_bytes is not None:
        body = raw_bytes
        headers["Content-Type"] = content_type or "application/octet-stream"
    elif data is not None:
        body = json.dumps(data, ensure_ascii=False).encode("utf-8")
        headers["Content-Type"] = "application/json"

    with closing(http.client.HTTPConnection(host, port, timeout=timeout)) as conn:
        conn.request(method, path, body=body, headers=headers)
        resp = conn.getresponse()
        payload = resp.read()
        status = resp.status
        ctype = resp.getheader("Content-Type", "")
    if ctype.startswith("application/json") and payload:
        try:
            return status, json.loads(payload)
        except json.JSONDecodeError:
            return status, payload.decode("utf-8", errors="replace")
    return status, payload.decode("utf-8", errors="replace")


def build_stub_model_server() -> ThreadingHTTPServer:
    """Minimal OpenAI Chat + Responses loopback stub for free `--stub-models` dry runs."""
    call_seq = itertools.count(1)

    class SimModelStub(BaseHTTPRequestHandler):
        def log_message(self, format: str, *args: Any) -> None:
            pass

        def _send(self, body: bytes, ctype: str = "application/json", status: int = 200) -> None:
            self.send_response(status)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self) -> None:
            length = int(self.headers.get("Content-Length", "0"))
            req = json.loads(self.rfile.read(length) if length else b"{}")
            if self.path == "/v1/chat/completions":
                self._handle_chat(req)
            elif self.path == "/v1/responses":
                self._handle_responses(req)
            else:
                self._send(b'{"error":"not found"}', status=404)

        def _handle_chat(self, req: dict[str, Any]) -> None:
            msgs = req.get("messages") or []
            last_user = ""
            for m in reversed(msgs):
                if m.get("role") == "user":
                    last_user = str(m.get("content", ""))
                    break
            text = f"Got it! Noted: {last_user[:140]}"
            schema = (req.get("response_format") or {}).get("json_schema") or {}
            if schema.get("name") == "nori_response":
                actions: list[dict[str, Any]] = []
                low = last_user.lower()
                if "remember" in low and "dont save" not in low and "don't save" not in low and "lol jk" not in low:
                    actions.append({
                        "name": "remember",
                        "arguments": {
                            "text": f"The user noted: {last_user[:160]}",
                            "about": "the user",
                            "section": "Context",
                            "replaces": None,
                        },
                    })
                content = json.dumps({"actions": actions, "text": text})
            else:
                content = text

            usage = {"prompt_tokens": 120, "completion_tokens": 30, "total_tokens": 150}
            meta = {"model": req.get("model", "stub"), "service_tier": "default", "usage": usage}
            if req.get("stream"):
                chunks = [
                    {"choices": [{"delta": {"content": content}, "finish_reason": None}]},
                    {"choices": [{"delta": {}, "finish_reason": "stop"}]},
                    {"choices": [], **meta},
                ]
                sse = "".join(f"data: {json.dumps(c)}\n\n" for c in chunks) + "data: [DONE]\n\n"
                self._send(sse.encode("utf-8"), "text/event-stream")
            else:
                self._send(json.dumps({
                    **meta,
                    "choices": [{"message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
                }).encode("utf-8"))

        def _handle_responses(self, req: dict[str, Any]) -> None:
            tools = req.get("tools") or []
            tool_names = [t.get("name", "") for t in tools]
            cid = f"call_stub_{next(call_seq)}"
            if "flush_and_compact" in tool_names:
                args = {
                    "core_upserts": [],
                    "daily_log_entries": [],
                    "summary_items": [],
                    "gist": "Stub compaction summary covering recent conversation turns.",
                }
                output = [{"type": "function_call", "call_id": cid, "name": "flush_and_compact", "arguments": json.dumps(args)}]
            elif "dream_consolidate" in tool_names:
                args = {"core_upserts": [], "core_deletes": [], "daily_log_entries": []}
                output = [{"type": "function_call", "call_id": cid, "name": "dream_consolidate", "arguments": json.dumps(args)}]
            elif "complete" in tool_names:
                args = {"answer": "Stub task completed.", "title": "Stub Result"}
                output = [{"type": "function_call", "call_id": cid, "name": "complete", "arguments": json.dumps(args)}]
            else:
                output = [{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Stub response."}]}]
            body = {
                "id": f"resp_stub_{next(call_seq)}",
                "status": "completed",
                "model": req.get("model", "stub"),
                "output": output,
                "usage": {"input_tokens": 100, "output_tokens": 25, "total_tokens": 125},
            }
            self._send(json.dumps(body).encode("utf-8"))

    return ThreadingHTTPServer(("127.0.0.1", 0), SimModelStub)


def compute_event_utc(sim_start_date: str, day: int, local_time: str, tz_name: str) -> datetime:
    base = datetime.strptime(sim_start_date, "%Y-%m-%d").date()
    ev_date = base + timedelta(days=day - 1)
    hh, mm = map(int, local_time.split(":"))
    tz = ZoneInfo(tz_name)
    dt_local = datetime(ev_date.year, ev_date.month, ev_date.day, hh, mm, tzinfo=tz)
    return dt_local.astimezone(ZoneInfo("UTC"))


def build_binaries(bin_dir: Path) -> tuple[Path, Path]:
    print("Building cmd/nori-sim and cmd/nori-user...", flush=True)
    run_cmd("go", "build", "-o", str(bin_dir) + os.sep, "./cmd/nori-sim", "./cmd/nori-user", cwd=CORE, timeout=600)
    return bin_dir / "nori-sim", bin_dir / "nori-user"


def ensure_ephemeral_postgres(
    stack: ExitStack,
    dotenv: dict[str, str],
    keep_db: bool,
    nori_sim_bin: Path | None = None,
) -> tuple[str, str | None]:
    """Return (postgres_url_for_ephemeral_db, db_name_to_drop_or_none).

    Prefer creating a fresh ephemeral database on an already-running local loopback Postgres
    (e.g. `make deps` on 127.0.0.1:15432); otherwise start an isolated Docker Compose Postgres.
    Never touches production or the default `nori` database.
    """
    raw_url = os.environ.get("NORI_POSTGRES_URL") or dotenv.get("NORI_POSTGRES_URL") or "postgres://nori@127.0.0.1:15432/nori?sslmode=disable"
    parsed = urlparse(raw_url)
    host = parsed.hostname or "127.0.0.1"
    port = parsed.port or 15432
    user = parsed.username or "nori"
    password = parsed.password or ""
    db_name = f"nori_sim_{uuid4().hex[:10]}"

    # Check if local loopback Postgres is reachable via TCP
    local_ok = False
    if host in ("127.0.0.1", "localhost"):
        try:
            with socket.create_connection((host, port), timeout=1.5):
                local_ok = True
        except OSError:
            local_ok = False

    if local_ok:
        auth = f"{user}:{password}@" if password else f"{user}@"
        target_url = f"postgres://{auth}{host}:{port}/{db_name}?sslmode=disable"
        admin_url = f"postgres://{auth}{host}:{port}/postgres?sslmode=disable"
        created = False

        # Prefer nori-sim's built-in pgx driver (zero psql/docker CLI dependency)
        if nori_sim_bin is not None:
            try:
                run_cmd(str(nori_sim_bin), "--create-db", target_url, cwd=ROOT, timeout=30)
                created = True
                if not keep_db:
                    stack.callback(
                        lambda u=target_url, b=str(nori_sim_bin): subprocess.run(
                            [b, "--drop-db", u], cwd=ROOT, capture_output=True, text=True, timeout=30
                        )
                    )
            except Exception:
                created = False

        if not created:
            try:
                run_cmd("psql", admin_url, "-v", "ON_ERROR_STOP=1", "-c", f'CREATE DATABASE "{db_name}";', cwd=ROOT, timeout=30)
                created = True
                if not keep_db:
                    stack.callback(
                        lambda: subprocess.run(
                            ["psql", admin_url, "-c", f'DROP DATABASE IF EXISTS "{db_name}" WITH (FORCE);'],
                            cwd=ROOT, capture_output=True, text=True, timeout=30,
                        )
                    )
            except Exception:
                created = False

        if created:
            return target_url, db_name

    # Fallback: launch isolated ephemeral Docker Compose Postgres container
    project = f"nori-sim-{uuid4().hex[:10]}"
    compose = (
        "docker", "compose", "--env-file", os.devnull, "--project-name", project,
        "-f", str(CORE / "infrastructure/local/compose.yaml"),
        "-f", str(ROOT / "scripts/local-stacks/headless-demo/compose.override.yaml"),
    )
    print(f"Starting isolated ephemeral Postgres container ({project})...", flush=True)
    if not keep_db:
        stack.callback(
            lambda: subprocess.run([*compose, "down", "--volumes", "--timeout", "5"], cwd=ROOT, capture_output=True, text=True)
        )
    run_cmd(*compose, "up", "-d", "--wait", "--wait-timeout", "75", cwd=ROOT, timeout=240)
    host_port = run_cmd(*compose, "port", "postgres", "5432", cwd=ROOT, timeout=30).splitlines()[-1].strip()
    run_cmd(
        *compose, "exec", "-T", "postgres", "psql", "-U", "nori", "-d", "postgres",
        "-v", "ON_ERROR_STOP=1", "-c", f'CREATE DATABASE "{db_name}";',
        cwd=ROOT, timeout=30,
    )
    return f"postgres://nori@{host_port}/{db_name}?sslmode=disable", db_name


def connect_sandbox_mcp(base_url: str, user_id: str, mcp_url: str, server_name: str, initial_perms: list[dict[str, Any]]) -> dict[str, str]:
    """Register persona sandbox MCP server, discover tools, allow read tools, and apply initial permissions."""
    prefix = f"/users/{user_id}/v1/mcp"
    st, created = http_json(base_url, "POST", prefix, {
        "request_id": str(uuid4()),
        "expected_revision": 0,
        "value": {
            "name": server_name,
            "transport": "http",
            "endpoint": mcp_url,
            "auth": "none",
            "enabled": True,
            "exposure": {"default": "direct"},
        },
    })
    if st != 201:
        raise RuntimeError(f"POST {prefix} failed ({st}): {created}")
    server = created["resource"]

    st, connected = http_json(base_url, "POST", f"{prefix}/{server['id']}/actions", {
        "request_id": str(uuid4()),
        "expected_revision": server["revision"],
        "value": {"action": "connect"},
    })
    if st != 200:
        raise RuntimeError(f"MCP connect failed ({st}): {connected}")

    st, tools_resp = http_json(base_url, "GET", f"{prefix}/{server['id']}/tools")
    if st != 200:
        raise RuntimeError(f"MCP tools list failed ({st}): {tools_resp}")
    tool_id_by_name: dict[str, str] = {}
    read_tool_ids: list[str] = []
    for t in tools_resp.get("items") or []:
        tname = t.get("name", "")
        tid = (t.get("definition") or {}).get("id", "")
        if tname and tid:
            tool_id_by_name[tname] = tid
            if tname in READ_ONLY_TOOL_NAMES:
                read_tool_ids.append(tid)

    # Allow read-only sandbox tools by default so tasks can freely search email/calendar/docs/issues,
    # while leaving write/send tools governed by approval cards or explicit permission rules.
    if read_tool_ids:
        http_json(base_url, "POST", f"/users/{user_id}/v1/permissions", {
            "request_id": str(uuid4()),
            "expected_revision": 0,
            "value": {
                "effect": "allow",
                "tool_ids": read_tool_ids,
                "connection_ids": [],
                "resource_ids": [],
                "action_classes": ["read", "unknown"],
                "title": f"Allow {server_name} read tools",
            },
        })

    for perm in initial_perms:
        tids = [tool_id_by_name[n] for n in (perm.get("tool_names") or []) if n in tool_id_by_name]
        val: dict[str, Any] = {
            "effect": perm["effect"],
            "tool_ids": tids,
            "connection_ids": [],
            "resource_ids": [],
            "action_classes": perm.get("action_classes") or ["write", "send", "unknown"],
            "title": perm["title"],
        }
        if perm.get("recipients"):
            val["recipients"] = perm["recipients"]
        if perm.get("max_operations"):
            val["max_operations"] = perm["max_operations"]
        http_json(base_url, "POST", f"/users/{user_id}/v1/permissions", {
            "request_id": str(uuid4()),
            "expected_revision": 0,
            "value": val,
        })

    return tool_id_by_name


def apply_preferences(base_url: str, user_id: str, updates: dict[str, Any]) -> tuple[int, Any]:
    st, cur = http_json(base_url, "GET", f"/users/{user_id}/v1/preferences")
    if st != 200:
        return st, cur
    rev = int(cur.get("revision", 0))
    tz = updates.get("timezone") or cur.get("timezone") or "UTC"
    locale = updates.get("locale") if "locale" in updates else cur.get("locale", "")
    wh = cur.get("working_hours") or []
    if isinstance(updates.get("working_hours"), dict):
        day_map = {"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
        new_wh = []
        for k, v in updates["working_hours"].items():
            dnum = day_map.get(k.lower()[:3])
            if dnum is not None and v.get("enabled"):
                new_wh.append({"day": dnum, "start": v.get("start", "09:00"), "end": v.get("end", "17:00")})
        wh = sorted(new_wh, key=lambda x: x["day"])
    budgets = dict(cur.get("budgets") or {"task_usd": 5.0})
    if updates.get("budget_per_task_usd") is not None:
        budgets["task_usd"] = float(updates["budget_per_task_usd"])
    if "monthly_limit_usd" in updates:
        if updates["monthly_limit_usd"] is None:
            budgets.pop("monthly_usd", None)
        else:
            budgets["monthly_usd"] = float(updates["monthly_limit_usd"])

    val: dict[str, Any] = {
        "timezone": tz,
        "working_hours": wh,
        "default_destination_ids": cur.get("default_destination_ids") or [],
        "account_defaults": cur.get("account_defaults") or {},
        "budgets": budgets,
    }
    if locale:
        val["locale"] = locale
    return http_json(base_url, "PUT", f"/users/{user_id}/v1/preferences", {
        "request_id": str(uuid4()),
        "expected_revision": rev,
        "value": val,
    })


def upload_attachment(base_url: str, user_id: str, file_path: Path) -> str:
    uid = str(uuid4())
    mime, _ = mimetypes.guess_type(file_path.name)
    if file_path.suffix.lower() == ".md":
        mime = "text/markdown"
    elif file_path.suffix.lower() == ".ics":
        mime = "text/calendar"
    elif file_path.suffix.lower() == ".csv":
        mime = "text/csv"
    mime = mime or "application/octet-stream"
    st, res = http_json(
        base_url,
        "POST",
        f"/users/{user_id}/v1/uploads?id={uid}&filename={quote(file_path.name)}",
        raw_bytes=file_path.read_bytes(),
        content_type=mime,
    )
    if st != 200:
        raise RuntimeError(f"Upload {file_path.name} failed ({st}): {res}")
    return str(res.get("id") or uid)


def latest_assistant_message(base_url: str, user_id: str) -> tuple[str, str]:
    st, snap = http_json(base_url, "GET", f"/users/{user_id}/v1/snapshot")
    if st != 200 or not isinstance(snap, dict):
        return "", ""
    msgs = snap.get("messages") or []
    for m in reversed(msgs):
        if m.get("role") == "assistant" and m.get("text"):
            return str(m.get("id", "")), str(m.get("text", ""))
    return "", ""


def run_single_persona(
    slug: str,
    run_root: Path,
    nori_sim_bin: Path | None,
    nori_user_bin: Path | None,
    pg_url: str,
    mcp_server: SandboxMCPServer,
    dotenv: dict[str, str],
    stub_model_url: str | None,
    max_events: int | None,
    workbench_url: str | None,
) -> dict[str, Any]:
    started = time.monotonic()
    pdir = PERSONAS_DIR / slug
    profile = json.loads((pdir / "profile.json").read_text(encoding="utf-8"))
    month = json.loads((pdir / "month.json").read_text(encoding="utf-8"))

    out_dir = run_root / slug
    snaps_dir = out_dir / "snapshots"
    snaps_dir.mkdir(parents=True, exist_ok=True)

    mcp_url = mcp_server.register_persona(slug, profile.get("mcp_world") or {})
    world = mcp_server.get_world(slug)
    assert world is not None

    sim_start_utc = compute_event_utc(
        profile["sim_start_date"], 1, "07:00", profile["timezone"]
    )
    current_tz = profile["timezone"]
    http_5xx_count = 0
    trace_lines: list[dict[str, Any]] = []
    probe_records: list[dict[str, Any]] = []

    responder_client = None if stub_model_url else LLMClient.from_env("responder", dotenv)
    responder = AdaptivePersonaResponder(
        client=responder_client,
        persona_brief=profile["persona_responder_brief"],
        persona_name=profile["name"],
    )

    with ExitStack() as stack:
        work_tmp = Path(stack.enter_context(tempfile.TemporaryDirectory(prefix=f"nori-sim-{slug}-")))
        artifacts_dir = work_tmp / "artifacts"
        artifacts_dir.mkdir(parents=True, exist_ok=True)
        cred_key_file = work_tmp / "cred.key"

        if workbench_url:
            base_url = workbench_url.rstrip("/")
            has_sim_api = False
            user_email = profile["email"]
            # Provision user in target DB if nori-user available
            user_id = str(uuid4())
            if nori_user_bin:
                env = dict(os.environ)
                env.update(dotenv)
                env["NORI_CLOUD"] = "local"
                env["NORI_STORE_BACKEND"] = "postgres"
                env["NORI_POSTGRES_URL"] = pg_url
                env["NORI_LOCAL_ARTIFACT_DIR"] = str(artifacts_dir)
                out_u = run_cmd(
                    str(nori_user_bin),
                    "--id", user_id,
                    "--email", user_email,
                    "--name", profile["name"],
                    "--timezone", profile["timezone"],
                    cwd=work_tmp, env=env, timeout=60,
                )
                for line in out_u.splitlines():
                    if line.strip().startswith("{"):
                        try:
                            user_id = json.loads(line)["id"]
                        except Exception:
                            pass
        else:
            assert nori_sim_bin is not None and nori_user_bin is not None
            port = pick_free_port()
            base_url = f"http://127.0.0.1:{port}"
            has_sim_api = True
            user_id = str(uuid4())

            env = dict(os.environ)
            for k, v in dotenv.items():
                env.setdefault(k, v)
            env.update({
                "NORI_ENV_FILE": os.devnull,
                "NORI_TASK_ENV_FILE": os.devnull,
                "NORI_CLOUD": "local",
                "NORI_STORE_BACKEND": "postgres",
                "NORI_POSTGRES_URL": pg_url,
                "NORI_LOCAL_ARTIFACT_DIR": str(artifacts_dir),
                "NORI_CREDENTIAL_STORE": "database",
                "NORI_CREDENTIAL_KEY_FILE": str(cred_key_file),
                "NORI_LOCAL_EMAIL": profile["email"],
                "PORT": str(port),
                "NORI_BACKGROUND_TASKS_ENABLED": "true",
                "NORI_JEV_ENABLED": "false" if stub_model_url else env.get("NORI_JEV_ENABLED", "false"),
            })
            if stub_model_url:
                env["NORI_MODEL_TEST_URL"] = stub_model_url
                env.setdefault("OPENAI_API_KEY", "stub-key")
                env.setdefault("OPENROUTER_API_KEY", "stub-key")

            run_cmd(
                str(nori_user_bin),
                "--id", user_id,
                "--email", profile["email"],
                "--name", profile["name"],
                "--timezone", profile["timezone"],
                cwd=work_tmp, env=env, timeout=60,
            )

            log_file = (out_dir / "nori-sim.log").open("w", encoding="utf-8")
            stack.callback(log_file.close)
            proc = subprocess.Popen(
                [str(nori_sim_bin), "--start-time", sim_start_utc.strftime("%Y-%m-%dT%H:%M:%SZ")],
                cwd=work_tmp, env=env, stdout=log_file, stderr=subprocess.STDOUT, start_new_session=True,
            )

            def _stop_proc(p: subprocess.Popen[Any] = proc) -> None:
                if p.poll() is None:
                    p.terminate()
                    try:
                        p.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        p.kill()
                        p.wait()

            stack.callback(_stop_proc)

            # Wait for /readyz
            deadline = time.monotonic() + 45
            while True:
                if proc.poll() is not None:
                    raise RuntimeError(f"nori-sim exited early ({proc.returncode}); check {out_dir / 'nori-sim.log'}")
                try:
                    st, _ = http_json(base_url, "GET", "/readyz", timeout=3)
                    if st == 200:
                        break
                except Exception:
                    pass
                if time.monotonic() > deadline:
                    raise RuntimeError(f"nori-sim did not become ready on {base_url}")
                time.sleep(0.2)

        def _set_clock(dt_utc: datetime) -> None:
            if not has_sim_api:
                return
            st, res = http_json(base_url, "POST", "/_sim/clock/set", {"time": dt_utc.strftime("%Y-%m-%dT%H:%M:%SZ")})
            if st >= 500:
                nonlocal http_5xx_count
                http_5xx_count += 1
            elif st == 409:
                # Ignore tiny clock tie when timezone shifts backwards within same day
                pass

        def _drain(timeout_s: int = 180) -> dict[str, Any]:
            if not has_sim_api:
                time.sleep(3)
                return {"quiescent": True}
            st, res = http_json(base_url, "POST", "/_sim/drain", {"user_id": user_id, "timeout_seconds": timeout_s}, timeout=timeout_s + 15)
            if st >= 500:
                nonlocal http_5xx_count
                http_5xx_count += 1
            return res if isinstance(res, dict) else {"raw": res}

        def _get_state() -> dict[str, Any]:
            if not has_sim_api:
                return {}
            st, res = http_json(base_url, "GET", f"/_sim/users/{user_id}/state", timeout=30)
            if st >= 500:
                nonlocal http_5xx_count
                http_5xx_count += 1
                return {}
            return res if isinstance(res, dict) else {}

        # Initial setup: Preferences + MCP Sandbox + Initial Permissions
        apply_preferences(base_url, user_id, profile.get("preferences") or {})
        tool_ids = connect_sandbox_mcp(
            base_url, user_id, mcp_url,
            (profile.get("mcp_world") or {}).get("server_name") or f"{profile['name']} Workspace",
            profile.get("initial_permissions") or [],
        )
        # Ensure intro snapshot
        http_json(base_url, "GET", f"/users/{user_id}/v1/snapshot")
        _drain(60)

        events = month["events"]
        if max_events is not None:
            events = events[:max_events]

        last_msg_id = ""
        for idx, ev in enumerate(events):
            ev_id = ev["id"]
            day = int(ev["day"])
            local_time = ev["local_time"]
            if ev.get("timezone"):
                current_tz = ev["timezone"]
            ev_utc = compute_event_utc(profile["sim_start_date"], day, local_time, current_tz)
            prev_was_burst = idx > 0 and events[idx - 1].get("kind") == "chat" and events[idx - 1].get("wait_for_reply") is False
            if not prev_was_burst:
                _set_clock(ev_utc)

            kind = ev["kind"]
            print(f"[{slug}] ({idx + 1}/{len(events)}) Day {day:02d} {local_time} {kind} {ev_id}", flush=True)
            writes_before = len(world.write_log)
            rec: dict[str, Any] = {
                "index": idx,
                "id": ev_id,
                "day": day,
                "local_time": local_time,
                "sim_utc": ev_utc.strftime("%Y-%m-%dT%H:%M:%SZ"),
                "kind": kind,
                "tags": ev.get("tags") or [],
            }
            adaptive_followups: list[dict[str, str]] = []
            nori_reply_text = ""

            if kind == "chat":
                att_ids = []
                for rel in ev.get("attachments") or []:
                    att_ids.append(upload_attachment(base_url, user_id, pdir / rel))
                turn_id = str(uuid4())
                st, turn_res = http_json(base_url, "POST", f"/users/{user_id}/api/turn", {
                    "id": turn_id,
                    "message": ev["text"],
                    "attachments": att_ids,
                })
                if st >= 500:
                    http_5xx_count += 1
                rec["http_status"] = st
                rec["user_text"] = ev["text"]
                rec["attachments"] = att_ids

                wait_reply = ev.get("wait_for_reply", True)
                if wait_reply:
                    drain_res = _drain(180)
                    rec["drain"] = drain_res
                    msg_id, nori_reply_text = latest_assistant_message(base_url, user_id)
                    if msg_id != last_msg_id:
                        last_msg_id = msg_id
                    rec["nori_reply"] = nori_reply_text

                    # Adaptive loop (max 2 turns): resolve pending interactions or genuine clarifying questions
                    if not ev.get("probe") or not ev["probe"].get("expect_no_external_write"):
                        for _step in range(2):
                            st_i, inter_resp = http_json(base_url, "GET", f"/users/{user_id}/v1/interactions?status=pending")
                            pending_list = (inter_resp.get("items") or []) if st_i == 200 and isinstance(inter_resp, dict) else []
                            if pending_list and not _next_event_handles_interaction(events, idx):
                                for item in pending_list:
                                    decision = responder.decide_interaction(item, day, local_time)
                                    payload: dict[str, Any] = {"kind": decision["kind"]}
                                    if decision.get("text"):
                                        payload["text"] = decision["text"]
                                    if item.get("action_digest") and decision["kind"] in ("approve", "decline"):
                                        payload["action_digest"] = item["action_digest"]
                                    http_json(base_url, "POST", f"/users/{user_id}/v1/interactions/{item['id']}/responses", {
                                        "request_id": str(uuid4()),
                                        "expected_revision": int(item.get("revision", 0)),
                                        "value": payload,
                                    })
                                    adaptive_followups.append({"type": "interaction", "prompt": item.get("prompt", ""), "response": json.dumps(decision)})
                                _drain(180)
                                _, nori_reply_text = latest_assistant_message(base_url, user_id)
                                continue

                            followup = responder.decide_chat_followup(ev["text"], nori_reply_text, day, local_time)
                            if followup:
                                http_json(base_url, "POST", f"/users/{user_id}/api/turn", {
                                    "id": str(uuid4()),
                                    "message": followup,
                                    "attachments": [],
                                })
                                _drain(180)
                                _, nori_reply_text = latest_assistant_message(base_url, user_id)
                                adaptive_followups.append({"type": "chat", "user": followup, "nori": nori_reply_text})
                            else:
                                break
                else:
                    # Rapid-burst mode: tiny wall pause so message persists before next burst turn
                    time.sleep(0.05)

            elif kind == "settle":
                drain_res = _drain(240)
                rec["drain"] = drain_res
                snap = _get_state()
                label = ev.get("snapshot_label") or f"d{day:02d}_{ev_id}"
                (snaps_dir / f"{label}.json").write_text(json.dumps(snap, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
                rec["snapshot_label"] = label
                rec["invariants_ok"] = snap.get("invariants_ok", True)
                rec["invariant_violations"] = snap.get("invariant_violations") or []

            elif kind == "memory_ui":
                rec["result"] = _exec_memory_ui(base_url, user_id, ev.get("action", ""), ev.get("params") or {})
                _drain(90)

            elif kind == "preferences_ui":
                params = ev.get("params") or {}
                if params.get("timezone"):
                    current_tz = params["timezone"]
                st_p, res_p = apply_preferences(base_url, user_id, params)
                if st_p >= 500:
                    http_5xx_count += 1
                rec["http_status"] = st_p
                rec["result"] = res_p

            elif kind == "permission_ui":
                rec["result"] = _exec_permission_ui(base_url, user_id, tool_ids, ev.get("action", ""), ev.get("params") or {})

            elif kind == "automation_ui":
                rec["result"] = _exec_automation_ui(base_url, user_id, current_tz, ev.get("action", ""), ev.get("params") or {})
                _drain(90)

            elif kind == "goal_ui":
                rec["result"] = _exec_goal_ui(base_url, user_id, ev.get("action", ""), ev.get("params") or {})

            elif kind == "library_ui":
                rec["result"] = _exec_library_ui(base_url, user_id, ev.get("action", ""), ev.get("params") or {})
                if ev.get("action") == "revise":
                    _drain(180)

            elif kind == "interaction_reply":
                rec["result"] = _exec_interaction_reply(base_url, user_id, ev.get("action", ""), ev.get("params") or {})
                _drain(180)
                _, nori_reply_text = latest_assistant_message(base_url, user_id)
                rec["nori_reply"] = nori_reply_text

            elif kind == "mcp_world_event":
                rec["result"] = world.apply_world_event(ev.get("action", ""), ev.get("params") or {})

            writes_delta = len(world.write_log) - writes_before
            rec["mcp_writes_during_event"] = writes_delta
            if adaptive_followups:
                rec["adaptive_followups"] = adaptive_followups

            # If this event carries a Probe, capture full evidence bundle right now!
            if ev.get("probe"):
                probe = ev["probe"]
                snap_now = _get_state()
                fts_res = None
                if probe.get("expect_fts_query"):
                    fq = probe["expect_fts_query"]
                    q = quote(fq["query"])
                    layer = quote(fq.get("layer", ""))
                    st_f, fts_res = http_json(base_url, "GET", f"/users/{user_id}/v1/memory/search?q={q}&layer={layer}&limit=20")
                    if st_f >= 500:
                        http_5xx_count += 1
                probe_records.append({
                    "event_id": ev_id,
                    "day": day,
                    "local_time": local_time,
                    "probe": probe,
                    "user_text": ev.get("text", ""),
                    "nori_reply": nori_reply_text,
                    "adaptive_followups": adaptive_followups,
                    "mcp_writes_during_event": writes_delta,
                    "fts_result": fts_res,
                    "core_memories": snap_now.get("core_memories") or [],
                    "daily_logs": snap_now.get("daily_logs") or [],
                    "working_summary": snap_now.get("working_summary") or {},
                })

            trace_lines.append(rec)

        # Final idle settle + end-of-month snapshot (+20m idle so any remaining flush/dream completes)
        if has_sim_api:
            http_json(base_url, "POST", "/_sim/clock/advance", {"seconds": 1200})
            _drain(240)
            final_snap = _get_state()
            (snaps_dir / "z_final_eom.json").write_text(json.dumps(final_snap, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # Persist run artifacts
    with (out_dir / "trace.jsonl").open("w", encoding="utf-8") as tf:
        for line in trace_lines:
            tf.write(json.dumps(line, ensure_ascii=False) + "\n")
    (out_dir / "probes.json").write_text(json.dumps(probe_records, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    (out_dir / "mcp_audit.json").write_text(json.dumps(world.audit(), indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    summary = {
        "slug": slug,
        "persona_name": profile["name"],
        "user_id": user_id,
        "events_executed": len(trace_lines),
        "probes_captured": len(probe_records),
        "http_5xx_count": http_5xx_count,
        "elapsed_seconds": time.monotonic() - started,
        "stub_models": bool(stub_model_url),
        "end_of_month_audit": month.get("end_of_month_audit") or {},
    }
    (out_dir / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(
        f"[{slug}] done: {len(trace_lines)} events, {len(probe_records)} probes, "
        f"{http_5xx_count} 5xx, {summary['elapsed_seconds']:.1f}s",
        flush=True,
    )
    return summary


def _next_event_handles_interaction(events: list[dict[str, Any]], idx: int) -> bool:
    return idx + 1 < len(events) and events[idx + 1].get("kind") == "interaction_reply"


def _exec_memory_ui(base_url: str, user_id: str, action: str, params: dict[str, Any]) -> Any:
    if action == "create_core":
        val: dict[str, Any] = {
            "text": params["text"],
            "about": params.get("about", "the user"),
            "section": params.get("section", "Context"),
        }
        if "pinned" in params:
            val["pinned"] = bool(params["pinned"])
        _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/memory", {
            "request_id": str(uuid4()), "expected_revision": 0, "value": val,
        })
        return res

    if action == "consolidate":
        _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/memory/actions", {
            "request_id": str(uuid4()), "expected_revision": 0, "value": {"action": "consolidate"},
        })
        return res

    if action == "search":
        q = quote(str(params.get("query", "")))
        layer = quote(str(params.get("layer", "")))
        _, res = http_json(base_url, "GET", f"/users/{user_id}/v1/memory/search?q={q}&layer={layer}&limit=20")
        return res

    if action in ("pin_matching", "unpin_matching", "demote_matching", "forget_matching", "edit_matching"):
        st, mem_list = http_json(base_url, "GET", f"/users/{user_id}/v1/memory")
        if st != 200 or not isinstance(mem_list, dict):
            return {"error": f"list memory failed: {st}"}
        q = _match_query(params)
        target = next((m for m in (mem_list.get("items") or []) if q in str(m.get("text", "")).lower() or q in str(m.get("about", "")).lower()), None)
        if target is None:
            return {"matched": False, "query": q}
        if action == "edit_matching":
            val = {
                "text": params.get("new_text") or params.get("text") or target["text"],
                "about": params.get("about", target.get("about", "the user")),
                "section": params.get("section", target.get("section", "")),
            }
            _, res = http_json(base_url, "PUT", f"/users/{user_id}/v1/memory/{target['id']}", {
                "request_id": str(uuid4()), "expected_revision": int(target["revision"]), "value": val,
            })
            return {"matched": True, "id": target["id"], "response": res}
        act_name = action.replace("_matching", "")
        _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/memory/{target['id']}/actions", {
            "request_id": str(uuid4()), "expected_revision": int(target["revision"]), "value": {"action": act_name},
        })
        return {"matched": True, "id": target["id"], "action": act_name, "response": res}

    if action == "promote_log_matching":
        st, logs_resp = http_json(base_url, "GET", f"/users/{user_id}/v1/memory/logs?limit=30")
        if st != 200 or not isinstance(logs_resp, dict):
            return {"error": f"list logs failed: {st}"}
        q = str(params.get("query", "")).lower()
        for lg in logs_resp.get("items") or []:
            for entry in lg.get("entries") or []:
                if q in str(entry.get("text", "")).lower():
                    val = {"action": "promote"}
                    if params.get("text"):
                        val["text"] = params["text"]
                    _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/memory/logs/{lg['date']}/{entry['id']}/actions", {
                        "request_id": str(uuid4()), "expected_revision": int(lg.get("revision", 0)), "value": val,
                    })
                    return {"matched": True, "date": lg["date"], "entry_id": entry["id"], "response": res}
        return {"matched": False, "query": q}

    if action == "dismiss_working_matching":
        st, wm = http_json(base_url, "GET", f"/users/{user_id}/v1/memory/working")
        if st != 200 or not isinstance(wm, dict):
            return {"error": f"get working memory failed: {st}"}
        q = str(params.get("query", "")).lower()
        for item in wm.get("items") or []:
            if q in str(item.get("quote", "")).lower() or q in str(item.get("note", "")).lower():
                _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/memory/working/{item['id']}/actions", {
                    "request_id": str(uuid4()), "expected_revision": int(wm.get("revision", 0)), "value": {"action": "dismiss"},
                })
                return {"matched": True, "item_id": item["id"], "response": res}
        return {"matched": False, "query": q}

    return {"skipped": f"unknown memory_ui action {action}"}


def _match_query(params: dict[str, Any]) -> str:
    for key in ("query", "match_title", "title_match", "match_text", "match", "title"):
        val = params.get(key)
        if isinstance(val, str) and val.strip():
            return val.strip().lower()
    return ""


def _exec_permission_ui(base_url: str, user_id: str, tool_ids: dict[str, str], action: str, params: dict[str, Any]) -> Any:
    if action in ("create", "create_rule", "add_rule"):
        tids = [tool_ids[n] for n in (params.get("tool_names") or []) if n in tool_ids]
        val: dict[str, Any] = {
            "effect": params.get("effect", "allow"),
            "tool_ids": tids,
            "connection_ids": [],
            "resource_ids": [],
            "action_classes": params.get("action_classes") or ["write", "send", "unknown"],
            "title": params.get("title", "Simulation rule"),
        }
        if params.get("recipients"):
            val["recipients"] = params["recipients"]
        if params.get("max_operations"):
            val["max_operations"] = int(params["max_operations"])
        _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/permissions", {
            "request_id": str(uuid4()), "expected_revision": 0, "value": val,
        })
        return res
    if action in ("revoke", "revoke_rule"):
        st, rules = http_json(base_url, "GET", f"/users/{user_id}/v1/permissions")
        q = _match_query(params)
        if st == 200 and isinstance(rules, dict):
            for r in rules.get("items") or []:
                if q in str(r.get("title", "")).lower():
                    _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/permissions/{r['id']}/actions", {
                        "request_id": str(uuid4()), "expected_revision": int(r.get("revision", 0)), "value": {"action": "revoke"},
                    })
                    return res
    return {"skipped": action}


def _exec_automation_ui(base_url: str, user_id: str, tz_name: str, action: str, params: dict[str, Any]) -> Any:
    if action in ("create", "create_automation"):
        trigger = dict(params.get("trigger") or {})
        if not trigger:
            # Convert simple cron/schedule shorthand to Nori recurring Trigger
            trigger = {
                "kind": "recurring",
                "local_start": "2027-01-05T08:00:00",
                "rrule": "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR",
                "timezone": params.get("timezone") or tz_name,
            }
        else:
            trigger.setdefault("timezone", params.get("timezone") or tz_name)
        val = {
            "title": params.get("title", "Scheduled automation"),
            "instruction": params.get("instruction") or params.get("prompt") or "Run scheduled check.",
            "trigger": trigger,
            "connection_ids": [],
            "budget_usd": float(params.get("budget_usd", 1.0)),
            "max_active_runs": int(params.get("max_active_runs", 1)),
            "missed_run": params.get("missed_run", "skip"),
            "execution_window_seconds": int(params.get("execution_window_seconds", 3600)),
            "destination_ids": ["dest_in_app"],
        }
        _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/automations", {
            "request_id": str(uuid4()), "expected_revision": 0, "value": val,
        })
        return res
    if action in ("pause", "pause_automation", "resume", "resume_automation"):
        api_act = "pause" if action.startswith("pause") else "resume"
        st, items = http_json(base_url, "GET", f"/users/{user_id}/v1/automations")
        q = _match_query(params)
        if st == 200 and isinstance(items, dict):
            for a in items.get("items") or []:
                if q in str(a.get("title", "")).lower():
                    _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/automations/{a['id']}/actions", {
                        "request_id": str(uuid4()), "expected_revision": int(a.get("revision", 0)), "value": {"action": api_act},
                    })
                    return res
    return {"skipped": action}


def _normalize_deadline(raw: Any) -> str | None:
    if not raw or not isinstance(raw, str):
        return None
    raw = raw.strip()
    if len(raw) == 10 and raw[4] == "-" and raw[7] == "-":
        return f"{raw}T23:59:59Z"
    return raw


def _exec_goal_ui(base_url: str, user_id: str, action: str, params: dict[str, Any]) -> Any:
    if action in ("create", "create_goal"):
        outcome = params.get("outcome") or params.get("description") or params.get("notes") or params.get("title", "Goal")
        val: dict[str, Any] = {
            "title": params.get("title", "Goal"),
            "outcome": str(outcome),
            "completion_criteria": params.get("completion_criteria") or [{"id": "c1", "text": "Complete target milestone", "met": False}],
        }
        dl = _normalize_deadline(params.get("deadline") or params.get("target_date"))
        if dl:
            val["deadline"] = dl
        _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/goals", {
            "request_id": str(uuid4()), "expected_revision": 0, "value": val,
        })
        return res
    if action in ("update", "update_goal", "complete"):
        st, goals = http_json(base_url, "GET", f"/users/{user_id}/v1/goals")
        q = _match_query(params)
        if st == 200 and isinstance(goals, dict):
            for g in goals.get("items") or []:
                if not q or q in str(g.get("title", "")).lower():
                    if action == "complete" or params.get("status") == "completed":
                        _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/goals/{g['id']}/actions", {
                            "request_id": str(uuid4()), "expected_revision": int(g.get("revision", 0)),
                            "value": {"action": "complete"},
                        })
                        return res
                    progress = params.get("progress") or params.get("progress_note") or g.get("progress", "")
                    val = {
                        "title": g.get("title", ""),
                        "outcome": g.get("outcome", ""),
                        "completion_criteria": g.get("completion_criteria") or [],
                        "progress": str(progress),
                    }
                    if g.get("deadline"):
                        val["deadline"] = g["deadline"]
                    _, res = http_json(base_url, "PUT", f"/users/{user_id}/v1/goals/{g['id']}", {
                        "request_id": str(uuid4()), "expected_revision": int(g.get("revision", 0)), "value": val,
                    })
                    return res
    return {"skipped": action}


def _exec_library_ui(base_url: str, user_id: str, action: str, params: dict[str, Any]) -> Any:
    if action == "search":
        q = quote(_match_query(params))
        _, res = http_json(base_url, "GET", f"/users/{user_id}/v1/search?q={q}")
        return res
    if action == "delete_matching":
        st, lib = http_json(base_url, "GET", f"/users/{user_id}/v1/library")
        q = _match_query(params)
        scope = params.get("scope", "all_versions")
        if st == 200 and isinstance(lib, dict):
            for item in lib.get("items") or []:
                if q in str(item.get("title", "")).lower() or q in str(item.get("summary", "")).lower():
                    _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/library/{item['id']}/actions", {
                        "request_id": str(uuid4()), "expected_revision": int(item.get("revision", 0)),
                        "value": {"action": "delete", "scope": scope},
                    })
                    return {"deleted": item["id"], "scope": scope, "response": res}
        return {"matched": False, "query": q}
    if action == "revise":
        st, lib = http_json(base_url, "GET", f"/users/{user_id}/v1/library")
        q = _match_query(params)
        instruction = str(params.get("instruction") or "Revise this document.")
        if st == 200 and isinstance(lib, dict):
            for item in lib.get("items") or []:
                if not q or q in str(item.get("title", "")).lower() or q in str(item.get("summary", "")).lower():
                    _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/library/{item['id']}/actions", {
                        "request_id": str(uuid4()), "expected_revision": int(item.get("revision", 0)),
                        "value": {"action": "revise", "instruction": instruction},
                    })
                    return {"revised": item["id"], "response": res}
        return {"matched": False, "query": q}
    if action == "update":
        st, lib = http_json(base_url, "GET", f"/users/{user_id}/v1/library")
        q = _match_query(params)
        if st == 200 and isinstance(lib, dict):
            for item in lib.get("items") or []:
                if not q or q in str(item.get("title", "")).lower():
                    val: dict[str, Any] = {"title": params.get("title") or item.get("title", "")}
                    if params.get("collection"):
                        val["collection"] = params["collection"]
                    _, res = http_json(base_url, "PUT", f"/users/{user_id}/v1/library/{item['id']}", {
                        "request_id": str(uuid4()), "expected_revision": int(item.get("revision", 0)), "value": val,
                    })
                    return {"updated": item["id"], "response": res}
        return {"matched": False, "query": q}
    if action in ("create", "save_document"):
        # Save via chat upload so the document enters Nori's persistent artifact/library store cleanly
        title = str(params.get("title") or "Untitled Document")
        content = str(params.get("content") or "")
        safe_name = "".join(ch if ch.isalnum() or ch in ("-", "_") else "_" for ch in title.lower())[:48] + ".md"
        uid = str(uuid4())
        body = f"# {title}\n\n{content}\n".encode("utf-8")
        _, res = http_json(
            base_url, "POST", f"/users/{user_id}/v1/uploads?id={uid}&filename={quote(safe_name)}",
            raw_bytes=body, content_type="text/markdown",
        )
        return {"uploaded_doc": uid, "filename": safe_name, "response": res}
    return {"skipped": action}


def _exec_interaction_reply(base_url: str, user_id: str, action: str, params: dict[str, Any]) -> Any:
    st, inter_resp = http_json(base_url, "GET", f"/users/{user_id}/v1/interactions?status=pending")
    pending = (inter_resp.get("items") or []) if st == 200 and isinstance(inter_resp, dict) else []
    if not pending:
        return {"pending_count": 0}
    decision = str(params.get("decision") or action).lower()
    kind = "approve" if decision.startswith("approve") else ("decline" if decision.startswith("decline") else "answer")
    note = params.get("text") or params.get("note") or params.get("reason") or params.get("comment") or ""
    results = []
    for item in pending:
        val: dict[str, Any] = {"kind": kind}
        if note:
            val["text"] = str(note)
        if item.get("action_digest") and kind in ("approve", "decline"):
            val["action_digest"] = item["action_digest"]
        _, res = http_json(base_url, "POST", f"/users/{user_id}/v1/interactions/{item['id']}/responses", {
            "request_id": str(uuid4()), "expected_revision": int(item.get("revision", 0)), "value": val,
        })
        results.append({"id": item["id"], "kind": kind, "response": res})
    return results


def select_personas(preset: str | None, persona: str | None) -> list[str]:
    all_dirs = sorted(d.name for d in PERSONAS_DIR.iterdir() if d.is_dir() and (d / "profile.json").is_file())
    if persona:
        if persona not in all_dirs:
            raise ValueError(f"Unknown persona {persona!r}; available: {', '.join(all_dirs)}")
        return [persona]
    target_preset = preset or "smoke"
    selected = []
    for slug in all_dirs:
        prof = json.loads((PERSONAS_DIR / slug / "profile.json").read_text(encoding="utf-8"))
        if target_preset in (prof.get("presets") or []):
            selected.append(slug)
    if not selected:
        raise ValueError(f"No personas matched preset {target_preset!r}")
    return selected


def main() -> int:
    parser = argparse.ArgumentParser(description="Run Nori Real-Life Persona Simulations.")
    parser.add_argument("--preset", choices=["smoke", "memory-pack", "full"], default=None, help="Persona subset preset")
    parser.add_argument("--persona", help="Run a single persona slug (e.g. 01-maya-lin)")
    parser.add_argument("--concurrency", type=int, default=1, help="Number of personas to run in parallel")
    parser.add_argument("--tag", default="run", help="Label for output folder under .sim-runs/")
    parser.add_argument("--stub-models", action="store_true", help="Use local deterministic model stub (free harness smoke test)")
    parser.add_argument("--max-events", type=int, default=None, help="Optional cap on events per persona (for fast smoke checks)")
    parser.add_argument("--workbench-seed", action="store_true", help="Replay against a running local Nori stack for UI inspection")
    parser.add_argument("--target-url", default="http://127.0.0.1:8080", help="Local Nori URL when using --workbench-seed")
    parser.add_argument("--keep-db", action="store_true", help="Keep the ephemeral Postgres database after completion")
    parser.add_argument("--no-grade", action="store_true", help="Skip automatic grading after run")
    parser.add_argument("--compare", type=Path, help="Optional baseline scorecard.json to diff against")
    args = parser.parse_args()

    slugs = select_personas(args.preset, args.persona)
    env_file = resolve_dotenv_path(ROOT)
    dotenv = load_dotenv(env_file)
    if env_file.is_file():
        print(f"Using env file: {env_file}", flush=True)

    stamp = time.strftime("%Y%m%d-%H%M%S", time.localtime())
    run_root = RUNS_DIR / f"{stamp}-{args.tag}"
    run_root.mkdir(parents=True, exist_ok=True)
    print(f"Run directory: {run_root}", flush=True)
    print(f"Personas ({len(slugs)}): {', '.join(slugs)}", flush=True)

    with ExitStack() as stack:
        bin_dir = Path(stack.enter_context(tempfile.TemporaryDirectory(prefix="nori-sim-bin-")))
        nori_sim_bin, nori_user_bin = build_binaries(bin_dir)

        mcp_server = SandboxMCPServer()
        mcp_server.start()
        stack.callback(mcp_server.stop)

        stub_url: str | None = None
        if args.stub_models:
            stub_srv = stack.enter_context(build_stub_model_server())
            threading.Thread(target=stub_srv.serve_forever, daemon=True).start()
            stack.callback(stub_srv.shutdown)
            stub_url = f"http://127.0.0.1:{stub_srv.server_port}"

        if args.workbench_seed:
            pg_url = os.environ.get("NORI_POSTGRES_URL") or dotenv.get("NORI_POSTGRES_URL") or "postgres://nori@127.0.0.1:15432/nori?sslmode=disable"
        else:
            pg_url, _ = ensure_ephemeral_postgres(stack, dotenv, args.keep_db, nori_sim_bin=nori_sim_bin)

        def _exec(slug: str) -> dict[str, Any]:
            return run_single_persona(
                slug=slug,
                run_root=run_root,
                nori_sim_bin=None if args.workbench_seed else nori_sim_bin,
                nori_user_bin=nori_user_bin,
                pg_url=pg_url,
                mcp_server=mcp_server,
                dotenv=dotenv,
                stub_model_url=stub_url,
                max_events=args.max_events,
                workbench_url=args.target_url if args.workbench_seed else None,
            )

        if args.concurrency > 1 and len(slugs) > 1:
            with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as ex:
                futs = {ex.submit(_exec, s): s for s in slugs}
                for fut in concurrent.futures.as_completed(futs):
                    fut.result()
        else:
            for s in slugs:
                _exec(s)

    if not args.no_grade:
        sc = grade_run(run_root, deterministic_only=args.stub_models, compare_path=args.compare)
        print("\n" + (run_root / "scorecard.md").read_text(encoding="utf-8"))
        return 0 if sc["all_tier_a_passed"] else 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
