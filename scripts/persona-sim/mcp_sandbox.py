#!/usr/bin/env python3
"""Stateful per-persona Streamable HTTP JSON-RPC 2.0 MCP sandbox server.

Hosts realistic isolated Email, Calendar, Docs, and Issue-Tracker worlds for each
simulated persona at /mcp/<slug>, plus control endpoints at /_world/<slug>/* so
runner.py can inject mid-month world events and audit every external write/send.
"""

from __future__ import annotations

import copy
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import urlparse

TOOLS = [
    {
        "name": "email_search",
        "title": "Search emails",
        "description": "Search the user's mailbox by keyword, sender, or folder.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "query": {"type": "string", "description": "Keyword to match in subject, sender, or body."},
                "folder": {"type": "string", "description": "Optional folder filter (e.g. inbox, sent)."},
                "limit": {"type": "integer", "minimum": 1, "maximum": 50},
            },
            "additionalProperties": False,
        },
        "annotations": {"readOnlyHint": True, "destructiveHint": False, "idempotentHint": True, "openWorldHint": False},
    },
    {
        "name": "email_read",
        "title": "Read an email",
        "description": "Read the full contents of one email by ID.",
        "inputSchema": {
            "type": "object",
            "properties": {"email_id": {"type": "string", "minLength": 1}},
            "required": ["email_id"],
            "additionalProperties": False,
        },
        "annotations": {"readOnlyHint": True, "destructiveHint": False, "idempotentHint": True, "openWorldHint": False},
    },
    {
        "name": "email_send",
        "title": "Send an email",
        "description": "Send an outbound email to one or more recipients.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "to": {"type": "array", "items": {"type": "string"}, "minItems": 1},
                "cc": {"type": "array", "items": {"type": "string"}},
                "subject": {"type": "string", "minLength": 1},
                "body": {"type": "string", "minLength": 1},
            },
            "required": ["to", "subject", "body"],
            "additionalProperties": False,
        },
        "annotations": {"readOnlyHint": False, "destructiveHint": False, "idempotentHint": False, "openWorldHint": True},
    },
    {
        "name": "calendar_list",
        "title": "List calendar events",
        "description": "List calendar events matching an optional date range or keyword.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "from_date": {"type": "string", "description": "Inclusive ISO date prefix YYYY-MM-DD."},
                "to_date": {"type": "string", "description": "Inclusive ISO date prefix YYYY-MM-DD."},
                "query": {"type": "string"},
            },
            "additionalProperties": False,
        },
        "annotations": {"readOnlyHint": True, "destructiveHint": False, "idempotentHint": True, "openWorldHint": False},
    },
    {
        "name": "calendar_create_event",
        "title": "Create calendar event",
        "description": "Create a new calendar event and invite attendees.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "title": {"type": "string", "minLength": 1},
                "start": {"type": "string", "minLength": 1},
                "end": {"type": "string", "minLength": 1},
                "location": {"type": "string"},
                "attendees": {"type": "array", "items": {"type": "string"}},
                "notes": {"type": "string"},
            },
            "required": ["title", "start", "end"],
            "additionalProperties": False,
        },
        "annotations": {"readOnlyHint": False, "destructiveHint": False, "idempotentHint": False, "openWorldHint": True},
    },
    {
        "name": "doc_list",
        "title": "List workspace docs",
        "description": "List notes and documents in the user's workspace.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "query": {"type": "string"},
                "tag": {"type": "string"},
            },
            "additionalProperties": False,
        },
        "annotations": {"readOnlyHint": True, "destructiveHint": False, "idempotentHint": True, "openWorldHint": False},
    },
    {
        "name": "doc_read",
        "title": "Read a workspace doc",
        "description": "Read the full text of a workspace document by ID.",
        "inputSchema": {
            "type": "object",
            "properties": {"doc_id": {"type": "string", "minLength": 1}},
            "required": ["doc_id"],
            "additionalProperties": False,
        },
        "annotations": {"readOnlyHint": True, "destructiveHint": False, "idempotentHint": True, "openWorldHint": False},
    },
    {
        "name": "doc_write",
        "title": "Create or update workspace doc",
        "description": "Write or update a document in the user's workspace.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "doc_id": {"type": "string"},
                "title": {"type": "string", "minLength": 1},
                "content": {"type": "string", "minLength": 1},
                "tags": {"type": "array", "items": {"type": "string"}},
            },
            "required": ["title", "content"],
            "additionalProperties": False,
        },
        "annotations": {"readOnlyHint": False, "destructiveHint": False, "idempotentHint": False, "openWorldHint": False},
    },
    {
        "name": "issue_list",
        "title": "List tracked issues",
        "description": "List issues, incidents, or project tracker items.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "status": {"type": "string"},
                "query": {"type": "string"},
            },
            "additionalProperties": False,
        },
        "annotations": {"readOnlyHint": True, "destructiveHint": False, "idempotentHint": True, "openWorldHint": False},
    },
    {
        "name": "issue_update",
        "title": "Update a tracked issue",
        "description": "Update an issue's status, assignee, or notes.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "issue_id": {"type": "string", "minLength": 1},
                "status": {"type": "string"},
                "assignee": {"type": "string"},
                "notes": {"type": "string"},
            },
            "required": ["issue_id"],
            "additionalProperties": False,
        },
        "annotations": {"readOnlyHint": False, "destructiveHint": False, "idempotentHint": False, "openWorldHint": False},
    },
]

READ_ONLY_TOOL_NAMES = {t["name"] for t in TOOLS if t["annotations"].get("readOnlyHint")}


class PersonaWorld:
    def __init__(self, slug: str, seed: dict[str, Any]) -> None:
        self.slug = slug
        self.lock = threading.Lock()
        self.server_name = seed.get("server_name") or f"{slug} Workspace"
        self.emails: list[dict[str, Any]] = copy.deepcopy(seed.get("emails") or [])
        self.calendar_events: list[dict[str, Any]] = copy.deepcopy(seed.get("calendar_events") or [])
        self.docs: list[dict[str, Any]] = copy.deepcopy(seed.get("docs") or [])
        self.issues: list[dict[str, Any]] = copy.deepcopy(seed.get("issues") or [])
        self.call_log: list[dict[str, Any]] = []
        self.write_log: list[dict[str, Any]] = []
        self._seq = 100

    def _next_id(self, prefix: str) -> str:
        self._seq += 1
        return f"{prefix}_{self._seq}"

    def apply_world_event(self, action: str, params: dict[str, Any]) -> dict[str, Any]:
        with self.lock:
            if action in ("add_email", "inject_email"):
                item = copy.deepcopy(params)
                if "email" in item and isinstance(item["email"], dict):
                    item = copy.deepcopy(item["email"])
                item.setdefault("id", self._next_id("mail"))
                item.setdefault("folder", "inbox")
                if isinstance(item.get("to"), str):
                    item["to"] = [item["to"]]
                self.emails.append(item)
                return {"added": "email", "id": item["id"]}
            if action == "add_calendar_event":
                item = copy.deepcopy(params)
                item.setdefault("id", self._next_id("cal"))
                self.calendar_events.append(item)
                return {"added": "calendar_event", "id": item["id"]}
            if action == "add_doc":
                item = copy.deepcopy(params)
                item.setdefault("id", self._next_id("doc"))
                self.docs.append(item)
                return {"added": "doc", "id": item["id"]}
            if action == "update_issue":
                issue_id = params.get("id") or params.get("issue_id")
                for existing in self.issues:
                    if existing.get("id") == issue_id:
                        for k, v in params.items():
                            if k == "append_note":
                                existing["notes"] = (existing.get("notes", "") + "\n" + str(v)).strip()
                            elif k != "issue_id":
                                existing[k] = v
                        return {"updated": "issue", "id": issue_id}
                item = copy.deepcopy(params)
                item["id"] = issue_id or self._next_id("iss")
                item.setdefault("status", "open")
                self.issues.append(item)
                return {"added": "issue", "id": item["id"]}
            raise ValueError(f"unknown mcp_world_event action: {action}")

    def call_tool(self, name: str, args: dict[str, Any]) -> dict[str, Any]:
        with self.lock:
            ts = time.time()
            self.call_log.append({"ts": ts, "tool": name, "arguments": copy.deepcopy(args)})

            if name == "email_search":
                q = str(args.get("query") or "").lower().strip()
                folder = str(args.get("folder") or "").lower().strip()
                limit = int(args.get("limit") or 20)
                matches = []
                for m in reversed(self.emails):
                    if folder and str(m.get("folder", "inbox")).lower() != folder:
                        continue
                    hay = " ".join([
                        str(m.get("from", "")),
                        " ".join(m.get("to") or []),
                        str(m.get("subject", "")),
                        str(m.get("body", "")),
                    ]).lower()
                    if not q or q in hay:
                        matches.append({
                            "id": m["id"],
                            "from": m.get("from", ""),
                            "to": m.get("to", []),
                            "subject": m.get("subject", ""),
                            "date": m.get("date", ""),
                            "folder": m.get("folder", "inbox"),
                            "snippet": str(m.get("body", ""))[:180],
                        })
                return {"emails": matches[:limit], "total": len(matches)}

            if name == "email_read":
                eid = str(args.get("email_id") or "")
                for m in self.emails:
                    if m.get("id") == eid:
                        return {"email": copy.deepcopy(m)}
                return {"error": f"email {eid} not found"}

            if name == "email_send":
                receipt = self._next_id("sent")
                record = {
                    "id": receipt,
                    "from": "me",
                    "to": list(args.get("to") or []),
                    "cc": list(args.get("cc") or []),
                    "subject": str(args.get("subject") or ""),
                    "body": str(args.get("body") or ""),
                    "date": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                    "folder": "sent",
                }
                self.emails.append(record)
                self.write_log.append({"ts": ts, "tool": name, "receipt_id": receipt, "arguments": copy.deepcopy(args)})
                return {"status": "sent", "receipt_id": receipt, "to": record["to"], "subject": record["subject"]}

            if name == "calendar_list":
                from_d = str(args.get("from_date") or "")
                to_d = str(args.get("to_date") or "")
                q = str(args.get("query") or "").lower().strip()
                out = []
                for ev in self.calendar_events:
                    start_day = str(ev.get("start", ""))[:10]
                    if from_d and start_day < from_d:
                        continue
                    if to_d and start_day > to_d:
                        continue
                    if q:
                        hay = f"{ev.get('title','')} {ev.get('location','')} {ev.get('notes','')}".lower()
                        if q not in hay:
                            continue
                    out.append(copy.deepcopy(ev))
                return {"events": out, "total": len(out)}

            if name == "calendar_create_event":
                ev_id = self._next_id("cal")
                ev = {
                    "id": ev_id,
                    "title": str(args.get("title") or ""),
                    "start": str(args.get("start") or ""),
                    "end": str(args.get("end") or ""),
                    "location": str(args.get("location") or ""),
                    "attendees": list(args.get("attendees") or []),
                    "notes": str(args.get("notes") or ""),
                }
                self.calendar_events.append(ev)
                self.write_log.append({"ts": ts, "tool": name, "receipt_id": ev_id, "arguments": copy.deepcopy(args)})
                return {"status": "created", "event": ev}

            if name == "doc_list":
                q = str(args.get("query") or "").lower().strip()
                tag = str(args.get("tag") or "").lower().strip()
                out = []
                for d in self.docs:
                    tags = [str(t).lower() for t in (d.get("tags") or [])]
                    if tag and tag not in tags:
                        continue
                    hay = f"{d.get('title','')} {d.get('content','')} {' '.join(tags)}".lower()
                    if q and q not in hay:
                        continue
                    out.append({
                        "id": d["id"],
                        "title": d.get("title", ""),
                        "tags": d.get("tags", []),
                        "updated_at": d.get("updated_at", ""),
                        "preview": str(d.get("content", ""))[:200],
                    })
                return {"docs": out, "total": len(out)}

            if name == "doc_read":
                did = str(args.get("doc_id") or "")
                for d in self.docs:
                    if d.get("id") == did:
                        return {"doc": copy.deepcopy(d)}
                return {"error": f"doc {did} not found"}

            if name == "doc_write":
                did = str(args.get("doc_id") or "").strip() or self._next_id("doc")
                updated = None
                for d in self.docs:
                    if d.get("id") == did:
                        d["title"] = str(args.get("title") or d.get("title", ""))
                        d["content"] = str(args.get("content") or "")
                        if "tags" in args:
                            d["tags"] = list(args["tags"])
                        updated = copy.deepcopy(d)
                        break
                if updated is None:
                    updated = {
                        "id": did,
                        "title": str(args.get("title") or ""),
                        "content": str(args.get("content") or ""),
                        "tags": list(args.get("tags") or []),
                    }
                    self.docs.append(updated)
                self.write_log.append({"ts": ts, "tool": name, "receipt_id": did, "arguments": copy.deepcopy(args)})
                return {"status": "saved", "doc": updated}

            if name == "issue_list":
                status = str(args.get("status") or "").lower().strip()
                q = str(args.get("query") or "").lower().strip()
                out = []
                for iss in self.issues:
                    if status and str(iss.get("status", "")).lower() != status:
                        continue
                    hay = f"{iss.get('id','')} {iss.get('title','')} {iss.get('notes','')} {iss.get('assignee','')}".lower()
                    if q and q not in hay:
                        continue
                    out.append(copy.deepcopy(iss))
                return {"issues": out, "total": len(out)}

            if name == "issue_update":
                iid = str(args.get("issue_id") or "")
                for iss in self.issues:
                    if iss.get("id") == iid:
                        for key in ("status", "assignee", "notes"):
                            if key in args and args[key] is not None:
                                iss[key] = args[key]
                        self.write_log.append({"ts": ts, "tool": name, "receipt_id": iid, "arguments": copy.deepcopy(args)})
                        return {"status": "updated", "issue": copy.deepcopy(iss)}
                return {"error": f"issue {iid} not found"}

            raise KeyError(f"Unknown tool: {name}")

    def audit(self) -> dict[str, Any]:
        with self.lock:
            return {
                "slug": self.slug,
                "call_count": len(self.call_log),
                "write_count": len(self.write_log),
                "calls": copy.deepcopy(self.call_log),
                "writes": copy.deepcopy(self.write_log),
            }


class SandboxMCPServer:
    """Multi-persona in-process HTTP MCP sandbox server."""

    def __init__(self, host: str = "127.0.0.1", port: int = 0) -> None:
        self.worlds: dict[str, PersonaWorld] = {}
        self._lock = threading.Lock()
        parent = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format: str, *args: Any) -> None:
                pass

            def _read_json(self) -> Any:
                length = int(self.headers.get("Content-Length", "0"))
                raw = self.rfile.read(length) if length > 0 else b"{}"
                return json.loads(raw)

            def _send_json(self, payload: Any, status: int = 200) -> None:
                body = json.dumps(payload).encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self) -> None:
                path = urlparse(self.path).path
                if path.startswith("/mcp/"):
                    self.send_response(405)
                    self.send_header("Allow", "POST, DELETE")
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                if path.startswith("/_world/") and path.endswith("/audit"):
                    slug = path[len("/_world/") : -len("/audit")].strip("/")
                    world = parent.get_world(slug)
                    if world is None:
                        self._send_json({"error": "unknown persona"}, 404)
                        return
                    self._send_json(world.audit())
                    return
                self._send_json({"error": "not found"}, 404)

            def do_DELETE(self) -> None:
                path = urlparse(self.path).path
                if path.startswith("/mcp/"):
                    self.send_response(204)
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                self.send_error(404)

            def do_POST(self) -> None:
                path = urlparse(self.path).path
                if path.startswith("/_world/") and path.endswith("/event"):
                    slug = path[len("/_world/") : -len("/event")].strip("/")
                    world = parent.get_world(slug)
                    if world is None:
                        self._send_json({"error": "unknown persona"}, 404)
                        return
                    try:
                        body = self._read_json()
                        res = world.apply_world_event(body["action"], body.get("params") or {})
                        self._send_json(res)
                    except Exception as exc:
                        self._send_json({"error": str(exc)}, 400)
                    return

                if not path.startswith("/mcp/"):
                    self.send_error(404)
                    return
                slug = path[len("/mcp/") :].strip("/")
                world = parent.get_world(slug)
                if world is None:
                    self._send_json({"jsonrpc": "2.0", "id": None, "error": {"code": -32004, "message": f"Unknown persona {slug}"}}, 404)
                    return

                try:
                    message = self._read_json()
                except json.JSONDecodeError:
                    self._send_json({"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "Parse error"}}, 400)
                    return

                if "id" not in message:
                    self.send_response(202)
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return

                req_id = message["id"]
                method = message.get("method", "")
                params = message.get("params") or {}

                if method == "initialize":
                    version = params.get("protocolVersion") or "2025-06-18"
                    self._send_json({
                        "jsonrpc": "2.0",
                        "id": req_id,
                        "result": {
                            "protocolVersion": version,
                            "capabilities": {"tools": {}},
                            "serverInfo": {"name": f"nori-sandbox-{slug}", "version": "1.0.0"},
                        },
                    })
                elif method == "ping":
                    self._send_json({"jsonrpc": "2.0", "id": req_id, "result": {}})
                elif method == "tools/list":
                    self._send_json({"jsonrpc": "2.0", "id": req_id, "result": {"tools": TOOLS}})
                elif method == "tools/call":
                    tool_name = params.get("name", "")
                    tool_args = params.get("arguments") or {}
                    try:
                        structured = world.call_tool(tool_name, tool_args)
                        is_err = isinstance(structured, dict) and "error" in structured
                        self._send_json({
                            "jsonrpc": "2.0",
                            "id": req_id,
                            "result": {
                                "content": [{"type": "text", "text": json.dumps(structured, ensure_ascii=False)}],
                                "structuredContent": structured,
                                "isError": is_err,
                            },
                        })
                    except KeyError:
                        self._send_json({"jsonrpc": "2.0", "id": req_id, "error": {"code": -32602, "message": f"Unknown tool: {tool_name}"}})
                    except Exception as exc:
                        self._send_json({"jsonrpc": "2.0", "id": req_id, "error": {"code": -32603, "message": str(exc)}})
                else:
                    self._send_json({"jsonrpc": "2.0", "id": req_id, "error": {"code": -32601, "message": f"Method not found: {method}"}})

        self._httpd = ThreadingHTTPServer((host, port), Handler)
        self._thread = threading.Thread(target=self._httpd.serve_forever, daemon=True)

    @property
    def port(self) -> int:
        return int(self._httpd.server_port)

    def start(self) -> None:
        self._thread.start()

    def stop(self) -> None:
        self._httpd.shutdown()
        self._httpd.server_close()

    def register_persona(self, slug: str, seed: dict[str, Any]) -> str:
        with self._lock:
            self.worlds[slug] = PersonaWorld(slug, seed)
        return f"http://127.0.0.1:{self.port}/mcp/{slug}"

    def get_world(self, slug: str) -> PersonaWorld | None:
        with self._lock:
            return self.worlds.get(slug)
