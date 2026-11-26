#!/usr/bin/env python3
"""Deterministic local model stub server for free persona-sim smoke tests."""

from __future__ import annotations

import itertools
import json
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from uuid import uuid4


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


def _extract_stub_email(text: str) -> str | None:
    domain_pattern = re.compile(r"[A-Za-z0-9.-]+\.[A-Za-z]{2,}")
    for local in re.finditer(r"[A-Za-z0-9._%+-]+", text):
        separator = local.end()
        if not text.startswith("@", separator):
            continue
        domain = domain_pattern.match(text, separator + 1)
        if domain:
            return text[local.start():domain.end()]
    return None


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
                        recip = _extract_stub_email(prompt_text) or "colleague@example.com"
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
