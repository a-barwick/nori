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
import re
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
from contextlib import ExitStack, closing
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable
from urllib.parse import quote, urlparse
from uuid import uuid4
from zoneinfo import ZoneInfo

from grader import grade_run
from llm_client import (
    AdaptivePersonaResponder,
    LLMClient,
    load_dotenv,
    resolve_dotenv_path,
    strip_raw_provider_keys,
)
from mcp_sandbox import READ_ONLY_TOOL_NAMES, SandboxMCPServer

ROOT = Path(__file__).resolve().parents[2]
CORE = ROOT / "core"
PERSONAS_DIR = ROOT / "sim" / "personas"
RUNS_DIR = ROOT / ".sim-runs"

_db_init_lock = threading.Lock()


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


def _supports_user_id_flag(nori_sim_bin: Path) -> bool:
    """Detect whether the compiled nori-sim binary accepts --user-id."""
    try:
        res = subprocess.run(
            [str(nori_sim_bin), "-h"],
            cwd=CORE,
            timeout=15,
            text=True,
            capture_output=True,
        )
        return "--user-id" in ((res.stdout or "") + (res.stderr or ""))
    except Exception:
        return False


def _flatten_chat_content(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for blk in content:
            if isinstance(blk, dict):
                txt = blk.get("text")
                if isinstance(txt, str):
                    parts.append(txt)
            elif isinstance(blk, str):
                parts.append(blk)
        return "\n".join(parts)
    return str(content or "")


def _extract_chat_user_text(msgs: list[dict[str, Any]]) -> str:
    """Extract the user's text from Chat Completions messages, supporting cache breakpoints and commandPlan JSON."""
    raw_user = ""
    for m in reversed(msgs):
        if m.get("role") == "user":
            raw_user = _flatten_chat_content(m.get("content", ""))
            break
    if not raw_user:
        return ""
    stripped = raw_user.strip()
    # commandPlan wraps current user messages in a JSON array after a runtime prefix
    bracket_idx = stripped.find("[{")
    if bracket_idx >= 0:
        tail = stripped[bracket_idx:]
        try:
            arr = json.loads(tail)
            if isinstance(arr, list):
                texts = [str(item["text"]) for item in arr if isinstance(item, dict) and isinstance(item.get("text"), str)]
                if texts:
                    return "\n".join(texts)
        except json.JSONDecodeError:
            pass
    return raw_user


def _stub_autoreview_decision(prompt_text: str) -> dict[str, str]:
    low = prompt_text.lower()
    for bad in ("exfiltrat", "hypothetical", "do not send", "don't send", "never send", "unauthorized", "attacker", "evil.", "leaked"):
        if bad in low:
            return {"verdict": "deny", "reason": f"Blocked prohibited or suspicious action ({bad})."}
    if "ambiguous" in low or "unclear" in low:
        return {"verdict": "ask", "reason": "Action is ambiguous; confirm with user."}
    return {"verdict": "allow", "reason": "Action matches verified user request."}


def _extract_responses_text(items: list[Any]) -> str:
    parts: list[str] = []
    for item in items:
        if not isinstance(item, dict):
            continue
        if item.get("type") == "function_call_output":
            continue
        content = item.get("content")
        if isinstance(content, str) and content.strip():
            parts.append(content)
        elif isinstance(content, list):
            for blk in content:
                if isinstance(blk, dict) and isinstance(blk.get("text"), str):
                    parts.append(blk["text"])
        elif isinstance(item.get("text"), str):
            parts.append(item["text"])
    return "\n".join(parts)


def build_stub_model_server() -> ThreadingHTTPServer:
    """OpenAI Chat + Responses loopback stub for `--stub-models` runs and harness tests."""
    call_seq = itertools.count(1)
    stats_lock = threading.Lock()
    stats: dict[str, int] = {
        "chat_calls": 0,
        "responses_calls": 0,
        "extract_only_calls": 0,
        "flush_compact_calls": 0,
        "autoreview_calls": 0,
        "tool_calls": 0,
    }

    def _inc(key: str) -> None:
        with stats_lock:
            stats[key] = stats.get(key, 0) + 1

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
            _inc("chat_calls")
            msgs = req.get("messages") or []
            all_text = "\n".join(_flatten_chat_content(m.get("content", "")) for m in msgs)
            if "You are Nori's independent action-boundary safety reviewer." in all_text:
                _inc("autoreview_calls")
                content = json.dumps(_stub_autoreview_decision(all_text))
                usage = {"prompt_tokens": 90, "completion_tokens": 20, "total_tokens": 110}
                meta = {"model": req.get("model", "stub"), "service_tier": "default", "usage": usage}
                self._send(json.dumps({
                    **meta,
                    "choices": [{"message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
                }).encode("utf-8"))
                return

            last_user = _extract_chat_user_text(msgs)
            text = f"Got it! Noted: {last_user[:140]}"
            schema = (req.get("response_format") or {}).get("json_schema") or {}
            if schema.get("name") == "nori_response":
                actions: list[dict[str, Any]] = []
                low = last_user.lower()
                if "remember" in low and "dont save" not in low and "don't save" not in low and "lol jk" not in low and "off the record" not in low:
                    actions.append({
                        "name": "remember",
                        "arguments": {
                            "text": f"The user noted: {last_user[:160]}",
                            "about": "the user",
                            "section": "Context",
                            "replaces": None,
                        },
                    })
                dispatch_cues = (
                    "save to library", "email", "send", "calendar", "issue", "search",
                    "draft", "schedule", "check", "find", "review", "update",
                    "ask me", "approval", "autoreview", "exfiltrat", "weather",
                    "forecast", "report", "summary", "create a saved library",
                )
                has_attachment = "[attachment" in all_text.lower() or ".md" in low or ".pdf" in low or ".png" in low
                if any(cue in low for cue in dispatch_cues) or has_attachment:
                    actions.append({
                        "name": "dispatch",
                        "arguments": {
                            "request": last_user[:8000] or "Complete the user's request.",
                            "for": None,
                            "question_id": None,
                            "attachments": [],
                            "revises": None,
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
            _inc("responses_calls")
            tools = req.get("tools") or []
            tool_names = [t.get("name", "") for t in tools if isinstance(t, dict)]
            input_items = req.get("input") or []
            cid = f"call_stub_{next(call_seq)}"

            if "flush_and_compact" in tool_names:
                raw_text = _extract_responses_text(input_items)
                payload: dict[str, Any] = {}
                try:
                    payload = json.loads(raw_text) if raw_text.strip() else {}
                except json.JSONDecodeError:
                    payload = {}
                is_extract_only = bool(payload.get("extract_only"))
                if is_extract_only:
                    _inc("extract_only_calls")
                else:
                    _inc("flush_compact_calls")

                core_upserts: list[dict[str, Any]] = []
                daily_log_entries: list[dict[str, Any]] = []
                summary_items: list[dict[str, Any]] = []
                for turn in payload.get("turns") or []:
                    if not isinstance(turn, dict) or turn.get("already_extracted"):
                        continue
                    for msg in turn.get("messages") or []:
                        if not isinstance(msg, dict) or msg.get("from") != "user":
                            continue
                        input_id = str(msg.get("input_id") or "").strip()
                        mtext = str(msg.get("text") or "")
                        if not input_id or not mtext.strip():
                            continue
                        quote_txt = mtext[:200]
                        entry_txt = mtext[:600]
                        daily_log_entries.append({
                            "text": entry_txt,
                            "about": "the user",
                            "section": "Context",
                            "input_id": input_id,
                            "run_id": "",
                            "quote": quote_txt,
                        })
                        if "remember" in mtext.lower() and "dont save" not in mtext.lower() and "don't save" not in mtext.lower():
                            core_upserts.append({
                                "text": quote_txt,
                                "about": "the user",
                                "section": "Context",
                                "input_id": input_id,
                                "quote": quote_txt,
                            })
                        if not is_extract_only and len(summary_items) < 5:
                            summary_items.append({
                                "kind": "fact",
                                "quote": quote_txt,
                                "input_id": input_id,
                                "note": "Stub summary item",
                            })

                args = {
                    "core_upserts": core_upserts,
                    "daily_log_entries": daily_log_entries,
                    "summary_items": [] if is_extract_only else summary_items,
                    "gist": "" if is_extract_only else "Stub compaction summary covering recent conversation turns.",
                }
                output = [{"type": "function_call", "call_id": cid, "name": "flush_and_compact", "arguments": json.dumps(args)}]
            elif "dream_consolidate" in tool_names:
                args = {"core_upserts": [], "core_deletes": [], "daily_log_entries": []}
                output = [{"type": "function_call", "call_id": cid, "name": "dream_consolidate", "arguments": json.dumps(args)}]
            elif "complete" in tool_names:
                prior_outputs = [
                    it for it in input_items
                    if isinstance(it, dict) and it.get("type") == "function_call_output"
                ]
                prompt_text = _extract_responses_text(input_items)
                low = prompt_text.lower()

                if prior_outputs:
                    args = {
                        "answer": "Stub task completed after tool execution.",
                        "evidence": [],
                        "unmet": [],
                    }
                    output = [{"type": "function_call", "call_id": cid, "name": "complete", "arguments": json.dumps(args)}]
                elif "save to library" in low or "saved library document" in low:
                    args = {
                        "answer": prompt_text.strip() or "Saved document content.",
                        "evidence": [],
                        "unmet": [],
                    }
                    output = [{"type": "function_call", "call_id": cid, "name": "complete", "arguments": json.dumps(args)}]
                elif "request_input" in tool_names and any(k in low for k in ("ask me", "clarif", "question")):
                    _inc("tool_calls")
                    output = [{
                        "type": "function_call",
                        "call_id": cid,
                        "name": "request_input",
                        "arguments": json.dumps({"question": "Please confirm the details before I proceed."}),
                    }]
                else:
                    email_send_tool = next((n for n in tool_names if n.startswith("mcp_") and n.endswith("__email_send")), "")
                    web_search_tool = next((n for n in tool_names if n.startswith("mcp_") and n.endswith("__web_search")), "")
                    email_search_tool = next((n for n in tool_names if n.startswith("mcp_") and n.endswith("__email_search")), "")

                    if email_send_tool and any(k in low for k in ("email", "send", "autoreview", "exfiltrat")):
                        _inc("tool_calls")
                        m_addr = re.search(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}", prompt_text)
                        recip = m_addr.group(0) if m_addr else "colleague@example.com"
                        output = [{
                            "type": "function_call",
                            "call_id": cid,
                            "name": email_send_tool,
                            "arguments": json.dumps({"to": [recip], "subject": "Stub Update", "body": prompt_text[:300]}),
                        }]
                    elif web_search_tool and any(k in low for k in ("web_search", "search the web", "look up", "weather", "forecast")):
                        _inc("tool_calls")
                        output = [{
                            "type": "function_call",
                            "call_id": cid,
                            "name": web_search_tool,
                            "arguments": json.dumps({"query": prompt_text[:120] or "weather"}),
                        }]
                    elif email_search_tool and any(k in low for k in ("inbox", "search email", "find email", "check email")):
                        _inc("tool_calls")
                        output = [{
                            "type": "function_call",
                            "call_id": cid,
                            "name": email_search_tool,
                            "arguments": json.dumps({"query": "update"}),
                        }]
                    else:
                        args = {"answer": "Stub task completed.", "evidence": [], "unmet": []}
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

    srv = ThreadingHTTPServer(("127.0.0.1", 0), SimModelStub)
    srv.stats = stats  # type: ignore[attr-defined]
    return srv


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
                with _db_init_lock:
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
                with _db_init_lock:
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
        st_r, res_r = http_json(base_url, "POST", f"/users/{user_id}/v1/permissions", {
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
        if st_r >= 400:
            raise RuntimeError(f"POST /v1/permissions (read tools) failed ({st_r}): {res_r}")

    for perm in initial_perms:
        tool_names = perm.get("tool_names") or []
        tids = [tool_id_by_name[n] for n in tool_names if n in tool_id_by_name]
        effect = perm["effect"]
        val: dict[str, Any] = {
            "effect": effect,
            "tool_ids": tids,
            "connection_ids": [],
            "resource_ids": [],
            "action_classes": _normalize_action_classes(perm.get("action_classes"), effect, tool_names),
            "title": perm["title"],
        }
        if perm.get("recipients"):
            val["recipients"] = perm["recipients"]
        if perm.get("max_operations"):
            val["max_operations"] = perm["max_operations"]
        st_p, res_p = http_json(base_url, "POST", f"/users/{user_id}/v1/permissions", {
            "request_id": str(uuid4()),
            "expected_revision": 0,
            "value": val,
        })
        if st_p >= 400:
            raise RuntimeError(f"POST /v1/permissions ({perm.get('title')}) failed ({st_p}): {res_p}")

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


def latest_assistant_turn(
    base_url: str,
    user_id: str,
    since_msg_id: str = "",
) -> tuple[str, str, list[dict[str, Any]]]:
    """Return (latest_msg_id, latest_msg_text, untruncated_show_results_cards)."""
    st, snap = http_json(base_url, "GET", f"/users/{user_id}/v1/snapshot")
    if st != 200 or not isinstance(snap, dict):
        return "", "", []

    msgs = snap.get("messages") or []
    latest_id = ""
    latest_text = ""
    for m in reversed(msgs):
        if m.get("role") == "assistant" and (m.get("text") or m.get("result_ids") or m.get("resultIds") or m.get("parts")):
            latest_id = str(m.get("id", ""))
            latest_text = str(m.get("text", ""))
            break

    # Collect result IDs from assistant messages after since_msg_id (or from the latest assistant message)
    start_idx = 0
    if since_msg_id:
        for i, m in enumerate(msgs):
            if str(m.get("id", "")) == since_msg_id:
                start_idx = i + 1
                break
        else:
            start_idx = max(0, len(msgs) - 1)
    else:
        for i in range(len(msgs) - 1, -1, -1):
            if str(msgs[i].get("id", "")) == latest_id:
                start_idx = i
                break

    ordered_rids: list[str] = []
    seen_rids: set[str] = set()
    for m in msgs[start_idx:]:
        if m.get("role") != "assistant":
            continue
        for rid in (m.get("result_ids") or m.get("resultIds") or []):
            s_rid = str(rid or "").strip()
            if s_rid and s_rid not in seen_rids:
                seen_rids.add(s_rid)
                ordered_rids.append(s_rid)
        for part in (m.get("parts") or []):
            if not isinstance(part, dict):
                continue
            ptype = str(part.get("type", "")).lower()
            if ptype in ("artifact", "image", "link", "result", "card"):
                s_rid = str(part.get("result_id") or part.get("id") or "").strip()
                if s_rid and s_rid not in seen_rids:
                    seen_rids.add(s_rid)
                    ordered_rids.append(s_rid)

    snap_results_by_id = {
        str(r.get("id")): r
        for r in (snap.get("results") or [])
        if isinstance(r, dict) and r.get("id")
    }
    show_results: list[dict[str, Any]] = []
    for rid in ordered_rids:
        card_src = snap_results_by_id.get(rid)
        st_r, full_r = http_json(base_url, "GET", f"/users/{user_id}/v1/results/{quote(rid)}")
        if st_r == 200 and isinstance(full_r, dict) and full_r.get("id"):
            card_src = full_r
        if isinstance(card_src, dict):
            show_results.append({
                "id": str(card_src.get("id", rid)),
                "title": str(card_src.get("title", "")),
                "kind": str(card_src.get("kind", "")),
                "text": str(card_src.get("text", "")),
                "name": str(card_src.get("name", "")),
                "url": str(card_src.get("url", "")),
                "run_id": str(card_src.get("run_id", "")),
                "version": int(card_src.get("version") or 1),
                "evidence": card_src.get("evidence") or [],
                "unmet": card_src.get("unmet") or [],
            })

    return latest_id, latest_text, show_results


def latest_assistant_message(base_url: str, user_id: str) -> tuple[str, str]:
    msg_id, msg_text, _ = latest_assistant_turn(base_url, user_id)
    return msg_id, msg_text


def _estimate_usage_cost_usd(rec: dict[str, Any]) -> float | None:
    """Fallback tariff matching core/internal/usage/openai.go when cost_usd is absent."""
    model = str(rec.get("model") or "").lower()
    inp = int(rec.get("input_tokens") or 0)
    cached = int(rec.get("cached_tokens") or 0)
    cache_write = int(rec.get("cache_write_tokens") or 0)
    out_tok = int(rec.get("output_tokens") or 0)
    uncached = max(0, inp - cached - cache_write)
    if "gpt-6.1-sol" in model or "gpt-6.2-sol" in model:
        return (uncached * 2.0 + cached * 0.10 + cache_write * 2.50 + out_tok * 10.0) / 1_000_000.0
    if "gpt-6.1-terra" in model:
        if inp > 272_000:
            return (uncached * 2.0 + cached * 0.10 + out_tok * 7.50) / 1_000_000.0
        return (uncached * 1.0 + cached * 0.05 + out_tok * 5.0) / 1_000_000.0
    return None


def aggregate_model_usage(records: list[dict[str, Any]]) -> dict[str, Any]:
    """Aggregate state.Usage records into summary.json['model_usage']."""
    total_in = 0
    total_out = 0
    total_cached = 0
    total_cache_write = 0
    total_cost = 0.0
    unknown_cost_calls = 0
    by_model: dict[str, dict[str, Any]] = {}
    by_provider: dict[str, dict[str, Any]] = {}
    clean_records: list[dict[str, Any]] = []

    for rec in records or []:
        if not isinstance(rec, dict):
            continue
        clean_records.append(rec)
        model = str(rec.get("model") or "unknown")
        provider = str(rec.get("provider") or "unknown")
        inp = int(rec.get("input_tokens") or 0)
        out_tok = int(rec.get("output_tokens") or 0)
        cached = int(rec.get("cached_tokens") or 0)
        cache_write = int(rec.get("cache_write_tokens") or 0)

        raw_cost = rec.get("cost_usd")
        if raw_cost is not None:
            cost = float(raw_cost)
        else:
            est = _estimate_usage_cost_usd(rec)
            if est is not None:
                cost = est
            else:
                cost = 0.0
                unknown_cost_calls += 1

        total_in += inp
        total_out += out_tok
        total_cached += cached
        total_cache_write += cache_write
        total_cost += cost

        m_entry = by_model.setdefault(model, {
            "provider": provider,
            "calls": 0,
            "input_tokens": 0,
            "output_tokens": 0,
            "cached_tokens": 0,
            "cache_write_tokens": 0,
            "cost_usd": 0.0,
        })
        if provider != "unknown" and m_entry["provider"] == "unknown":
            m_entry["provider"] = provider
        m_entry["calls"] += 1
        m_entry["input_tokens"] += inp
        m_entry["output_tokens"] += out_tok
        m_entry["cached_tokens"] += cached
        m_entry["cache_write_tokens"] += cache_write
        m_entry["cost_usd"] += cost

        p_entry = by_provider.setdefault(provider, {
            "calls": 0,
            "input_tokens": 0,
            "output_tokens": 0,
            "cached_tokens": 0,
            "cache_write_tokens": 0,
            "cost_usd": 0.0,
        })
        p_entry["calls"] += 1
        p_entry["input_tokens"] += inp
        p_entry["output_tokens"] += out_tok
        p_entry["cached_tokens"] += cached
        p_entry["cache_write_tokens"] += cache_write
        p_entry["cost_usd"] += cost

    for entry in by_model.values():
        entry["cost_usd"] = round(entry["cost_usd"], 6)
    for entry in by_provider.values():
        entry["cost_usd"] = round(entry["cost_usd"], 6)

    return {
        "call_count": len(clean_records),
        "total_input_tokens": total_in,
        "total_output_tokens": total_out,
        "total_cached_tokens": total_cached,
        "total_cache_write_tokens": total_cache_write,
        "total_cost_usd": round(total_cost, 6),
        "unknown_cost_calls": unknown_cost_calls,
        "by_model": by_model,
        "by_provider": by_provider,
        "records": clean_records,
    }


def _fetch_all_interactions(base_url: str, user_id: str) -> list[dict[str, Any]]:
    """Fetch all interactions across statuses for interactions.json / approvals log."""
    st, resp = http_json(base_url, "GET", f"/users/{user_id}/v1/interactions?limit=200")
    if st != 200 or not isinstance(resp, dict):
        return []
    items = [x for x in (resp.get("items") or []) if isinstance(x, dict)]
    return items


def _parse_iso_utc(raw: str) -> datetime | None:
    s = str(raw or "").strip()
    if not s:
        return None
    if s.endswith("Z"):
        s = s[:-1] + "+00:00"
    try:
        dt = datetime.fromisoformat(s)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        return dt.astimezone(timezone.utc)
    except ValueError:
        return None


def _clip_utf8(text: str, max_bytes: int) -> str:
    raw = text.encode("utf-8")
    if len(raw) <= max_bytes:
        return text
    clipped = raw[:max_bytes]
    while clipped:
        try:
            return clipped.decode("utf-8")
        except UnicodeDecodeError:
            clipped = clipped[:-1]
    return ""


def _record_ui_error(
    ui_errors: list[dict[str, Any]],
    *,
    event_id: str,
    kind: str,
    action: str,
    detail: Any,
    http_status: int | None = None,
) -> None:
    entry: dict[str, Any] = {
        "event_id": event_id,
        "kind": kind,
        "action": action,
        "detail": detail,
    }
    if http_status is not None:
        entry["http_status"] = http_status
    ui_errors.append(entry)


def _inspect_handler_result(
    res: Any,
    *,
    event_id: str,
    kind: str,
    action: str,
    ui_errors: list[dict[str, Any]],
) -> str | None:
    """Inspect a UI handler result and record any failure into ui_errors; return error summary if failed."""
    if isinstance(res, dict):
        st = res.get("http_status")
        if isinstance(st, int) and st >= 400 and not res.get("stale_verified"):
            msg = str(res.get("error") or f"HTTP {st}: {res.get('response', res)}")
            _record_ui_error(ui_errors, event_id=event_id, kind=kind, action=action, detail=msg, http_status=st)
            return msg
        if res.get("matched") is False:
            msg = str(res.get("error") or f"unmatched {kind}.{action}: {res}")
            _record_ui_error(ui_errors, event_id=event_id, kind=kind, action=action, detail=msg)
            return msg
        if "error" in res:
            msg = str(res["error"])
            _record_ui_error(ui_errors, event_id=event_id, kind=kind, action=action, detail=msg)
            return msg
        if "skipped" in res:
            msg = f"skipped: {res['skipped']}"
            _record_ui_error(ui_errors, event_id=event_id, kind=kind, action=action, detail=msg)
            return msg
        resp = res.get("response")
        if isinstance(resp, dict) and isinstance(resp.get("http_status"), int) and resp["http_status"] >= 400:
            msg = f"HTTP {resp['http_status']}: {resp}"
            _record_ui_error(ui_errors, event_id=event_id, kind=kind, action=action, detail=msg, http_status=resp["http_status"])
            return msg
    elif isinstance(res, list):
        for item in res:
            if isinstance(item, dict):
                err = _inspect_handler_result(item, event_id=event_id, kind=kind, action=action, ui_errors=ui_errors)
                if err:
                    return err
    return None


def _count_5xx_in_result(res: Any) -> int:
    if isinstance(res, dict):
        st = res.get("http_status")
        if isinstance(st, int) and st >= 500:
            return 1
        return _count_5xx_in_result(res.get("response"))
    if isinstance(res, list):
        return sum(_count_5xx_in_result(item) for item in res)
    return 0


def run_single_persona(
    slug: str,
    run_root: Path,
    nori_sim_bin: Path | None,
    nori_user_bin: Path | None,
    pg_url: str | None,
    mcp_server: SandboxMCPServer,
    dotenv: dict[str, str],
    stub_model_url: str | None,
    max_events: int | None,
    workbench_url: str | None,
    keep_db: bool = False,
    supports_user_id: bool = False,
    sim_url: str | None = None,
    personas_dir: Path | None = None,
) -> dict[str, Any]:
    started = time.monotonic()
    pdir = (personas_dir or PERSONAS_DIR) / slug
    profile = json.loads((pdir / "profile.json").read_text(encoding="utf-8"))
    month = json.loads((pdir / "month.json").read_text(encoding="utf-8"))

    out_dir = run_root / slug
    snaps_dir = out_dir / "snapshots"
    snaps_dir.mkdir(parents=True, exist_ok=True)

    sim_start_utc = compute_event_utc(
        profile["sim_start_date"], 1, "07:00", profile["timezone"]
    )
    mcp_url = mcp_server.register_persona(slug, profile.get("mcp_world") or {}, sim_time=sim_start_utc)
    world = mcp_server.get_world(slug)
    assert world is not None

    current_tz = profile["timezone"]
    cur_sim_utc = sim_start_utc
    http_5xx_count = 0
    harness_timeouts = 0
    unrecovered_drain_timeout = False
    ui_errors: list[dict[str, Any]] = []
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

        if sim_url:
            base_url = sim_url.rstrip("/")
            has_sim_api = True
            user_id = str(uuid4())
        elif workbench_url:
            base_url = workbench_url.rstrip("/")
            has_sim_api = False
            user_email = profile["email"]
            effective_pg_url = pg_url or os.environ.get("NORI_POSTGRES_URL") or dotenv.get("NORI_POSTGRES_URL") or "postgres://nori@127.0.0.1:15432/nori?sslmode=disable"
            # Provision user in target DB if nori-user available
            user_id = str(uuid4())
            if nori_user_bin:
                env = dict(os.environ)
                env.update(dotenv)
                env["NORI_CLOUD"] = "local"
                env["NORI_STORE_BACKEND"] = "postgres"
                env["NORI_POSTGRES_URL"] = effective_pg_url
                env["NORI_LOCAL_ARTIFACT_DIR"] = str(artifacts_dir)
                with _db_init_lock:
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
            effective_pg_url = pg_url
            if not effective_pg_url:
                effective_pg_url, _ = ensure_ephemeral_postgres(
                    stack, dotenv, keep_db=keep_db, nori_sim_bin=nori_sim_bin
                )
            port = pick_free_port()
            base_url = f"http://127.0.0.1:{port}"
            has_sim_api = True
            user_id = str(uuid4())

            env = dict(os.environ)
            if env.get("NORI_MODEL_PROVIDER", "").strip().lower() == "gateway":
                for k, v in strip_raw_provider_keys(dotenv).items():
                    env.setdefault(k, v)
                env = strip_raw_provider_keys(env)
            else:
                for k, v in dotenv.items():
                    env.setdefault(k, v)
            env.update({
                "NORI_ENV_FILE": os.devnull,
                "NORI_TASK_ENV_FILE": os.devnull,
                "NORI_CLOUD": "local",
                "NORI_STORE_BACKEND": "postgres",
                "NORI_POSTGRES_URL": effective_pg_url,
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

            with _db_init_lock:
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
            sim_cmd = [str(nori_sim_bin), "--start-time", sim_start_utc.strftime("%Y-%m-%dT%H:%M:%SZ")]
            if supports_user_id:
                sim_cmd.extend(["--user-id", user_id])
            proc = subprocess.Popen(
                sim_cmd,
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

        event_drain_timed_out = False

        def _sync_world_clock(dt_utc: datetime) -> None:
            nonlocal cur_sim_utc
            cur_sim_utc = dt_utc
            world.set_sim_time(dt_utc)

        def _set_clock(dt_utc: datetime) -> None:
            nonlocal http_5xx_count, cur_sim_utc
            if dt_utc <= cur_sim_utc:
                _sync_world_clock(cur_sim_utc)
                return
            if not has_sim_api:
                _sync_world_clock(dt_utc)
                return
            st, res = http_json(base_url, "POST", "/_sim/clock/set", {"time": dt_utc.strftime("%Y-%m-%dT%H:%M:%SZ")})
            if st >= 500:
                http_5xx_count += 1
                _record_ui_error(ui_errors, event_id="clock_set", kind="sim_clock", action="set", detail=f"HTTP {st}: {res}", http_status=st)
                return
            if st == 409:
                return
            if st >= 400:
                _record_ui_error(ui_errors, event_id="clock_set", kind="sim_clock", action="set", detail=f"HTTP {st}: {res}", http_status=st)
                return
            parsed_now = _parse_iso_utc(res.get("sim_now", "")) if isinstance(res, dict) else None
            _sync_world_clock(parsed_now or dt_utc)

        def _advance_clock(seconds: float) -> None:
            nonlocal http_5xx_count
            if seconds <= 0:
                return
            if not has_sim_api:
                _sync_world_clock(cur_sim_utc + timedelta(seconds=seconds))
                return
            st, res = http_json(base_url, "POST", "/_sim/clock/advance", {"seconds": seconds})
            if st >= 500:
                http_5xx_count += 1
                _record_ui_error(ui_errors, event_id="clock_advance", kind="sim_clock", action="advance", detail=f"HTTP {st}: {res}", http_status=st)
                return
            if st >= 400:
                _record_ui_error(ui_errors, event_id="clock_advance", kind="sim_clock", action="advance", detail=f"HTTP {st}: {res}", http_status=st)
                return
            parsed_now = _parse_iso_utc(res.get("sim_now", "")) if isinstance(res, dict) else None
            _sync_world_clock(parsed_now or (cur_sim_utc + timedelta(seconds=seconds)))

        def _drain(timeout_s: int = 180) -> dict[str, Any]:
            nonlocal http_5xx_count, harness_timeouts, unrecovered_drain_timeout, event_drain_timed_out
            if not has_sim_api:
                time.sleep(0.05 if workbench_url is None else 3)
                return {"quiescent": True}
            try:
                st, res = http_json(base_url, "POST", "/_sim/drain", {"user_id": user_id, "timeout_seconds": timeout_s}, timeout=timeout_s + 15)
            except (TimeoutError, socket.timeout) as exc:
                st = 504
                res = {"error": f"client drain timeout: {exc}", "quiescent": False}
            if st == 504:
                harness_timeouts += 1
                unrecovered_drain_timeout = True
                event_drain_timed_out = True
                out = dict(res) if isinstance(res, dict) else {"raw": res}
                out["quiescent"] = False
                out["timed_out"] = True
                out["http_status"] = 504
                return out
            if st >= 500:
                http_5xx_count += 1
            elif st == 200 and isinstance(res, dict) and res.get("quiescent"):
                unrecovered_drain_timeout = False
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
        st_pref, res_pref = apply_preferences(base_url, user_id, profile.get("preferences") or {})
        if st_pref >= 500:
            http_5xx_count += 1
        if st_pref >= 400:
            raise RuntimeError(f"Initial apply_preferences failed ({st_pref}): {res_pref}")

        tool_ids = connect_sandbox_mcp(
            base_url, user_id, mcp_url,
            (profile.get("mcp_world") or {}).get("server_name") or f"{profile['name']} Workspace",
            profile.get("initial_permissions") or [],
        )
        # Ensure intro snapshot
        st_intro, res_intro = http_json(base_url, "GET", f"/users/{user_id}/v1/snapshot")
        if st_intro >= 500:
            http_5xx_count += 1
        if st_intro >= 400:
            raise RuntimeError(f"Initial snapshot failed ({st_intro}): {res_intro}")
        _drain(60)

        events = month["events"]
        if max_events is not None:
            events = events[:max_events]

        last_msg_id = ""
        final_snap: dict[str, Any] = {}
        for idx, ev in enumerate(events):
            ev_id = ev["id"]
            day = int(ev["day"])
            local_time = ev["local_time"]
            if ev.get("timezone"):
                current_tz = ev["timezone"]
            ev_utc = compute_event_utc(profile["sim_start_date"], day, local_time, current_tz)
            prev_was_burst = idx > 0 and events[idx - 1].get("kind") == "chat" and events[idx - 1].get("wait_for_reply") is False
            event_drain_timed_out = False

            if not prev_was_burst:
                if has_sim_api and cur_sim_utc is not None:
                    gap_s = (ev_utc - cur_sim_utc).total_seconds()
                    if gap_s >= 125:
                        _advance_clock(125)
                        _drain(180)
                        _set_clock(ev_utc)
                    elif gap_s >= 120:
                        _set_clock(ev_utc)
                        _drain(180)
                    else:
                        _set_clock(ev_utc)
                else:
                    _set_clock(ev_utc)

            kind = ev["kind"]
            action = ev.get("action", "")
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
            if action:
                rec["action"] = action
            adaptive_followups: list[dict[str, str]] = []
            probe_nori_reply = ""
            probe_show_results: list[dict[str, Any]] = []

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
                if st >= 400:
                    _record_ui_error(ui_errors, event_id=ev_id, kind=kind, action="turn", detail=f"HTTP {st}: {turn_res}", http_status=st)
                rec["http_status"] = st
                rec["user_text"] = ev["text"]
                rec["attachments"] = att_ids

                wait_reply = ev.get("wait_for_reply", True)
                if wait_reply:
                    drain_res = _drain(180)
                    rec["drain"] = drain_res
                    msg_id, probe_nori_reply, probe_show_results = latest_assistant_turn(base_url, user_id, last_msg_id)
                    if msg_id:
                        last_msg_id = msg_id
                    rec["nori_reply"] = probe_nori_reply
                    rec["show_results"] = probe_show_results

                    # Adaptive loop (max 2 turns): resolve pending interactions or genuine clarifying questions
                    # Keep probe_nori_reply immutable (H-4).
                    if not ev.get("probe") or not ev["probe"].get("expect_no_external_write"):
                        latest_adaptive_reply = probe_nori_reply
                        for _step in range(2):
                            st_i, inter_resp = http_json(base_url, "GET", f"/users/{user_id}/v1/interactions?status=pending")
                            if st_i >= 500:
                                http_5xx_count += 1
                            pending_list = (inter_resp.get("items") or []) if st_i == 200 and isinstance(inter_resp, dict) else []
                            if pending_list and not _next_event_handles_interaction(events, idx):
                                for item in pending_list:
                                    decision = responder.decide_interaction(item, day, local_time)
                                    item_kind = str(item.get("kind", "")).lower()
                                    if item_kind == "question":
                                        ans_text = str(decision.get("text") or "").strip() or "Go ahead with the sensible default."
                                        payload: dict[str, Any] = {"kind": "answer", "text": ans_text}
                                    else:
                                        payload = {"kind": decision["kind"]}
                                        if decision.get("text") and decision["kind"] in ("answer", "decline"):
                                            payload["text"] = decision["text"]
                                        if item.get("action_digest") and decision["kind"] in ("approve", "decline"):
                                            payload["action_digest"] = item["action_digest"]
                                        if decision["kind"] == "approve" and decision.get("remember_rule"):
                                            payload["remember_rule"] = True
                                    st_r, res_r = http_json(base_url, "POST", f"/users/{user_id}/v1/interactions/{item['id']}/responses", {
                                        "request_id": str(uuid4()),
                                        "expected_revision": int(item.get("revision", 0)),
                                        "value": payload,
                                    })
                                    if st_r >= 500:
                                        http_5xx_count += 1
                                    if st_r >= 400:
                                        _record_ui_error(
                                            ui_errors, event_id=ev_id, kind="adaptive_interaction", action=payload["kind"],
                                            detail=f"HTTP {st_r}: {res_r}", http_status=st_r,
                                        )
                                    adaptive_followups.append({
                                        "type": "interaction",
                                        "interaction_id": str(item.get("id", "")),
                                        "kind": payload["kind"],
                                        "prompt": item.get("prompt", ""),
                                        "response": json.dumps(payload),
                                        "http_status": st_r,
                                    })
                                _drain(180)
                                post_msg_id, post_reply, _ = latest_assistant_turn(base_url, user_id, last_msg_id)
                                if post_msg_id:
                                    last_msg_id = post_msg_id
                                if post_reply:
                                    latest_adaptive_reply = post_reply
                                continue

                            followup = responder.decide_chat_followup(ev["text"], latest_adaptive_reply, day, local_time)
                            if followup:
                                st_f2, res_f2 = http_json(base_url, "POST", f"/users/{user_id}/api/turn", {
                                    "id": str(uuid4()),
                                    "message": followup,
                                    "attachments": [],
                                })
                                if st_f2 >= 500:
                                    http_5xx_count += 1
                                if st_f2 >= 400:
                                    _record_ui_error(
                                        ui_errors, event_id=ev_id, kind="adaptive_chat", action="turn",
                                        detail=f"HTTP {st_f2}: {res_f2}", http_status=st_f2,
                                    )
                                _drain(180)
                                fu_msg_id, fu_reply, _ = latest_assistant_turn(base_url, user_id, last_msg_id)
                                if fu_msg_id:
                                    last_msg_id = fu_msg_id
                                if fu_reply:
                                    latest_adaptive_reply = fu_reply
                                adaptive_followups.append({"type": "chat", "user": followup, "nori": fu_reply})
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

            elif kind == "sim_control":
                params = ev.get("params") or {}
                if action == "reset_context":
                    if not has_sim_api:
                        rec["result"] = {"skipped": "sim_control requires nori-sim"}
                    else:
                        _advance_clock(125)
                        _drain(180)
                        cf_body: dict[str, Any] = {}
                        if params.get("context_from"):
                            cf_body["context_from"] = params["context_from"]
                        st_c, res_c = http_json(base_url, "POST", f"/_sim/users/{user_id}/context_from", cf_body)
                        if st_c >= 500:
                            http_5xx_count += 1
                        if st_c >= 400:
                            rec["result"] = {"http_status": st_c, "error": f"context_from failed ({st_c}): {res_c}", "response": res_c}
                        else:
                            adv = int(params.get("advance_seconds", 1))
                            if adv > 0:
                                _advance_clock(adv)
                            rec["result"] = res_c
                else:
                    rec["result"] = {"skipped": f"unknown sim_control action {action}"}

            elif kind == "memory_ui":
                if has_sim_api and action != "create_core":
                    _advance_clock(125)
                    _drain(180)
                rec["result"] = _exec_memory_ui(base_url, user_id, action, ev.get("params") or {})
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
                if st_p >= 400:
                    _record_ui_error(ui_errors, event_id=ev_id, kind=kind, action="update", detail=f"HTTP {st_p}: {res_p}", http_status=st_p)

            elif kind == "permission_ui":
                rec["result"] = _exec_permission_ui(base_url, user_id, tool_ids, action, ev.get("params") or {})

            elif kind == "automation_ui":
                ev_local_date = compute_event_utc(profile["sim_start_date"], day, local_time, current_tz).astimezone(ZoneInfo(current_tz)).strftime("%Y-%m-%d")
                rec["result"] = _exec_automation_ui(base_url, user_id, current_tz, action, ev.get("params") or {}, default_date=ev_local_date)
                _drain(90)

            elif kind == "goal_ui":
                rec["result"] = _exec_goal_ui(base_url, user_id, action, ev.get("params") or {})

            elif kind == "library_ui":
                rec["result"] = _exec_library_ui(base_url, user_id, action, ev.get("params") or {}, drain_fn=_drain)
                if action == "revise":
                    _drain(180)

            elif kind == "interaction_reply":
                rec["result"] = _exec_interaction_reply(base_url, user_id, action, ev.get("params") or {})
                _drain(180)
                msg_id, probe_nori_reply, probe_show_results = latest_assistant_turn(base_url, user_id, last_msg_id)
                if msg_id:
                    last_msg_id = msg_id
                rec["nori_reply"] = probe_nori_reply
                rec["show_results"] = probe_show_results

            elif kind == "mcp_world_event":
                rec["result"] = world.apply_world_event(action, ev.get("params") or {})

            if kind in ("sim_control", "memory_ui", "permission_ui", "automation_ui", "goal_ui", "library_ui", "interaction_reply"):
                http_5xx_count += _count_5xx_in_result(rec.get("result"))
                err_msg = _inspect_handler_result(
                    rec.get("result"),
                    event_id=ev_id,
                    kind=kind,
                    action=action,
                    ui_errors=ui_errors,
                )
                if err_msg:
                    rec["error"] = err_msg

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
                    if st_f >= 400:
                        _record_ui_error(ui_errors, event_id=ev_id, kind="probe_fts", action="search", detail=f"HTTP {st_f}: {fts_res}", http_status=st_f)
                is_inconclusive = bool(event_drain_timed_out or unrecovered_drain_timeout)
                probe_entry: dict[str, Any] = {
                    "event_id": ev_id,
                    "day": day,
                    "local_time": local_time,
                    "probe": probe,
                    "user_text": ev.get("text", ""),
                    "nori_reply": probe_nori_reply,
                    "show_results": probe_show_results,
                    "inconclusive": is_inconclusive,
                    "adaptive_followups": adaptive_followups,
                    "mcp_writes_during_event": writes_delta,
                    "fts_result": fts_res,
                    "core_memories": snap_now.get("core_memories") or [],
                    "daily_logs": snap_now.get("daily_logs") or [],
                    "working_summary": snap_now.get("working_summary") or {},
                }
                if is_inconclusive:
                    probe_entry["inconclusive_reason"] = "drain_timeout"
                probe_records.append(probe_entry)

            trace_lines.append(rec)

        # Final idle settle + end-of-month snapshot (+20m idle so any remaining flush/dream completes)
        if has_sim_api:
            _advance_clock(1200)
            _drain(240)
            final_snap = _get_state()
            (snaps_dir / "z_final_eom.json").write_text(json.dumps(final_snap, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

        interactions_all = _fetch_all_interactions(base_url, user_id)

    # Aggregate model usage from final sim state (or snapshot fallback)
    raw_usage_records = final_snap.get("model_usage") or []
    model_usage = aggregate_model_usage(raw_usage_records)
    mcp_audit = world.audit()
    approvals_log = [it for it in interactions_all if str(it.get("kind", "")).lower() == "approval"]

    # Persist run artifacts
    with (out_dir / "trace.jsonl").open("w", encoding="utf-8") as tf:
        for line in trace_lines:
            tf.write(json.dumps(line, ensure_ascii=False) + "\n")
    (out_dir / "probes.json").write_text(json.dumps(probe_records, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    (out_dir / "mcp_audit.json").write_text(json.dumps(mcp_audit, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    (out_dir / "interactions.json").write_text(json.dumps(interactions_all, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    summary = {
        "slug": slug,
        "persona_name": profile["name"],
        "user_id": user_id,
        "events_executed": len(trace_lines),
        "probes_captured": len(probe_records),
        "http_5xx_count": http_5xx_count,
        "harness_timeouts": harness_timeouts,
        "ui_errors": ui_errors,
        "ui_error_count": len(ui_errors),
        "elapsed_seconds": time.monotonic() - started,
        "stub_models": bool(stub_model_url),
        "model_usage": model_usage,
        "mcp_audit": mcp_audit,
        "interactions": interactions_all,
        "approvals": approvals_log,
        "end_of_month_audit": month.get("end_of_month_audit") or {},
    }
    (out_dir / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(
        f"[{slug}] done: {len(trace_lines)} events, {len(probe_records)} probes, "
        f"{http_5xx_count} 5xx, {harness_timeouts} timeouts, {len(ui_errors)} ui_errors, "
        f"${model_usage['total_cost_usd']:.4f}, {summary['elapsed_seconds']:.1f}s",
        flush=True,
    )
    return summary


def _next_event_handles_interaction(events: list[dict[str, Any]], idx: int) -> bool:
    return idx + 1 < len(events) and events[idx + 1].get("kind") == "interaction_reply"


def _with_http_status(res: Any, st: int) -> Any:
    if isinstance(res, dict):
        out = dict(res)
        out["http_status"] = st
        if st >= 400 and "error" not in out:
            out["error"] = f"HTTP {st}: {res}"
        return out
    if st >= 400:
        return {"http_status": st, "error": f"HTTP {st}: {res}", "raw": res}
    return {"http_status": st, "raw": res}


def _compute_edited_memory_text(base_text: str, params: dict[str, Any]) -> str:
    txt = str(params.get("new_text") or params.get("text") or base_text)
    append_note = str(params.get("append_note") or "").strip()
    if append_note and append_note not in txt:
        txt = f"{txt.rstrip()}\n{append_note}" if txt.strip() else append_note
    return _clip_utf8(txt, 2000)


def _exec_memory_ui(base_url: str, user_id: str, action: str, params: dict[str, Any]) -> Any:
    if action == "create_core":
        val: dict[str, Any] = {
            "text": _clip_utf8(str(params["text"]), 2000),
            "about": params.get("about", "the user"),
            "section": params.get("section", "Context"),
        }
        if "pinned" in params:
            val["pinned"] = bool(params["pinned"])
        st, res = http_json(base_url, "POST", f"/users/{user_id}/v1/memory", {
            "request_id": str(uuid4()), "expected_revision": 0, "value": val,
        })
        return _with_http_status(res, st)

    if action == "consolidate":
        st, res = http_json(base_url, "POST", f"/users/{user_id}/v1/memory/actions", {
            "request_id": str(uuid4()), "expected_revision": 0, "value": {"action": "consolidate"},
        })
        return _with_http_status(res, st)

    if action == "search":
        q = quote(str(params.get("query", "")))
        layer = quote(str(params.get("layer", "")))
        st, res = http_json(base_url, "GET", f"/users/{user_id}/v1/memory/search?q={q}&layer={layer}&limit=20")
        return _with_http_status(res, st)

    if action in ("pin_matching", "unpin_matching", "demote_matching", "forget_matching", "edit_matching"):
        st, mem_list = http_json(base_url, "GET", f"/users/{user_id}/v1/memory")
        if st != 200 or not isinstance(mem_list, dict):
            return {"http_status": st, "error": f"list memory failed: {st}"}
        q = _match_query(params)
        if not q:
            return {"matched": False, "query": q, "error": f"{action} requires non-empty match query"}

        items = mem_list.get("items") or []
        core_matches = [
            m for m in items
            if q in str(m.get("text", "")).lower() or q in str(m.get("about", "")).lower()
        ]
        about_filter = str(params.get("about") or "").strip().lower()
        if len(core_matches) > 1 and about_filter and action != "edit_matching":
            narrowed = [m for m in core_matches if about_filter in str(m.get("about", "")).lower()]
            if narrowed:
                core_matches = narrowed

        if len(core_matches) > 1:
            return {
                "matched": False,
                "ambiguous": True,
                "count": len(core_matches),
                "query": q,
                "error": f"ambiguous Core Memory match ({len(core_matches)} items) for {q!r}",
            }

        if len(core_matches) == 1:
            target = core_matches[0]
            if action == "edit_matching":
                new_text = _compute_edited_memory_text(str(target.get("text", "")), params)
                val = {
                    "text": new_text,
                    "about": params.get("about", target.get("about", "the user")),
                    "section": params.get("section", target.get("section", "")),
                }
                if "pinned" in params:
                    val["pinned"] = bool(params["pinned"])
                elif target.get("pinned"):
                    val["pinned"] = True
                st_u, res = http_json(base_url, "PUT", f"/users/{user_id}/v1/memory/{target['id']}", {
                    "request_id": str(uuid4()), "expected_revision": int(target["revision"]), "value": val,
                })
                out = {"matched": True, "id": target["id"], "http_status": st_u, "response": res}
                if st_u >= 400:
                    out["error"] = f"PUT memory/{target['id']} failed ({st_u}): {res}"
                return out
            act_name = action.replace("_matching", "")
            st_a, res = http_json(base_url, "POST", f"/users/{user_id}/v1/memory/{target['id']}/actions", {
                "request_id": str(uuid4()), "expected_revision": int(target["revision"]), "value": {"action": act_name},
            })
            out = {"matched": True, "id": target["id"], "action": act_name, "http_status": st_a, "response": res}
            if st_a >= 400:
                out["error"] = f"POST memory/{target['id']}/actions ({act_name}) failed ({st_a}): {res}"
            return out

        # 0 Core Memory matches -> Daily Log fallback for forget_matching / pin_matching / edit_matching
        if action in ("forget_matching", "pin_matching", "edit_matching"):
            st_l, logs_resp = http_json(base_url, "GET", f"/users/{user_id}/v1/memory/logs?limit=100")
            if st_l != 200 or not isinstance(logs_resp, dict):
                return {"http_status": st_l, "error": f"list logs failed: {st_l}"}
            log_matches: list[tuple[dict[str, Any], dict[str, Any]]] = []
            for lg in logs_resp.get("items") or []:
                for entry in lg.get("entries") or []:
                    prov = str(entry.get("provenance", "")).lower()
                    if action in ("pin_matching", "edit_matching") and prov == "task_outcome" and not (params.get("new_text") or params.get("text")):
                        continue
                    if q in str(entry.get("text", "")).lower() or q in str(entry.get("about", "")).lower():
                        log_matches.append((lg, entry))
            if len(log_matches) > 1 and action == "forget_matching":
                forgotten: list[dict[str, Any]] = []
                # Group by date and re-fetch latest revision per date after each forget.
                seen_pairs = [(lg["date"], entry["id"]) for lg, entry in log_matches]
                for log_date, entry_id in seen_pairs:
                    st_cur, cur_logs = http_json(base_url, "GET", f"/users/{user_id}/v1/memory/logs?limit=100")
                    if st_cur != 200 or not isinstance(cur_logs, dict):
                        return {"matched": True, "source": "daily_log", "forgotten": forgotten, "http_status": st_cur, "error": f"refresh logs failed ({st_cur})"}
                    cur_lg = next((x for x in (cur_logs.get("items") or []) if x.get("date") == log_date), None)
                    if cur_lg is None:
                        continue
                    if not any(e.get("id") == entry_id for e in (cur_lg.get("entries") or [])):
                        continue
                    st_act, res = http_json(
                        base_url,
                        "POST",
                        f"/users/{user_id}/v1/memory/logs/{log_date}/{entry_id}/actions",
                        {"request_id": str(uuid4()), "expected_revision": int(cur_lg.get("revision", 0)), "value": {"action": "forget"}},
                    )
                    if st_act >= 400:
                        return {
                            "matched": True,
                            "source": "daily_log",
                            "date": log_date,
                            "entry_id": entry_id,
                            "forgotten": forgotten,
                            "http_status": st_act,
                            "error": f"POST log action failed ({st_act}): {res}",
                        }
                    forgotten.append({"date": log_date, "entry_id": entry_id, "http_status": st_act})
                return {"matched": True, "source": "daily_log", "forgotten_count": len(forgotten), "forgotten": forgotten, "http_status": 200}
            if len(log_matches) > 1:
                return {
                    "matched": False,
                    "ambiguous": True,
                    "count": len(log_matches),
                    "query": q,
                    "error": f"ambiguous Daily Log match ({len(log_matches)} entries) for {q!r}",
                }
            if len(log_matches) == 1:
                lg, entry = log_matches[0]
                if action == "forget_matching":
                    val = {"action": "forget"}
                elif action == "pin_matching":
                    val = {"action": "promote", "pinned": True}
                else:
                    updated_text = _compute_edited_memory_text(str(entry.get("text", "")), params)
                    val = {
                        "action": "promote",
                        "text": updated_text,
                        "about": params.get("about", entry.get("about", "the user")),
                        "section": params.get("section", entry.get("section", "Context")),
                    }
                    if "pinned" in params:
                        val["pinned"] = bool(params["pinned"])
                st_act, res = http_json(
                    base_url,
                    "POST",
                    f"/users/{user_id}/v1/memory/logs/{lg['date']}/{entry['id']}/actions",
                    {"request_id": str(uuid4()), "expected_revision": int(lg.get("revision", 0)), "value": val},
                )
                out = {
                    "matched": True,
                    "source": "daily_log",
                    "date": lg["date"],
                    "entry_id": entry["id"],
                    "http_status": st_act,
                    "response": res,
                }
                if st_act >= 400:
                    out["error"] = f"POST log action failed ({st_act}): {res}"
                return out

        return {"matched": False, "query": q, "error": f"no matching memory for {q!r}"}

    if action == "promote_log_matching":
        st, logs_resp = http_json(base_url, "GET", f"/users/{user_id}/v1/memory/logs?limit=100")
        if st != 200 or not isinstance(logs_resp, dict):
            return {"http_status": st, "error": f"list logs failed: {st}"}
        q = _match_query(params)
        if not q:
            return {"matched": False, "query": q, "error": "promote_log_matching requires non-empty query"}
        matches: list[tuple[dict[str, Any], dict[str, Any]]] = []
        for lg in logs_resp.get("items") or []:
            for entry in lg.get("entries") or []:
                if q in str(entry.get("text", "")).lower() or q in str(entry.get("about", "")).lower():
                    matches.append((lg, entry))
        if len(matches) != 1:
            return {
                "matched": False,
                "ambiguous": len(matches) > 1,
                "count": len(matches),
                "query": q,
                "error": f"expected 1 daily log match for {q!r}, found {len(matches)}",
            }
        lg, entry = matches[0]
        val: dict[str, Any] = {"action": "promote"}
        if params.get("text"):
            val["text"] = params["text"]
        if "pinned" in params:
            val["pinned"] = bool(params["pinned"])
        st_p, res = http_json(base_url, "POST", f"/users/{user_id}/v1/memory/logs/{lg['date']}/{entry['id']}/actions", {
            "request_id": str(uuid4()), "expected_revision": int(lg.get("revision", 0)), "value": val,
        })
        out = {"matched": True, "date": lg["date"], "entry_id": entry["id"], "http_status": st_p, "response": res}
        if st_p >= 400:
            out["error"] = f"promote log failed ({st_p}): {res}"
        return out

    if action == "dismiss_working_matching":
        st, wm = http_json(base_url, "GET", f"/users/{user_id}/v1/memory/working")
        if st != 200 or not isinstance(wm, dict):
            return {"http_status": st, "error": f"get working memory failed: {st}"}
        q = _match_query(params)
        if not q:
            return {"matched": False, "query": q, "error": "dismiss_working_matching requires non-empty query"}
        matches = [
            item for item in (wm.get("items") or [])
            if q in str(item.get("quote", "")).lower() or q in str(item.get("note", "")).lower()
        ]
        if len(matches) != 1:
            return {
                "matched": False,
                "ambiguous": len(matches) > 1,
                "count": len(matches),
                "query": q,
                "error": f"expected 1 working memory match for {q!r}, found {len(matches)}",
            }
        item = matches[0]
        st_d, res = http_json(base_url, "POST", f"/users/{user_id}/v1/memory/working/{item['id']}/actions", {
            "request_id": str(uuid4()), "expected_revision": int(wm.get("revision", 0)), "value": {"action": "dismiss"},
        })
        out = {"matched": True, "item_id": item["id"], "http_status": st_d, "response": res}
        if st_d >= 400:
            out["error"] = f"dismiss working memory failed ({st_d}): {res}"
        return out

    return {"skipped": f"unknown memory_ui action {action}"}


def _match_query(params: dict[str, Any]) -> str:
    for key in ("query", "match_title", "title_match", "match_text", "match", "title"):
        val = params.get(key)
        if isinstance(val, str) and val.strip():
            return val.strip().lower()
    return ""


_VALID_ACTION_CLASSES = {"read", "reversible_write", "sensitive_send", "unknown"}


def _normalize_action_classes(raw: Any, effect: str, tool_names: list[str] | None = None) -> list[str]:
    """Map persona shorthand action_classes onto valid contract Effect enum values."""
    if not raw:
        if effect == "allow" and tool_names and all(n in READ_ONLY_TOOL_NAMES for n in tool_names):
            return ["read", "unknown"]
        return ["reversible_write", "sensitive_send", "unknown"]
    alias_map = {
        "write": ["reversible_write"],
        "send": ["sensitive_send"],
        "external_write": ["reversible_write", "sensitive_send", "unknown"],
    }
    out: list[str] = []
    for item in raw:
        mapped = alias_map.get(str(item), [str(item)])
        for cls in mapped:
            if cls in _VALID_ACTION_CLASSES and cls not in out:
                out.append(cls)
    return out or ["reversible_write", "sensitive_send", "unknown"]


def _exec_permission_ui(base_url: str, user_id: str, tool_ids: dict[str, str], action: str, params: dict[str, Any]) -> Any:
    if action in ("create", "create_rule", "add_rule"):
        tool_names = params.get("tool_names") or []
        tids = [tool_ids[n] for n in tool_names if n in tool_ids]
        effect = params.get("effect", "allow")
        val: dict[str, Any] = {
            "effect": effect,
            "tool_ids": tids,
            "connection_ids": [],
            "resource_ids": [],
            "action_classes": _normalize_action_classes(params.get("action_classes"), effect, tool_names),
            "title": params.get("title", "Simulation rule"),
        }
        if params.get("recipients"):
            val["recipients"] = params["recipients"]
        if params.get("max_operations"):
            val["max_operations"] = int(params["max_operations"])
        st, res = http_json(base_url, "POST", f"/users/{user_id}/v1/permissions", {
            "request_id": str(uuid4()), "expected_revision": 0, "value": val,
        })
        return _with_http_status(res, st)
    if action in ("revoke", "revoke_rule"):
        st, rules = http_json(base_url, "GET", f"/users/{user_id}/v1/permissions")
        if st != 200 or not isinstance(rules, dict):
            return {"http_status": st, "error": f"list permissions failed ({st})"}
        q = _match_query(params)
        matches = [r for r in (rules.get("items") or []) if not q or q in str(r.get("title", "")).lower()]
        if len(matches) != 1:
            return {"matched": False, "match_count": len(matches), "query": q, "error": f"expected 1 permission rule for {q!r}, found {len(matches)}"}
        r = matches[0]
        st_rev, res = http_json(base_url, "POST", f"/users/{user_id}/v1/permissions/{r['id']}/actions", {
            "request_id": str(uuid4()), "expected_revision": int(r.get("revision", 0)), "value": {"action": "revoke"},
        })
        return _with_http_status(res, st_rev)
    return {"skipped": action}


_CRON_DOW_TO_BYDAY = {
    "0": "SU", "7": "SU", "SUN": "SU",
    "1": "MO", "MON": "MO",
    "2": "TU", "TUE": "TU",
    "3": "WE", "WED": "WE",
    "4": "TH", "THU": "TH",
    "5": "FR", "FRI": "FR",
    "6": "SA", "SAT": "SA",
}
_CRON_DOW_ORDER = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"]


def _parse_cron_num_field(field: str, min_v: int, max_v: int) -> list[int]:
    vals: list[int] = []
    for part in field.split(","):
        part = part.strip()
        if not part:
            raise ValueError(f"empty cron part in {field!r}")
        if "/" in part:
            base, step_s = part.split("/", 1)
            step = int(step_s)
            if step < 1:
                raise ValueError(f"invalid cron step {step}")
            if base == "*":
                lo, hi = min_v, max_v
            elif "-" in base:
                lo_s, hi_s = base.split("-", 1)
                lo, hi = int(lo_s), int(hi_s)
            else:
                lo, hi = int(base), max_v
            if lo < min_v or hi > max_v or lo > hi:
                raise ValueError(f"cron range out of bounds: {part!r}")
            vals.extend(range(lo, hi + 1, step))
        elif part == "*":
            vals.extend(range(min_v, max_v + 1))
        elif "-" in part:
            lo_s, hi_s = part.split("-", 1)
            lo, hi = int(lo_s), int(hi_s)
            if lo < min_v or hi > max_v or lo > hi:
                raise ValueError(f"cron range out of bounds: {part!r}")
            vals.extend(range(lo, hi + 1))
        else:
            n = int(part)
            if n < min_v or n > max_v:
                raise ValueError(f"cron value out of bounds: {n}")
            vals.append(n)
    # Deduplicate preserving order
    return list(dict.fromkeys(vals))


def _parse_cron_dow(field: str) -> list[str]:
    out: list[str] = []
    for part in field.split(","):
        part = part.strip().upper()
        if "-" in part:
            lo_s, hi_s = part.split("-", 1)
            lo_day = _CRON_DOW_TO_BYDAY.get(lo_s)
            hi_day = _CRON_DOW_TO_BYDAY.get(hi_s)
            if not lo_day or not hi_day:
                raise ValueError(f"invalid cron weekday range {part!r}")
            i0 = _CRON_DOW_ORDER.index(lo_day)
            i1 = _CRON_DOW_ORDER.index(hi_day)
            if i0 <= i1:
                out.extend(_CRON_DOW_ORDER[i0 : i1 + 1])
            else:
                out.extend(_CRON_DOW_ORDER[i0:] + _CRON_DOW_ORDER[: i1 + 1])
        else:
            day = _CRON_DOW_TO_BYDAY.get(part)
            if not day:
                raise ValueError(f"invalid cron weekday {part!r}")
            out.append(day)
    return list(dict.fromkeys(out))


def _build_automation_trigger(
    params: dict[str, Any],
    tz_name: str,
    default_date: str = "2027-01-05",
) -> dict[str, Any]:
    """Translate persona automation_ui params (`trigger` or `schedule`/`kind`) into Go scheduler.Trigger."""
    tz = params.get("timezone") or tz_name
    raw_trigger = params.get("trigger")
    if isinstance(raw_trigger, dict) and raw_trigger:
        trigger = dict(raw_trigger)
        if trigger.get("kind") == "monitor":
            trigger["kind"] = "recurring"
        if trigger.get("kind") != "once":
            trigger.setdefault("timezone", tz)
        return trigger

    top_kind = str(params.get("kind") or "recurring").strip().lower()
    if top_kind == "monitor":
        top_kind = "recurring"

    schedule = str(params.get("schedule") or "").strip()
    if not schedule:
        return {
            "kind": top_kind,
            "local_start": f"{default_date}T08:00:00",
            "rrule": "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR",
            "timezone": tz,
        }

    low = schedule.lower()
    if schedule.upper().startswith("FREQ="):
        return {
            "kind": top_kind,
            "local_start": f"{default_date}T08:00:00",
            "rrule": schedule,
            "timezone": tz,
        }
    if low in ("daily", "every day"):
        return {
            "kind": top_kind,
            "local_start": f"{default_date}T08:00:00",
            "rrule": "FREQ=DAILY",
            "timezone": tz,
        }
    if low == "every hour":
        hours = ",".join(str(h) for h in range(24))
        return {
            "kind": top_kind,
            "local_start": f"{default_date}T00:00:00",
            "rrule": f"FREQ=DAILY;BYHOUR={hours};BYMINUTE=0",
            "timezone": tz,
        }
    m_hours = re.match(r"^every\s+(\d+)\s+hours?$", low)
    if m_hours:
        step = int(m_hours.group(1))
        if step < 1 or step > 24 or (24 % step != 0):
            raise ValueError(f"unsupported hourly interval in schedule {schedule!r}")
        hours = ",".join(str(h) for h in range(0, 24, step))
        return {
            "kind": top_kind,
            "local_start": f"{default_date}T00:00:00",
            "rrule": f"FREQ=DAILY;BYHOUR={hours};BYMINUTE=0",
            "timezone": tz,
        }

    parts = schedule.split()
    if len(parts) == 5:
        min_f, hour_f, dom_f, mon_f, dow_f = parts
        mins = _parse_cron_num_field(min_f, 0, 59)
        hours = _parse_cron_num_field(hour_f, 0, 23)
        start_hh = hours[0]
        start_mm = mins[0]
        rrule_parts: list[str] = []

        if dow_f != "*":
            byday = _parse_cron_dow(dow_f)
            rrule_parts.append("FREQ=WEEKLY")
            rrule_parts.append(f"BYDAY={','.join(byday)}")
        elif dom_f != "*":
            dom_vals = _parse_cron_num_field(dom_f, 1, 31)
            rrule_parts.append("FREQ=MONTHLY")
            rrule_parts.append(f"BYMONTHDAY={','.join(str(d) for d in dom_vals)}")
        else:
            rrule_parts.append("FREQ=DAILY")

        if mon_f != "*":
            mon_vals = _parse_cron_num_field(mon_f, 1, 12)
            rrule_parts.append(f"BYMONTH={','.join(str(m) for m in mon_vals)}")

        if len(hours) > 1 or hour_f == "*" or "/" in hour_f or len(mins) > 1 or min_f == "*" or "/" in min_f:
            if len(hours) < 24:
                rrule_parts.append(f"BYHOUR={','.join(str(h) for h in hours)}")
            if len(mins) < 60:
                rrule_parts.append(f"BYMINUTE={','.join(str(m) for m in mins)}")

        return {
            "kind": top_kind,
            "local_start": f"{default_date}T{start_hh:02d}:{start_mm:02d}:00",
            "rrule": ";".join(rrule_parts),
            "timezone": tz,
        }

    raise ValueError(f"unsupported automation schedule: {schedule!r}")


def _exec_automation_ui(
    base_url: str,
    user_id: str,
    tz_name: str,
    action: str,
    params: dict[str, Any],
    default_date: str = "2027-01-05",
) -> Any:
    if action in ("create", "create_automation"):
        try:
            trigger = _build_automation_trigger(params, tz_name, default_date=default_date)
        except ValueError as exc:
            return {"error": str(exc)}
        val: dict[str, Any] = {
            "title": params.get("title", "Scheduled automation"),
            "instruction": params.get("instruction") or params.get("prompt") or "Run scheduled check.",
            "trigger": trigger,
            "connection_ids": list(params.get("connection_ids") or []),
            "budget_usd": float(params.get("budget_usd", 1.0)),
            "max_active_runs": int(params.get("max_active_runs", 1)),
            "missed_run": params.get("missed_run", "skip"),
            "execution_window_seconds": int(params.get("execution_window_seconds", 3600)),
            "destination_ids": list(params.get("destination_ids") or ["dest_in_app"]),
        }
        if params.get("goal_id"):
            val["goal_id"] = params["goal_id"]
        if "max_occurrences" in params:
            val["max_occurrences"] = int(params["max_occurrences"])
        if params.get("end_at"):
            val["end_at"] = params["end_at"]
        st, res = http_json(base_url, "POST", f"/users/{user_id}/v1/automations", {
            "request_id": str(uuid4()), "expected_revision": 0, "value": val,
        })
        return _with_http_status(res, st)
    if action in ("pause", "pause_automation", "resume", "resume_automation", "cancel", "run_now"):
        if action.startswith("pause"):
            api_act = "pause"
        elif action.startswith("resume"):
            api_act = "resume"
        else:
            api_act = action
        st, items = http_json(base_url, "GET", f"/users/{user_id}/v1/automations")
        if st != 200 or not isinstance(items, dict):
            return {"http_status": st, "error": f"list automations failed ({st})"}
        q = _match_query(params)
        matches = [
            a for a in (items.get("items") or [])
            if not q or q in str(a.get("title", "")).lower()
        ]
        if len(matches) != 1:
            return {
                "matched": False,
                "match_count": len(matches),
                "query": q,
                "error": f"expected 1 automation matching {q!r}, found {len(matches)}",
            }
        a = matches[0]
        st_act, res = http_json(base_url, "POST", f"/users/{user_id}/v1/automations/{a['id']}/actions", {
            "request_id": str(uuid4()), "expected_revision": int(a.get("revision", 0)), "value": {"action": api_act},
        })
        return _with_http_status(res, st_act)
    return {"skipped": action}


def _normalize_deadline(raw: Any) -> str | None:
    if not raw or not isinstance(raw, str):
        return None
    raw = raw.strip()
    if len(raw) == 10 and raw[4] == "-" and raw[7] == "-":
        return f"{raw}T23:59:59Z"
    return raw


def _normalize_completion_criteria(raw: Any) -> list[dict[str, str]]:
    """Normalize string or dict completion_criteria onto GoalEditable's {id, text} schema."""
    if not raw:
        return [{"id": "c1", "text": "Complete target milestone"}]
    out: list[dict[str, str]] = []
    for idx, item in enumerate(raw):
        if isinstance(item, str):
            txt = item.strip()
            if txt:
                out.append({"id": f"c{idx + 1}", "text": txt})
        elif isinstance(item, dict):
            cid = str(item.get("id") or f"c{idx + 1}").strip() or f"c{idx + 1}"
            txt = str(item.get("text") or "").strip()
            if txt:
                out.append({"id": cid, "text": txt})
    return out or [{"id": "c1", "text": "Complete target milestone"}]


def _exec_goal_ui(base_url: str, user_id: str, action: str, params: dict[str, Any]) -> Any:
    if action in ("create", "create_goal"):
        outcome = params.get("outcome") or params.get("description") or params.get("notes") or params.get("title", "Goal")
        val: dict[str, Any] = {
            "title": params.get("title", "Goal"),
            "outcome": str(outcome),
            "completion_criteria": _normalize_completion_criteria(params.get("completion_criteria")),
        }
        dl = _normalize_deadline(params.get("deadline") or params.get("target_date"))
        if dl:
            val["deadline"] = dl
        if params.get("follow_up") in ("", "none", "remind", "monitor"):
            val["follow_up"] = params["follow_up"]
        st, res = http_json(base_url, "POST", f"/users/{user_id}/v1/goals", {
            "request_id": str(uuid4()), "expected_revision": 0, "value": val,
        })
        return _with_http_status(res, st)
    if action in ("update", "update_goal", "complete"):
        st, goals = http_json(base_url, "GET", f"/users/{user_id}/v1/goals")
        if st != 200 or not isinstance(goals, dict):
            return {"http_status": st, "error": f"list goals failed ({st})"}
        q = _match_query(params)
        matches = [
            g for g in (goals.get("items") or [])
            if not q or q in str(g.get("title", "")).lower()
        ]
        if len(matches) != 1:
            return {
                "matched": False,
                "match_count": len(matches),
                "query": q,
                "error": f"expected 1 goal matching {q!r}, found {len(matches)}",
            }
        g = matches[0]
        if action == "complete" or params.get("status") == "completed":
            st_c, res = http_json(base_url, "POST", f"/users/{user_id}/v1/goals/{g['id']}/actions", {
                "request_id": str(uuid4()), "expected_revision": int(g.get("revision", 0)),
                "value": {"action": "complete"},
            })
            return _with_http_status(res, st_c)

        base_outcome = str(params.get("outcome") or params.get("description") or g.get("outcome", ""))
        note = str(params.get("progress") or params.get("progress_note") or params.get("notes") or "").strip()
        if note and note not in base_outcome:
            base_outcome = f"{base_outcome.rstrip()}\nProgress: {note}".strip()
        next_actions = params.get("next_actions")
        if isinstance(next_actions, list) and next_actions:
            na_str = "; ".join(str(x).strip() for x in next_actions if str(x).strip())
            if na_str and na_str not in base_outcome:
                base_outcome = f"{base_outcome.rstrip()}\nNext actions: {na_str}".strip()

        raw_criteria = params.get("completion_criteria") if "completion_criteria" in params else g.get("completion_criteria")
        val = {
            "title": str(params.get("title") or g.get("title", "")),
            "outcome": base_outcome,
            "completion_criteria": _normalize_completion_criteria(raw_criteria),
        }
        dl = _normalize_deadline(params.get("deadline") or params.get("target_date") or g.get("deadline"))
        if dl:
            val["deadline"] = dl
        follow_up = params.get("follow_up") if "follow_up" in params else g.get("follow_up")
        if follow_up in ("none", "remind", "monitor"):
            val["follow_up"] = follow_up
        st_u, res = http_json(base_url, "PUT", f"/users/{user_id}/v1/goals/{g['id']}", {
            "request_id": str(uuid4()), "expected_revision": int(g.get("revision", 0)), "value": val,
        })
        return _with_http_status(res, st_u)
    return {"skipped": action}


def _library_item_matches(item: dict[str, Any], q: str) -> bool:
    if not q:
        return True
    return any(
        q in str(item.get(field, "")).lower()
        for field in ("title", "summary", "preview_text")
    )


def _narrow_library_matches(items: list[dict[str, Any]], q: str) -> list[dict[str, Any]]:
    matches = [item for item in items if _library_item_matches(item, q)]
    if len(matches) > 1 and q:
        title_matches = [item for item in matches if q in str(item.get("title", "")).lower()]
        if title_matches:
            matches = title_matches
    return matches


def _exec_library_ui(
    base_url: str,
    user_id: str,
    action: str,
    params: dict[str, Any],
    drain_fn: Callable[[int], dict[str, Any]] | None = None,
) -> Any:
    if action == "search":
        q = quote(_match_query(params))
        st, res = http_json(base_url, "GET", f"/users/{user_id}/v1/search?q={q}")
        return _with_http_status(res, st)
    if action == "delete_matching":
        st, lib = http_json(base_url, "GET", f"/users/{user_id}/v1/library")
        if st != 200 or not isinstance(lib, dict):
            return {"http_status": st, "error": f"list library failed ({st})"}
        q = _match_query(params)
        scope = params.get("scope", "all_versions")
        matches = _narrow_library_matches(lib.get("items") or [], q)
        if len(matches) == 0:
            return {
                "matched": False,
                "match_count": 0,
                "query": q,
                "error": f"expected 1 library item matching {q!r}, found 0",
            }
        if len(matches) > 1:
            same_title = len({str(it.get("title", "")).strip().lower() for it in matches}) == 1
            if scope != "all_versions" or not same_title:
                return {
                    "matched": False,
                    "match_count": len(matches),
                    "query": q,
                    "error": f"expected 1 library item matching {q!r}, found {len(matches)}",
                }
            # Multiple versions of the same document: sort by version/updated_at desc and delete all_versions.
            matches.sort(key=lambda it: (int(it.get("version") or 0), str(it.get("updated_at") or "")), reverse=True)
        item = matches[0]
        st_d, res = http_json(base_url, "POST", f"/users/{user_id}/v1/library/{item['id']}/actions", {
            "request_id": str(uuid4()), "expected_revision": int(item.get("revision", 0)),
            "value": {"action": "delete", "scope": scope},
        })
        out = {"matched": True, "deleted": item["id"], "scope": scope, "http_status": st_d, "response": res}
        if st_d >= 400:
            out["error"] = f"delete library item failed ({st_d}): {res}"
        return out
    if action == "revise":
        st, lib = http_json(base_url, "GET", f"/users/{user_id}/v1/library")
        if st != 200 or not isinstance(lib, dict):
            return {"http_status": st, "error": f"list library failed ({st})"}
        q = _match_query(params)
        instruction = str(params.get("instruction") or "Revise this document.")
        matches = _narrow_library_matches(lib.get("items") or [], q)
        if len(matches) == 0:
            return {
                "matched": False,
                "match_count": 0,
                "query": q,
                "error": f"expected 1 library item matching {q!r}, found 0",
            }
        if len(matches) > 1:
            same_title = len({str(it.get("title", "")).strip().lower() for it in matches}) == 1
            if not same_title:
                return {
                    "matched": False,
                    "match_count": len(matches),
                    "query": q,
                    "error": f"expected 1 library item matching {q!r}, found {len(matches)}",
                }
            matches.sort(key=lambda it: (int(it.get("version") or 0), str(it.get("updated_at") or "")), reverse=True)
        item = matches[0]
        st_r, res = http_json(base_url, "POST", f"/users/{user_id}/v1/library/{item['id']}/actions", {
            "request_id": str(uuid4()), "expected_revision": int(item.get("revision", 0)),
            "value": {"action": "revise", "instruction": instruction},
        })
        out = {"matched": True, "revised": item["id"], "http_status": st_r, "response": res}
        if st_r >= 400:
            out["error"] = f"revise library item failed ({st_r}): {res}"
        return out
    if action == "update":
        st, lib = http_json(base_url, "GET", f"/users/{user_id}/v1/library")
        if st != 200 or not isinstance(lib, dict):
            return {"http_status": st, "error": f"list library failed ({st})"}
        q = _match_query(params)
        matches = _narrow_library_matches(lib.get("items") or [], q)
        if len(matches) == 0:
            return {
                "matched": False,
                "match_count": 0,
                "query": q,
                "error": f"expected 1 library item matching {q!r}, found 0",
            }
        if len(matches) > 1:
            same_title = len({str(it.get("title", "")).strip().lower() for it in matches}) == 1
            if not same_title:
                return {
                    "matched": False,
                    "match_count": len(matches),
                    "query": q,
                    "error": f"expected 1 library item matching {q!r}, found {len(matches)}",
                }
            matches.sort(key=lambda it: (int(it.get("version") or 0), str(it.get("updated_at") or "")), reverse=True)
        item = matches[0]
        val: dict[str, Any] = {"title": str(params.get("title") or item.get("title", ""))[:200]}
        if params.get("collection"):
            val["collection"] = str(params["collection"])
        st_u, res = http_json(base_url, "PUT", f"/users/{user_id}/v1/library/{item['id']}", {
            "request_id": str(uuid4()), "expected_revision": int(item.get("revision", 0)), "value": val,
        })
        out = {"matched": True, "updated": item["id"], "http_status": st_u, "response": res}
        if st_u >= 400:
            out["error"] = f"update library item failed ({st_u}): {res}"
        return out
    if action in ("create", "save_document"):
        title = str(params.get("title") or "Untitled Document").strip() or "Untitled Document"
        content = str(params.get("content") or "")
        safe_name = "".join(ch if ch.isalnum() or ch in ("-", "_") else "_" for ch in title.lower())[:48] + ".md"
        uid = str(uuid4())
        body = f"# {title}\n\n{content}\n".encode("utf-8")

        st_before, lib_before = http_json(base_url, "GET", f"/users/{user_id}/v1/library")
        if st_before != 200 or not isinstance(lib_before, dict):
            return {"http_status": st_before, "error": f"list library before create failed ({st_before})"}
        before_ids = {str(it.get("id")) for it in (lib_before.get("items") or []) if isinstance(it, dict) and it.get("id")}

        st_up, res_up = http_json(
            base_url, "POST", f"/users/{user_id}/v1/uploads?id={uid}&filename={quote(safe_name)}",
            raw_bytes=body, content_type="text/markdown",
        )
        if st_up != 200:
            return {"http_status": st_up, "error": f"upload {safe_name} failed ({st_up}): {res_up}", "response": res_up}
        att_id = str(res_up.get("id") or uid) if isinstance(res_up, dict) else uid

        turn_msg = (
            f"Save to Library: {title}\n\n"
            f"Create a saved Library document titled {title!r} containing the exact text below:\n\n{content}"
        )[:8192]
        st_turn, res_turn = http_json(base_url, "POST", f"/users/{user_id}/api/turn", {
            "id": str(uuid4()),
            "message": turn_msg,
            "attachments": [att_id],
        })
        if st_turn not in (200, 202):
            return {"http_status": st_turn, "uploaded_doc": att_id, "error": f"POST /api/turn failed ({st_turn}): {res_turn}"}

        if drain_fn is not None:
            drain_fn(180)

        st_after, lib_after = http_json(base_url, "GET", f"/users/{user_id}/v1/library")
        if st_after != 200 or not isinstance(lib_after, dict):
            return {"http_status": st_after, "uploaded_doc": att_id, "error": f"list library after create failed ({st_after})"}
        after_items = [it for it in (lib_after.get("items") or []) if isinstance(it, dict)]
        new_items = [it for it in after_items if str(it.get("id")) not in before_ids]
        created_item: dict[str, Any] | None = None
        if new_items:
            t_low = title.lower()
            created_item = next((it for it in new_items if t_low in str(it.get("title", "")).lower()), new_items[-1])
        if created_item is None:
            return {
                "matched": False,
                "created": False,
                "uploaded_doc": att_id,
                "filename": safe_name,
                "error": "library item was not created after turn drain",
            }

        target_title = title[:200]
        collection = str(params.get("collection") or "").strip()
        if str(created_item.get("title", "")) != target_title or collection:
            put_val: dict[str, Any] = {"title": target_title}
            if collection:
                put_val["collection"] = collection
            st_put, res_put = http_json(base_url, "PUT", f"/users/{user_id}/v1/library/{created_item['id']}", {
                "request_id": str(uuid4()),
                "expected_revision": int(created_item.get("revision", 0)),
                "value": put_val,
            })
            if st_put >= 400:
                return {
                    "matched": True,
                    "created": True,
                    "id": created_item["id"],
                    "uploaded_doc": att_id,
                    "http_status": st_put,
                    "error": f"PUT library/{created_item['id']} failed ({st_put}): {res_put}",
                }
            if isinstance(res_put, dict) and isinstance(res_put.get("resource"), dict):
                created_item = res_put["resource"]

        return {
            "matched": True,
            "created": True,
            "id": created_item["id"],
            "uploaded_doc": att_id,
            "filename": safe_name,
            "item": created_item,
        }
    return {"skipped": action}


def _collect_strings(obj: Any) -> list[str]:
    out: list[str] = []
    if isinstance(obj, str):
        out.append(obj)
    elif isinstance(obj, dict):
        for v in obj.values():
            out.extend(_collect_strings(v))
    elif isinstance(obj, list):
        for v in obj:
            out.extend(_collect_strings(v))
    return out


def _interaction_matches(item: dict[str, Any], match_tool: str, match_recipient: str) -> bool:
    raw_payload = item.get("payload")
    if isinstance(raw_payload, str):
        try:
            payload: dict[str, Any] = json.loads(raw_payload)
            if not isinstance(payload, dict):
                payload = {}
        except json.JSONDecodeError:
            payload = {}
    elif isinstance(raw_payload, dict):
        payload = raw_payload
    else:
        payload = {}

    if match_tool:
        mt = match_tool.strip().lower()
        mt_hyph = mt.replace("_", "-")
        mt_under = mt.replace("-", "_")
        candidates = [
            str(item.get("tool", "")),
            str(payload.get("tool", "")),
            str(payload.get("tool_name", "")),
            str(payload.get("tool_id", "")),
            str(payload.get("summary", "")),
            str(item.get("prompt", "")),
            str(item.get("task_title", "")),
        ]
        if not any(mt in c.lower() or mt_hyph in c.lower() or mt_under in c.lower() for c in candidates if c):
            return False

    if match_recipient:
        mr = match_recipient.strip().lower()
        recip_sources: list[str] = []
        for src in (
            item.get("recipient"),
            item.get("recipients"),
            payload.get("recipients"),
            payload.get("recipient"),
            payload.get("args"),
            payload.get("arguments"),
            payload.get("summary"),
            item.get("prompt"),
            item.get("task_title"),
        ):
            recip_sources.extend(_collect_strings(src))
        # Also parse JSON args embedded after " with " in summary/prompt (mcp.Adapter.Prepare)
        for text_blob in (str(payload.get("summary", "")), str(item.get("prompt", ""))):
            if " with " in text_blob:
                tail = text_blob.split(" with ", 1)[1].strip()
                try:
                    recip_sources.extend(_collect_strings(json.loads(tail)))
                except Exception:
                    pass
        if not any(mr in s.lower() for s in recip_sources if s):
            return False

    return True


def _exec_interaction_reply(base_url: str, user_id: str, action: str, params: dict[str, Any]) -> Any:
    st, inter_resp = http_json(base_url, "GET", f"/users/{user_id}/v1/interactions?status=pending")
    if st != 200 or not isinstance(inter_resp, dict):
        return {"http_status": st, "error": f"list interactions failed ({st}): {inter_resp}"}
    pending = inter_resp.get("items") or []

    match_tool = str(params.get("match_tool") or "")
    match_recipient = str(params.get("match_recipient") or "")
    matched = [it for it in pending if _interaction_matches(it, match_tool, match_recipient)]

    if not matched:
        if params.get("expect_stale") or params.get("allow_expired"):
            st_exp, exp_resp = http_json(base_url, "GET", f"/users/{user_id}/v1/interactions?status=expired")
            expired_items = (exp_resp.get("items") or []) if st_exp == 200 and isinstance(exp_resp, dict) else []
            exp_matched = [it for it in expired_items if _interaction_matches(it, match_tool, match_recipient)]
            if exp_matched:
                item = exp_matched[0]
                decision = str(params.get("decision") or action).lower()
                kind = "approve" if decision.startswith("approve") else ("decline" if decision.startswith("decline") else "answer")
                val: dict[str, Any] = {"kind": kind}
                if item.get("action_digest") and kind in ("approve", "decline"):
                    val["action_digest"] = item["action_digest"]
                st_post, res_post = http_json(base_url, "POST", f"/users/{user_id}/v1/interactions/{item['id']}/responses", {
                    "request_id": str(uuid4()), "expected_revision": int(item.get("revision", 0)), "value": val,
                })
                return {
                    "matched": True,
                    "stale_verified": st_post == 409,
                    "expired_id": item["id"],
                    "http_status": st_post,
                    "response": res_post,
                }
        return {
            "matched": False,
            "pending_count": len(pending),
            "match_tool": match_tool,
            "match_recipient": match_recipient,
            "error": "no matching pending interaction",
        }

    decision = str(params.get("decision") or action).lower()
    note = params.get("text") or params.get("note") or params.get("reason") or params.get("comment") or ""
    results = []
    for item in matched:
        item_kind = str(item.get("kind", "")).lower()
        if item_kind in ("question", "sign_in"):
            kind = "answer"
            ans_text = str(note).strip() or ("Yes, please proceed." if decision.startswith("approve") else "No, do not proceed.")
            val = {"kind": "answer", "text": ans_text}
        else:
            kind = "approve" if decision.startswith("approve") else ("decline" if decision.startswith("decline") else "answer")
            val = {"kind": kind}
            if note and kind in ("answer", "decline"):
                val["text"] = str(note)
            if item.get("action_digest") and kind in ("approve", "decline"):
                val["action_digest"] = item["action_digest"]
            if kind == "approve" and params.get("remember_rule"):
                val["remember_rule"] = True
        st_r, res = http_json(base_url, "POST", f"/users/{user_id}/v1/interactions/{item['id']}/responses", {
            "request_id": str(uuid4()), "expected_revision": int(item.get("revision", 0)), "value": val,
        })
        entry = {"id": item["id"], "kind": kind, "http_status": st_r, "response": res}
        if st_r >= 400:
            entry["error"] = f"POST interaction response failed ({st_r}): {res}"
        results.append(entry)
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
    parser.add_argument("--outer-model", default=None, help="Outer conversation model ID (overrides NORI_AGENT_MODEL, e.g. gpt-6.2-sol)")
    parser.add_argument("--outer-reasoning", choices=["low", "medium", "high"], default=None,
                        help="Outer conversation reasoning effort (overrides NORI_OUTER_REASONING_EFFORT)")
    parser.add_argument("--inner-model", default=None, help="Inner task/flush/dream model ID (overrides MODEL_NAME, e.g. gpt-6.1-terra)")
    parser.add_argument("--inner-reasoning", choices=["low", "medium", "high"], default=None,
                        help="Inner task loop default reasoning effort (overrides NORI_INNER_REASONING_EFFORT)")
    parser.add_argument("--compare", type=Path, help="Optional baseline scorecard.json to diff against")
    args = parser.parse_args()

    if args.outer_model:
        os.environ["NORI_AGENT_MODEL"] = args.outer_model
    if args.outer_reasoning:
        os.environ["NORI_OUTER_REASONING_EFFORT"] = args.outer_reasoning
    if args.inner_model:
        os.environ["MODEL_NAME"] = args.inner_model
    if args.inner_reasoning:
        os.environ["NORI_INNER_REASONING_EFFORT"] = args.inner_reasoning

    if args.concurrency > 1 and args.workbench_seed:
        raise SystemExit(
            "--concurrency > 1 cannot be combined with --workbench-seed (shared database and server). "
            "Use --concurrency 1 or omit --workbench-seed."
        )

    signal.signal(signal.SIGTERM, lambda _sig, _frame: (_ for _ in ()).throw(SystemExit(143)))

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
        supports_user_id = False if args.workbench_seed else _supports_user_id_flag(nori_sim_bin)

        mcp_server = SandboxMCPServer()
        mcp_server.start()
        stack.callback(mcp_server.stop)

        stub_url: str | None = None
        if args.stub_models:
            stub_srv = stack.enter_context(build_stub_model_server())
            threading.Thread(target=stub_srv.serve_forever, daemon=True).start()
            stack.callback(stub_srv.shutdown)
            stub_url = f"http://127.0.0.1:{stub_srv.server_port}"

        shared_pg_url: str | None = None
        if args.workbench_seed:
            shared_pg_url = os.environ.get("NORI_POSTGRES_URL") or dotenv.get("NORI_POSTGRES_URL") or "postgres://nori@127.0.0.1:15432/nori?sslmode=disable"

        def _exec(slug: str) -> dict[str, Any]:
            return run_single_persona(
                slug=slug,
                run_root=run_root,
                nori_sim_bin=None if args.workbench_seed else nori_sim_bin,
                nori_user_bin=nori_user_bin,
                pg_url=shared_pg_url,
                mcp_server=mcp_server,
                dotenv=dotenv,
                stub_model_url=stub_url,
                max_events=args.max_events,
                workbench_url=args.target_url if args.workbench_seed else None,
                keep_db=args.keep_db,
                supports_user_id=supports_user_id,
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
