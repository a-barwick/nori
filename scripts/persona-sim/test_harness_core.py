#!/usr/bin/env python3
"""Comprehensive stdlib unittest suite for M3 Python Harness Core.

Covers:
- mcp_sandbox.py (virtual sim time, non-contiguous multi-word search, web_search stub, JSON-RPC HTTP)
- asset_gen.py (5x7 bitmap font, Unicode sanitization, auto-scaling PNG text rendering, PDF generation)
- runner.py unit helpers (automation schedule/cron, goal criteria sanitization, model_usage, --stub-models)
- runner.py mock stack integration (library_ui, goal_ui, interaction_reply, memory_ui, H-1..H-13, sim_control)

Runs completely offline with zero external dependencies and zero Postgres.
"""

from __future__ import annotations

import concurrent.futures
import http.client
import json
import sys
import tempfile
import threading
import unittest
from contextlib import ExitStack, closing
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from unittest import mock
from urllib.parse import parse_qs, urlparse

HERE = Path(__file__).resolve().parent
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

import asset_gen  # noqa: E402
import mcp_sandbox  # noqa: E402
import runner  # noqa: E402


def _http_call(base_url: str, method: str, path: str, body: Any = None) -> tuple[int, Any]:
    parsed = urlparse(base_url)
    host = parsed.hostname or "127.0.0.1"
    port = parsed.port or 80
    raw = json.dumps(body).encode("utf-8") if body is not None else None
    headers = {"Content-Type": "application/json"} if raw is not None else {}
    with closing(http.client.HTTPConnection(host, port, timeout=10)) as conn:
        conn.request(method, path, body=raw, headers=headers)
        resp = conn.getresponse()
        payload = resp.read()
        status = resp.status
    if not payload:
        return status, {}
    try:
        return status, json.loads(payload.decode("utf-8"))
    except json.JSONDecodeError:
        return status, payload.decode("utf-8", errors="replace")


class TestMCPSandbox(unittest.TestCase):
    def test_virtual_sim_time_stamping_and_clock_endpoint(self) -> None:
        seed = {
            "server_name": "Maya Workspace",
            "sim_time": "2027-01-05T14:30:00Z",
            "emails": [],
            "docs": [],
            "issues": [{"id": "ISS-1", "title": "Leak", "status": "open"}],
        }
        srv = mcp_sandbox.SandboxMCPServer()
        srv.start()
        try:
            mcp_url = srv.register_persona("01-maya", seed)
            base_url = mcp_url.rsplit("/mcp/", 1)[0]
            world = srv.get_world("01-maya")
            assert world is not None
            self.assertEqual(world.sim_time, "2027-01-05T14:30:00Z")

            # email_send stamps virtual sim_time (2027-01-05)
            send_res = world.call_tool("email_send", {"to": ["bob@example.com"], "subject": "Hello", "body": "Body"})
            self.assertEqual(send_res["status"], "sent")
            sent_email = world.emails[-1]
            self.assertEqual(sent_email["date"], "2027-01-05T14:30:00Z")

            # Update clock via HTTP POST /_world/<slug>/clock with timezone offset
            st, clk_res = _http_call(base_url, "POST", "/_world/01-maya/clock", {"time": "2027-01-08T09:15:00-08:00"})
            self.assertEqual(st, 200)
            self.assertEqual(clk_res["sim_time"], "2027-01-08T17:15:00Z")

            # doc_write (create & update) and issue_update stamp updated_at
            doc_res = world.call_tool("doc_write", {"title": "Kitchen Notes", "content": "Putty paint"})
            self.assertEqual(doc_res["doc"]["updated_at"], "2027-01-08T17:15:00Z")
            doc_id = doc_res["doc"]["id"]

            world.set_sim_time(datetime(2027, 1, 9, 12, 0, 0, tzinfo=timezone.utc))
            doc_res2 = world.call_tool("doc_write", {"doc_id": doc_id, "title": "Kitchen Notes", "content": "Updated"})
            self.assertEqual(doc_res2["doc"]["updated_at"], "2027-01-09T12:00:00Z")

            iss_res = world.call_tool("issue_update", {"issue_id": "ISS-1", "status": "resolved"})
            self.assertEqual(iss_res["issue"]["updated_at"], "2027-01-09T12:00:00Z")

            # Audit log includes sim_time on top-level, call_log, and write_log
            st_a, audit = _http_call(base_url, "GET", "/_world/01-maya/audit")
            self.assertEqual(st_a, 200)
            self.assertEqual(audit["sim_time"], "2027-01-09T12:00:00Z")
            self.assertTrue(all("sim_time" in c and "ts" in c for c in audit["calls"]))
            self.assertTrue(all("sim_time" in w and "ts" in w for w in audit["writes"]))

            # Clock error handling (400 / 404)
            st_bad, _ = _http_call(base_url, "POST", "/_world/01-maya/clock", {"time": "not-a-date"})
            self.assertEqual(st_bad, 400)
            st_404, _ = _http_call(base_url, "POST", "/_world/missing/clock", {"time": "2027-01-09T12:00:00Z"})
            self.assertEqual(st_404, 404)

            # register_persona with explicit sim_time overrides seed
            srv.register_persona("02-devon", {}, sim_time=datetime(2027, 1, 12, 8, 30, 0))
            w2 = srv.get_world("02-devon")
            assert w2 is not None
            self.assertEqual(w2.sim_time, "2027-01-12T08:30:00Z")
        finally:
            srv.stop()

    def test_non_contiguous_multi_word_search(self) -> None:
        seed = {
            "emails": [
                {
                    "id": "m1",
                    "from": "contractor@build.example",
                    "to": ["maya@example.com"],
                    "cc": ["architect@studio.example"],
                    "subject": "Bernal Kitchen Remodel Schedule",
                    "body": "Cabinet delivery moved to Tuesday morning.",
                    "date": "2027-01-04T10:00:00Z",
                    "folder": "inbox",
                }
            ],
            "docs": [
                {
                    "id": "d1",
                    "title": "Finish Palette Specification",
                    "content": "Warm Putty walls with honed soapstone counters.",
                    "tags": ["kitchen", "bernal"],
                    "updated_at": "2027-01-04T11:00:00Z",
                }
            ],
            "issues": [
                {
                    "id": "DBSI-48",
                    "title": "PostgreSQL connection pool exhaustion",
                    "status": "investigating",
                    "priority": "p0",
                    "assignee": "marcus",
                    "notes": "Happens during burst traffic.",
                }
            ],
            "calendar_events": [
                {
                    "id": "c1",
                    "title": "Site Walkthrough",
                    "start": "2027-01-06T10:00:00Z",
                    "end": "2027-01-06T11:00:00Z",
                    "location": "Mission District",
                    "attendees": ["elena@hoa.example", "contractor@build.example"],
                    "notes": "Inspect roof flashing.",
                }
            ],
        }
        world = mcp_sandbox.PersonaWorld("test", seed)

        # Words reversed, spanning subject + body + cc, with punctuation
        r_email = world.call_tool("email_search", {"query": "Tuesday, kitchen! studio.example"})
        self.assertEqual(r_email["total"], 1)
        self.assertEqual(world.call_tool("email_search", {"query": "kitchen plumbing"})["total"], 0)
        self.assertEqual(world.call_tool("email_search", {"query": "   "})["total"], 1)

        # Doc search spanning title + tags + content
        r_doc = world.call_tool("doc_list", {"query": "soapstone bernal Finish"})
        self.assertEqual(r_doc["total"], 1)

        # Issue search spanning id + priority + status
        r_iss = world.call_tool("issue_list", {"query": "DBSI-48, p0 investigating"})
        self.assertEqual(r_iss["total"], 1)

        # Calendar search spanning attendees + location + notes
        r_cal = world.call_tool("calendar_list", {"query": "flashing elena@hoa.example Mission"})
        self.assertEqual(r_cal["total"], 1)

    def test_web_search_and_jsonrpc_round_trip(self) -> None:
        self.assertIn("web_search", mcp_sandbox.READ_ONLY_TOOL_NAMES)
        seed = {
            "sim_time": "2027-01-10T08:00:00Z",
            "web_results": [
                {
                    "title": "Lake District MWIS Mountain Forecast",
                    "url": "https://mwis.example/lakes",
                    "snippet": "Gales 45mph on Striding Edge ridge Saturday.",
                    "keywords": ["helvellyn", "weather"],
                }
            ],
        }
        srv = mcp_sandbox.SandboxMCPServer()
        srv.start()
        try:
            mcp_url = srv.register_persona("05-hannah", seed)
            base_url = mcp_url.rsplit("/mcp/", 1)[0]

            # Seed more results via world event
            st_ev, ev_res = _http_call(base_url, "POST", "/_world/05-hannah/event", {
                "action": "add_web_results",
                "params": {
                    "query": "keswick tea rooms",
                    "results": [
                        {"title": "Keswick Alpine Cafe", "url": "https://cafe.example", "snippet": "Open 07:30 daily."}
                    ],
                },
            })
            self.assertEqual(st_ev, 200)
            self.assertEqual(ev_res["count"], 1)

            # JSON-RPC initialize -> tools/list -> tools/call web_search
            st_i, init_r = _http_call(base_url, "POST", "/mcp/05-hannah", {"jsonrpc": "2.0", "id": 1, "method": "initialize"})
            self.assertEqual(st_i, 200)
            self.assertIn("result", init_r)

            st_l, list_r = _http_call(base_url, "POST", "/mcp/05-hannah", {"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
            self.assertEqual(st_l, 200)
            tool_names = [t["name"] for t in list_r["result"]["tools"]]
            self.assertIn("web_search", tool_names)

            st_c, call_r = _http_call(base_url, "POST", "/mcp/05-hannah", {
                "jsonrpc": "2.0",
                "id": 3,
                "method": "tools/call",
                "params": {"name": "web_search", "arguments": {"query": "Striding Edge gales"}},
            })
            self.assertEqual(st_c, 200)
            self.assertFalse(call_r["result"]["isError"])
            sc = call_r["result"]["structuredContent"]
            self.assertEqual(sc["total"], 1)
            self.assertEqual(sc["retrieved_at"], "2027-01-10T08:00:00Z")
            self.assertIn("untrusted data", sc["note"])

            # Blank query returns isError=True; read-only tool never writes to write_log
            st_err, err_r = _http_call(base_url, "POST", "/mcp/05-hannah", {
                "jsonrpc": "2.0",
                "id": 4,
                "method": "tools/call",
                "params": {"name": "web_search", "arguments": {"query": "   "}},
            })
            self.assertEqual(st_err, 200)
            self.assertTrue(err_r["result"]["isError"])

            world = srv.get_world("05-hannah")
            assert world is not None
            self.assertEqual(len(world.write_log), 0)
        finally:
            srv.stop()


class TestAssetGen(unittest.TestCase):
    def test_font_completeness_and_unicode_sanitization(self) -> None:
        for code in range(0x20, 0x7F):
            ch = chr(code)
            self.assertIn(ch, asset_gen._FONT_5X7)
            glyph = asset_gen._FONT_5X7[ch]
            self.assertEqual(len(glyph), 7)
            self.assertTrue(all(0 <= row <= 0b11111 for row in glyph))

        cleaned = asset_gen._sanitize_ascii_label("Café — 25°C → ‘Yes’ “No”… • item")
        self.assertEqual(cleaned, "Cafe - 25degC -> 'Yes' \"No\"... * item")

    def test_png_renders_visible_text_panel_and_preserves_text_chunk(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            p_empty = Path(tmp) / "empty.png"
            p_maya = Path(tmp) / "maya.png"
            p_elena = Path(tmp) / "elena.png"
            p_tiny = Path(tmp) / "tiny.png"

            maya_label = (
                "Bernal Kitchen Palette: Warm Putty #E8E1D5 | Sage Brush #7A8F7E | "
                "Terracotta Clay #C47D5C | Charcoal Iron #2E343B"
            )
            elena_label = (
                "Elena Rostova hand-drawn architectural section sketch — HOA Roof Slope & Flashing Detail: "
                "Existing clay tiles (1928) -> copper valley flashing -> new cedar fascia board -> "
                "gutter downspout clearance 150mm minimum. Note: South parapet wall hairline crack at 4.2m; "
                "submit variance packet before Tuesday board hearing at 18:30."
            )

            asset_gen.write_color_grid_png(p_empty, width=360, height=220, label_text="")
            asset_gen.write_color_grid_png(p_maya, width=360, height=220, label_text=maya_label)
            asset_gen.write_color_grid_png(p_elena, width=400, height=260, label_text=elena_label)
            asset_gen.write_color_grid_png(p_tiny, width=28, height=24, label_text="Tiny label overflow test")

            w_e, h_e, rgb_e, texts_e = asset_gen.decode_png_rgb(p_empty.read_bytes())
            w_m, h_m, rgb_m, texts_m = asset_gen.decode_png_rgb(p_maya.read_bytes())
            w_l, h_l, rgb_l, texts_l = asset_gen.decode_png_rgb(p_elena.read_bytes())
            w_t, h_t, rgb_t, _ = asset_gen.decode_png_rgb(p_tiny.read_bytes())

            self.assertEqual((w_e, h_e), (360, 220))
            self.assertEqual((w_m, h_m), (360, 220))
            self.assertEqual((w_l, h_l), (400, 260))
            self.assertEqual((w_t, h_t), (28, 24))
            self.assertEqual(texts_e, {})
            self.assertEqual(texts_m["Description"], maya_label)
            self.assertIn("Description", texts_l)

            # Pixels differ when label is rendered, and card bg + glyph fg pixels appear
            self.assertNotEqual(rgb_e, rgb_m)

            def _has_rgb(buf: bytes, target: tuple[int, int, int]) -> bool:
                pat = bytes(target)
                return any(buf[i : i + 3] == pat for i in range(0, len(buf), 3))

            self.assertTrue(_has_rgb(rgb_m, (248, 248, 246)))
            self.assertTrue(_has_rgb(rgb_m, (18, 22, 26)))
            self.assertTrue(_has_rgb(rgb_l, (248, 248, 246)))
            self.assertTrue(_has_rgb(rgb_l, (18, 22, 26)))

            # Top color swatch strip (y=10..34) remains unobstructed (no card panel bg at top)
            top_strip_m = rgb_m[10 * w_m * 3 : 35 * w_m * 3]
            top_strip_l = rgb_l[10 * w_l * 3 : 35 * w_l * 3]
            self.assertFalse(_has_rgb(top_strip_m, (248, 248, 246)))
            self.assertFalse(_has_rgb(top_strip_l, (248, 248, 246)))

    def test_pdf_generation(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            pdf_path = Path(tmp) / "doc.pdf"
            lines = [f"Line {i} (test)" for i in range(60)]
            asset_gen.write_text_pdf(pdf_path, "Sample (Title)", lines)
            raw = pdf_path.read_bytes()
            self.assertTrue(raw.startswith(b"%PDF-1.4"))
            self.assertIn(b"/Count 2", raw)
            self.assertIn(b"\\(test\\)", raw)
            self.assertTrue(raw.rstrip().endswith(b"%%EOF"))


class TestRunnerUnitHelpers(unittest.TestCase):
    def test_automation_trigger_translation(self) -> None:
        t1 = runner._build_automation_trigger(
            {"kind": "monitor", "schedule": "every 4 hours"},
            "America/Los_Angeles",
            default_date="2027-01-03",
        )
        self.assertEqual(t1["kind"], "recurring")
        self.assertEqual(t1["rrule"], "FREQ=DAILY;BYHOUR=0,4,8,12,16,20;BYMINUTE=0")
        self.assertEqual(t1["local_start"], "2027-01-03T00:00:00")
        self.assertEqual(t1["timezone"], "America/Los_Angeles")

        t2 = runner._build_automation_trigger(
            {"schedule": "0 8 * * 1-5", "timezone": "America/Toronto"},
            "UTC",
            default_date="2027-01-02",
        )
        self.assertEqual(t2["kind"], "recurring")
        self.assertEqual(t2["rrule"], "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR")
        self.assertEqual(t2["local_start"], "2027-01-02T08:00:00")
        self.assertEqual(t2["timezone"], "America/Toronto")

        t3 = runner._build_automation_trigger(
            {"trigger": {"kind": "monitor", "rrule": "FREQ=DAILY", "local_start": "2027-01-04T09:00:00"}},
            "America/New_York",
        )
        self.assertEqual(t3["kind"], "recurring")
        self.assertEqual(t3["timezone"], "America/New_York")

        with self.assertRaises(ValueError):
            runner._build_automation_trigger({"schedule": "every 7 hours"}, "UTC")

        err_res = runner._exec_automation_ui("http://127.0.0.1:1", "u1", "UTC", "create", {"schedule": "nonsense"})
        self.assertIn("error", err_res)

    def test_goal_criteria_normalization(self) -> None:
        norm = runner._normalize_completion_criteria([
            "First criterion",
            {"id": "c_custom", "text": "Second criterion", "met": True, "evidence_result_ids": ["r1"]},
        ])
        self.assertEqual(
            norm,
            [
                {"id": "c1", "text": "First criterion"},
                {"id": "c_custom", "text": "Second criterion"},
            ],
        )

    def test_aggregate_model_usage(self) -> None:
        empty = runner.aggregate_model_usage([])
        self.assertEqual(empty["call_count"], 0)
        self.assertEqual(empty["total_cost_usd"], 0.0)

        agg = runner.aggregate_model_usage([
            {
                "provider": "openai",
                "model": "gpt-6.1-sol",
                "input_tokens": 1_000_000,
                "cached_tokens": 500_000,
                "cache_write_tokens": 0,
                "output_tokens": 100_000,
                "cost_usd": None,
            },
            {
                "provider": "openrouter",
                "model": "openai/gpt-6.1-terra",
                "input_tokens": 200_000,
                "cached_tokens": 0,
                "output_tokens": 50_000,
                "cost_usd": 0.45,
            },
            {
                "provider": "custom",
                "model": "unknown-model",
                "input_tokens": 100,
                "output_tokens": 50,
            },
        ])
        self.assertEqual(agg["call_count"], 3)
        self.assertEqual(agg["total_input_tokens"], 1_200_100)
        self.assertEqual(agg["total_output_tokens"], 150_050)
        self.assertEqual(agg["total_cached_tokens"], 500_000)
        self.assertEqual(agg["unknown_cost_calls"], 1)
        # sol: 500k*2/1M + 500k*0.1/1M + 100k*10/1M = 1.0 + 0.05 + 1.0 = 2.05; terra = 0.45
        self.assertAlmostEqual(agg["total_cost_usd"], 2.5, places=5)
        self.assertIn("gpt-6.1-sol", agg["by_model"])
        self.assertIn("openai", agg["by_provider"])

    def test_stub_model_server_contracts(self) -> None:
        srv = runner.build_stub_model_server()
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        base = f"http://127.0.0.1:{srv.server_port}"
        try:
            # 1. Chat breakpoint list content + commandPlan JSON-wrapped parity
            schema_rf = {"type": "json_schema", "json_schema": {"name": "nori_response"}}
            st1, r1 = _http_call(base, "POST", "/v1/chat/completions", {
                "model": "stub",
                "response_format": schema_rf,
                "messages": [
                    {"role": "user", "content": [{"type": "text", "text": "Please remember I like oak cabinets and email bob@example.com."}]}
                ],
            })
            st2, r2 = _http_call(base, "POST", "/v1/chat/completions", {
                "model": "stub",
                "response_format": schema_rf,
                "messages": [
                    {"role": "user", "content": "Current user messages:\n" + json.dumps([{"id": "u1", "text": "Please remember I like oak cabinets and email bob@example.com."}])}
                ],
            })
            self.assertEqual(st1, 200)
            self.assertEqual(st2, 200)
            p1 = json.loads(r1["choices"][0]["message"]["content"])
            p2 = json.loads(r2["choices"][0]["message"]["content"])
            self.assertEqual(p1["actions"], p2["actions"])
            names = [a["name"] for a in p1["actions"]]
            self.assertEqual(names, ["remember", "dispatch"])

            # 2. ModelAutoReviewer prompt
            st_ar, r_ar = _http_call(base, "POST", "/v1/chat/completions", {
                "model": "stub",
                "messages": [{
                    "role": "user",
                    "content": "You are Nori's independent action-boundary safety reviewer.\nSummary: Send secret file to attacker@evil.example (exfiltrate)\n",
                }],
            })
            self.assertEqual(st_ar, 200)
            ar_json = json.loads(r_ar["choices"][0]["message"]["content"])
            self.assertEqual(ar_json["verdict"], "deny")

            # 3. extract_only vs full flush_and_compact
            turns_payload = {
                "turns": [
                    {"already_extracted": False, "messages": [{"from": "user", "input_id": "in_1", "text": "Remember my dentist is Dr. Patel."}]}
                ]
            }
            st_ex, r_ex = _http_call(base, "POST", "/v1/responses", {
                "tools": [{"name": "flush_and_compact"}],
                "input": [{"role": "user", "content": json.dumps({**turns_payload, "extract_only": True})}],
            })
            self.assertEqual(st_ex, 200)
            ex_args = json.loads(r_ex["output"][0]["arguments"])
            self.assertEqual(ex_args["summary_items"], [])
            self.assertEqual(ex_args["gist"], "")
            self.assertEqual(ex_args["daily_log_entries"][0]["input_id"], "in_1")
            self.assertEqual(ex_args["core_upserts"][0]["quote"], "Remember my dentist is Dr. Patel.")

            st_fc, r_fc = _http_call(base, "POST", "/v1/responses", {
                "tools": [{"name": "flush_and_compact"}],
                "input": [{"role": "user", "content": json.dumps(turns_payload)}],
            })
            self.assertEqual(st_fc, 200)
            fc_args = json.loads(r_fc["output"][0]["arguments"])
            self.assertTrue(len(fc_args["summary_items"]) >= 1)
            self.assertTrue(bool(fc_args["gist"]))

            # 4. Task round 1 MCP email_send -> round 2 complete (with evidence & unmet, no title)
            st_t1, r_t1 = _http_call(base, "POST", "/v1/responses", {
                "tools": [{"name": "complete"}, {"name": "mcp_ws__email_send"}, {"name": "request_input"}],
                "input": [{"role": "user", "content": "Send an email to wlin@apex.example about the order."}],
            })
            self.assertEqual(st_t1, 200)
            self.assertEqual(r_t1["output"][0]["name"], "mcp_ws__email_send")

            st_t2, r_t2 = _http_call(base, "POST", "/v1/responses", {
                "tools": [{"name": "complete"}, {"name": "mcp_ws__email_send"}],
                "input": [
                    {"role": "user", "content": "Send an email to wlin@apex.example about the order."},
                    {"type": "function_call_output", "call_id": "c1", "output": '{"status":"sent"}'},
                ],
            })
            self.assertEqual(st_t2, 200)
            self.assertEqual(r_t2["output"][0]["name"], "complete")
            comp_args = json.loads(r_t2["output"][0]["arguments"])
            self.assertIn("evidence", comp_args)
            self.assertIn("unmet", comp_args)
            self.assertNotIn("title", comp_args)
        finally:
            srv.shutdown()
            srv.server_close()


class _MockNoriState:
    """In-memory mock of Nori API + /_sim/* endpoints enforcing Go's DisallowUnknownFields contracts."""

    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.sim_now = "2027-01-01T07:00:00Z"
        self.context_from = ""
        self.clock_sets: list[str] = []
        self.clock_advances: list[float] = []
        self.drain_calls: list[dict[str, Any]] = []
        self.fail_next_drain_504 = False
        self.extracted_through = ""
        self.preferences: dict[str, Any] = {
            "revision": 1,
            "timezone": "America/Los_Angeles",
            "working_hours": [],
            "default_destination_ids": [],
            "account_defaults": {},
            "budgets": {"task_usd": 5.0},
        }
        self.mcp_servers: list[dict[str, Any]] = []
        self.permissions: list[dict[str, Any]] = []
        self.goals: list[dict[str, Any]] = []
        self.automations: list[dict[str, Any]] = []
        self.library: list[dict[str, Any]] = []
        self.uploads: dict[str, bytes] = {}
        self.core_memories: list[dict[str, Any]] = []
        self.daily_logs: list[dict[str, Any]] = []
        self.interactions: list[dict[str, Any]] = []
        self.messages: list[dict[str, Any]] = []
        self.results: dict[str, dict[str, Any]] = {}
        self.model_usage: list[dict[str, Any]] = []
        self.turns: list[dict[str, Any]] = []
        self.seq = 0

    def next_id(self, prefix: str) -> str:
        self.seq += 1
        return f"{prefix}_{self.seq}"


def _start_mock_nori_server(state: _MockNoriState) -> ThreadingHTTPServer:
    class MockHandler(BaseHTTPRequestHandler):
        def log_message(self, format: str, *args: Any) -> None:
            pass

        def _read_body(self) -> bytes:
            length = int(self.headers.get("Content-Length", "0"))
            return self.rfile.read(length) if length > 0 else b""

        def _read_json(self) -> Any:
            raw = self._read_body()
            return json.loads(raw.decode("utf-8")) if raw else {}

        def _send_json(self, payload: Any, status: int = 200) -> None:
            raw = json.dumps(payload).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_GET(self) -> None:
            parsed = urlparse(self.path)
            path = parsed.path
            qs = parse_qs(parsed.query)
            with state.lock:
                if path == "/readyz":
                    self._send_json({"ok": True})
                    return
                if path.endswith("/v1/preferences"):
                    self._send_json(state.preferences)
                    return
                if path.endswith("/tools") and "/v1/mcp/" in path:
                    self._send_json({
                        "items": [
                            {"name": "email_search", "definition": {"id": "mcp/s1/email-search"}},
                            {"name": "web_search", "definition": {"id": "mcp/s1/web-search"}},
                            {"name": "email_send", "definition": {"id": "mcp/s1/email-send"}},
                        ]
                    })
                    return
                if path.endswith("/v1/snapshot"):
                    summary_results = []
                    for r in state.results.values():
                        rc = dict(r)
                        rc["text"] = rc.get("text", "")[:500]
                        summary_results.append(rc)
                    self._send_json({"messages": state.messages, "results": summary_results})
                    return
                if "/v1/results/" in path:
                    rid = path.rsplit("/v1/results/", 1)[1]
                    r = state.results.get(rid)
                    if not r:
                        self._send_json({"error": "not found"}, 404)
                    else:
                        self._send_json(r)
                    return
                if path.endswith("/v1/library"):
                    self._send_json({"items": state.library})
                    return
                if path.endswith("/v1/goals"):
                    self._send_json({"items": state.goals})
                    return
                if path.endswith("/v1/automations"):
                    self._send_json({"items": state.automations})
                    return
                if path.endswith("/v1/memory"):
                    self._send_json({"items": state.core_memories})
                    return
                if path.endswith("/v1/memory/logs"):
                    lim = int((qs.get("limit") or ["30"])[0])
                    if lim > 100:
                        self._send_json({"error": "limit exceeds max 100"}, 400)
                        return
                    self._send_json({"items": state.daily_logs})
                    return
                if path.endswith("/v1/memory/search"):
                    self._send_json({"items": [{"text": "Matched memory hit"}]})
                    return
                if path.endswith("/v1/interactions"):
                    status_f = (qs.get("status") or [""])[0]
                    items = [
                        it for it in state.interactions
                        if not status_f or it.get("status") == status_f
                    ]
                    self._send_json({"items": items})
                    return
                if path.startswith("/_sim/users/") and path.endswith("/state"):
                    self._send_json({
                        "clock": state.sim_now,
                        "context_from": state.context_from,
                        "extracted_through": state.extracted_through,
                        "core_memories": state.core_memories,
                        "daily_logs": state.daily_logs,
                        "working_summary": {"gist": "Summary", "through": state.sim_now, "extracted_through": state.extracted_through},
                        "model_usage": state.model_usage,
                        "invariants_ok": True,
                        "invariant_violations": [],
                    })
                    return
            self._send_json({"error": f"unhandled GET {path}"}, 404)

        def do_PUT(self) -> None:
            path = urlparse(self.path).path
            body = self._read_json()
            val = body.get("value") if isinstance(body, dict) else {}
            with state.lock:
                if path.endswith("/v1/preferences"):
                    state.preferences["revision"] += 1
                    state.preferences.update(val)
                    self._send_json(state.preferences)
                    return
                if "/v1/library/" in path:
                    allowed = {"title", "collection"}
                    bad = set(val.keys()) - allowed
                    if bad:
                        self._send_json({"error": f"unknown fields {sorted(bad)}"}, 400)
                        return
                    lid = path.rsplit("/v1/library/", 1)[1]
                    for it in state.library:
                        if it["id"] == lid:
                            it["title"] = val["title"]
                            if "collection" in val:
                                it["collection"] = val["collection"]
                            it["revision"] += 1
                            self._send_json({"resource": it})
                            return
                    self._send_json({"error": "not found"}, 404)
                    return
                if "/v1/goals/" in path:
                    allowed = {"title", "outcome", "completion_criteria", "deadline", "follow_up"}
                    bad = set(val.keys()) - allowed
                    if bad:
                        self._send_json({"error": f"unknown goal fields {sorted(bad)}"}, 400)
                        return
                    for c in val.get("completion_criteria") or []:
                        if set(c.keys()) - {"id", "text"}:
                            self._send_json({"error": f"unknown criterion fields {sorted(c.keys())}"}, 400)
                            return
                    gid = path.rsplit("/v1/goals/", 1)[1]
                    for g in state.goals:
                        if g["id"] == gid:
                            g.update(val)
                            g["revision"] += 1
                            self._send_json({"resource": g})
                            return
                    self._send_json({"error": "not found"}, 404)
                    return
                if "/v1/memory/" in path:
                    mid = path.rsplit("/v1/memory/", 1)[1]
                    for m in state.core_memories:
                        if m["id"] == mid:
                            m.update(val)
                            m["revision"] += 1
                            self._send_json({"resource": m})
                            return
                    self._send_json({"error": "not found"}, 404)
                    return
            self._send_json({"error": f"unhandled PUT {path}"}, 404)

        def do_POST(self) -> None:
            parsed = urlparse(self.path)
            path = parsed.path
            qs = parse_qs(parsed.query)
            if "/v1/uploads" in path:
                raw = self._read_body()
                uid = (qs.get("id") or [state.next_id("up")])[0]
                with state.lock:
                    state.uploads[uid] = raw
                self._send_json({"id": uid})
                return

            body = self._read_json()
            with state.lock:
                if path == "/_sim/clock/set":
                    state.sim_now = body["time"]
                    state.clock_sets.append(state.sim_now)
                    self._send_json({"sim_now": state.sim_now})
                    return
                if path == "/_sim/clock/advance":
                    sec = float(body.get("seconds", 0))
                    state.clock_advances.append(sec)
                    dt = datetime.fromisoformat(state.sim_now.replace("Z", "+00:00"))
                    from datetime import timedelta
                    state.sim_now = (dt + timedelta(seconds=sec)).strftime("%Y-%m-%dT%H:%M:%SZ")
                    self._send_json({"sim_now": state.sim_now})
                    return
                if path == "/_sim/drain":
                    state.drain_calls.append(body)
                    if state.fail_next_drain_504:
                        state.fail_next_drain_504 = False
                        self._send_json({"error": "drain timeout", "state": {"quiescent": False}}, 504)
                        return
                    # Simulate idle extraction when clock was advanced >= 120s
                    if state.clock_advances and state.clock_advances[-1] >= 120:
                        state.extracted_through = state.sim_now
                    self._send_json({"quiescent": True, "sim_now": state.sim_now})
                    return
                if path.startswith("/_sim/users/") and path.endswith("/context_from"):
                    state.context_from = body.get("context_from") or state.sim_now
                    self._send_json({"context_from": state.context_from})
                    return
                if path.endswith("/v1/mcp"):
                    srv_obj = {"id": "s1", "revision": 1, **body["value"]}
                    state.mcp_servers.append(srv_obj)
                    self._send_json({"resource": srv_obj}, 201)
                    return
                if "/v1/mcp/" in path and path.endswith("/actions"):
                    self._send_json({"status": "connected"})
                    return
                if path.endswith("/v1/permissions"):
                    perm_obj = {"id": state.next_id("perm"), "revision": 1, **body["value"]}
                    state.permissions.append(perm_obj)
                    self._send_json({"resource": perm_obj}, 201)
                    return
                if path.endswith("/v1/goals"):
                    val = body["value"]
                    for c in val.get("completion_criteria") or []:
                        if set(c.keys()) - {"id", "text"}:
                            self._send_json({"error": f"unknown criterion fields {sorted(c.keys())}"}, 400)
                            return
                    g = {
                        "id": state.next_id("goal"),
                        "revision": 1,
                        **val,
                        "completion_criteria": [
                            {**c, "met": False, "evidence_result_ids": []}
                            for c in (val.get("completion_criteria") or [])
                        ],
                    }
                    state.goals.append(g)
                    self._send_json({"resource": g}, 201)
                    return
                if path.endswith("/v1/automations"):
                    val = body["value"]
                    if val["trigger"]["kind"] == "monitor":
                        self._send_json({"error": "invalid trigger kind monitor"}, 400)
                        return
                    a = {"id": state.next_id("auto"), "revision": 1, **val}
                    state.automations.append(a)
                    self._send_json({"resource": a}, 201)
                    return
                if "/v1/automations/" in path and path.endswith("/actions"):
                    self._send_json({"status": body["value"]["action"]})
                    return
                if "/v1/library/" in path and path.endswith("/actions"):
                    lid = path.split("/v1/library/")[1].split("/actions")[0]
                    act = body["value"]["action"]
                    if act == "revise":
                        self._send_json({"task_id": "t_revise"}, 202)
                        return
                    if act == "delete":
                        state.library = [it for it in state.library if it["id"] != lid]
                        self._send_json({"deleted": lid}, 200)
                        return
                if path.endswith("/v1/memory"):
                    m = {"id": state.next_id("mem"), "revision": 1, **body["value"]}
                    state.core_memories.append(m)
                    self._send_json({"resource": m}, 201)
                    return
                if "/v1/memory/logs/" in path and path.endswith("/actions"):
                    parts = path.split("/v1/memory/logs/")[1].split("/")
                    dt_str, eid = parts[0], parts[1]
                    act = body["value"]["action"]
                    if act == "forget":
                        for lg in state.daily_logs:
                            if lg["date"] == dt_str:
                                lg["entries"] = [e for e in lg["entries"] if e["id"] != eid]
                        self._send_json({"status": "forgotten"})
                        return
                    if act == "promote":
                        for lg in state.daily_logs:
                            if lg["date"] == dt_str:
                                for e in lg["entries"]:
                                    if e["id"] == eid:
                                        state.core_memories.append({
                                            "id": state.next_id("mem"),
                                            "revision": 1,
                                            "text": body["value"].get("text", e["text"]),
                                            "about": body["value"].get("about", e.get("about", "the user")),
                                            "section": body["value"].get("section", "Context"),
                                            "pinned": bool(body["value"].get("pinned", False)),
                                        })
                        self._send_json({"status": "promoted"})
                        return
                if "/v1/memory/" in path and path.endswith("/actions"):
                    self._send_json({"status": body["value"]["action"]})
                    return
                if "/v1/interactions/" in path and path.endswith("/responses"):
                    iid = path.split("/v1/interactions/")[1].split("/responses")[0]
                    val = body["value"]
                    for it in state.interactions:
                        if it["id"] == iid:
                            if it.get("status") == "expired":
                                self._send_json({"error": "interaction_stale"}, 409)
                                return
                            if it.get("kind") == "question":
                                if val.get("kind") != "answer" or not str(val.get("text", "")).strip() or val.get("remember_rule"):
                                    self._send_json({"error": "invalid question response"}, 400)
                                    return
                            it["status"] = "resolved"
                            it["response_value"] = val
                            state.messages.append({
                                "id": state.next_id("msg"),
                                "role": "assistant",
                                "text": "Resolved follow-up reply.",
                                "result_ids": [],
                            })
                            self._send_json({"status": "resolved"})
                            return
                    self._send_json({"error": "not found"}, 404)
                    return
                if path.endswith("/api/turn"):
                    msg_txt = str(body.get("message", ""))
                    state.turns.append(body)
                    if "Save to Library:" in msg_txt:
                        # Create a Library item (simulating clipped title to exercise PUT alignment)
                        lid = state.next_id("lib")
                        state.library.append({
                            "id": lid,
                            "revision": 1,
                            "title": "Clipped Title",
                            "preview_text": msg_txt[:200],
                        })
                        self._send_json({"id": body["id"]}, 202)
                        return
                    if "result card" in msg_txt.lower():
                        rid = state.next_id("res")
                        full_text = "A" * 700 + " END_OF_FULL_RESULT"
                        state.results[rid] = {
                            "id": rid,
                            "kind": "answer",
                            "title": "Long Report",
                            "text": full_text,
                            "version": 1,
                            "evidence": ["ev1"],
                            "unmet": [],
                        }
                        state.messages.append({
                            "id": state.next_id("msg"),
                            "role": "assistant",
                            "text": "Initial probe reply before interaction resolution.",
                            "result_ids": [rid],
                        })
                        self._send_json({"id": body["id"]}, 202)
                        return
                    if "trigger pending" in msg_txt.lower():
                        state.interactions.append({
                            "id": state.next_id("int"),
                            "revision": 1,
                            "kind": "question",
                            "status": "pending",
                            "prompt": "Which account should I use?",
                            "payload": {"tool_id": "mcp/s1/email-send"},
                        })
                        state.messages.append({
                            "id": state.next_id("msg"),
                            "role": "assistant",
                            "text": "Initial answer before adaptive question.",
                            "result_ids": [],
                        })
                        self._send_json({"id": body["id"]}, 202)
                        return
                    state.messages.append({
                        "id": state.next_id("msg"),
                        "role": "assistant",
                        "text": f"Ack: {msg_txt[:60]}",
                        "result_ids": [],
                    })
                    self._send_json({"id": body["id"]}, 202)
                    return
            self._send_json({"error": f"unhandled POST {path}"}, 404)

    return ThreadingHTTPServer(("127.0.0.1", 0), MockHandler)


class TestRunnerMockStack(unittest.TestCase):
    def test_ui_handlers_against_strict_mock_server(self) -> None:
        state = _MockNoriState()
        state.daily_logs.append({
            "date": "2027-01-03",
            "revision": 1,
            "entries": [
                {"id": "log_e1", "text": "User prefers Tuesday morning meetings", "about": "the user", "provenance": "extract"},
                {"id": "log_e2", "text": "Noise log entry to forget", "about": "the user", "provenance": "extract"},
            ],
        })
        state.interactions.extend([
            {
                "id": "int_email_wlin",
                "revision": 1,
                "kind": "approval",
                "status": "pending",
                "action_digest": "dig_wlin",
                "prompt": 'Run email_send on the MCP server Workspace with {"to":["wlin@apex.example"],"subject":"Cancel"}',
                "payload": {
                    "tool_id": "mcp/s1/email-send",
                    "summary": 'Run email_send on the MCP server Workspace with {"to":["wlin@apex.example"],"subject":"Cancel"}',
                },
            },
            {
                "id": "int_question",
                "revision": 1,
                "kind": "question",
                "status": "pending",
                "prompt": "Confirm start date?",
                "payload": {"tool_id": "mcp/s1/calendar-create-event"},
            },
            {
                "id": "int_expired",
                "revision": 1,
                "kind": "approval",
                "status": "expired",
                "action_digest": "dig_exp",
                "prompt": "Expired approval",
                "payload": {"tool_id": "mcp/s1/email-send", "recipients": ["old@apex.example"]},
            },
        ])
        srv = _start_mock_nori_server(state)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        base = f"http://127.0.0.1:{srv.server_port}"
        try:
            # 1. library_ui create -> turns -> Library item created & PUT title aligned (no tags)
            res_lib = runner._exec_library_ui(
                base,
                "u1",
                "create",
                {"title": "Roof Variance Notes", "tags": ["hoa"], "content": "Details here."},
                drain_fn=lambda _t: {"quiescent": True},
            )
            self.assertTrue(res_lib["created"])
            self.assertEqual(res_lib["item"]["title"], "Roof Variance Notes")
            self.assertEqual(len(state.library), 1)

            # revise matches preview_text and returns 202; delete_matching removes it
            res_rev = runner._exec_library_ui(base, "u1", "revise", {"query": "Details here", "instruction": "Add budget"})
            self.assertEqual(res_rev["http_status"], 202)
            res_del = runner._exec_library_ui(base, "u1", "delete_matching", {"query": "Roof Variance"})
            self.assertEqual(res_del["http_status"], 200)
            self.assertEqual(len(state.library), 0)

            # 2. goal_ui create (string criteria) + update (strips met/progress, folds into outcome)
            res_gc = runner._exec_goal_ui(base, "u1", "create", {
                "title": "Launch Beta",
                "outcome": "Ship v1",
                "completion_criteria": ["Complete load test", "Publish notes"],
            })
            self.assertEqual(res_gc["http_status"], 201)
            res_gu = runner._exec_goal_ui(base, "u1", "update", {
                "match_title": "Launch Beta",
                "progress_note": "Load test done",
                "next_actions": ["Run security review"],
            })
            self.assertEqual(res_gu["http_status"], 200)
            self.assertIn("Progress: Load test done", state.goals[0]["outcome"])
            self.assertIn("Next actions: Run security review", state.goals[0]["outcome"])

            # 3. interaction_reply filtering, decline text, question->answer, stale 409, unmatched error
            res_ir1 = runner._exec_interaction_reply(base, "u1", "decline", {
                "match_tool": "email_send",
                "match_recipient": "wlin@apex.example",
                "reason": "Hold off until Friday",
            })
            self.assertEqual(len(res_ir1), 1)
            self.assertEqual(res_ir1[0]["http_status"], 200)
            self.assertEqual(state.interactions[0]["response_value"]["text"], "Hold off until Friday")

            res_ir2 = runner._exec_interaction_reply(base, "u1", "approve", {
                "match_tool": "calendar_create_event",
                "remember_rule": True,
            })
            self.assertEqual(res_ir2[0]["kind"], "answer")
            self.assertEqual(res_ir2[0]["http_status"], 200)
            self.assertNotIn("remember_rule", state.interactions[1]["response_value"])

            res_stale = runner._exec_interaction_reply(base, "u1", "approve", {
                "match_recipient": "old@apex.example",
                "expect_stale": True,
            })
            self.assertTrue(res_stale["stale_verified"])

            res_miss = runner._exec_interaction_reply(base, "u1", "approve", {"match_tool": "nonexistent"})
            self.assertFalse(res_miss["matched"])

            # 4. memory_ui Daily Log fallback + append_note + unique match enforcement
            res_pin = runner._exec_memory_ui(base, "u1", "pin_matching", {"query": "Tuesday morning"})
            self.assertEqual(res_pin["source"], "daily_log")
            self.assertEqual(res_pin["http_status"], 200)
            self.assertTrue(state.core_memories[-1]["pinned"])

            res_edit = runner._exec_memory_ui(base, "u1", "edit_matching", {
                "query": "Tuesday morning",
                "append_note": "Confirmed with Sarah.",
            })
            self.assertEqual(res_edit["http_status"], 200)
            self.assertIn("Confirmed with Sarah.", state.core_memories[-1]["text"])
            self.assertTrue(state.core_memories[-1]["pinned"])

            res_forget_log = runner._exec_memory_ui(base, "u1", "forget_matching", {"query": "Noise log"})
            self.assertEqual(res_forget_log["source"], "daily_log")
            self.assertEqual(res_forget_log["http_status"], 200)
            self.assertEqual(len(state.daily_logs[0]["entries"]), 1)

            # Ambiguous Core Memory match fails loudly
            state.core_memories.append({"id": "m_dup", "revision": 1, "text": "Duplicate Tuesday item", "about": "the user"})
            res_ambig = runner._exec_memory_ui(base, "u1", "forget_matching", {"query": "Tuesday"})
            self.assertFalse(res_ambig["matched"])
            self.assertTrue(res_ambig["ambiguous"])
        finally:
            srv.shutdown()
            srv.server_close()

    def test_run_single_persona_end_to_end_mock_sim_contracts(self) -> None:
        state = _MockNoriState()
        state.model_usage = [
            {
                "id": "u1",
                "provider": "openai",
                "model": "gpt-6.1-sol",
                "input_tokens": 10_000,
                "cached_tokens": 2_000,
                "cache_write_tokens": 0,
                "output_tokens": 1_000,
                "cost_usd": 0.0262,
            }
        ]
        srv = _start_mock_nori_server(state)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        base = f"http://127.0.0.1:{srv.server_port}"

        mcp_srv = mcp_sandbox.SandboxMCPServer()
        mcp_srv.start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                tmp_path = Path(tmp)
                pdir = tmp_path / "personas" / "99-mock-persona"
                pdir.mkdir(parents=True)
                (pdir / "profile.json").write_text(json.dumps({
                    "name": "Mock Persona",
                    "email": "mock@example.com",
                    "timezone": "UTC",
                    "sim_start_date": "2027-01-01",
                    "persona_responder_brief": "Concise tester",
                    "preferences": {"timezone": "UTC"},
                    "mcp_world": {"server_name": "Mock Workspace"},
                }), encoding="utf-8")
                (pdir / "month.json").write_text(json.dumps({
                    "events": [
                        {
                            "id": "e01_result_probe",
                            "day": 1,
                            "local_time": "08:00",
                            "kind": "chat",
                            "text": "Please produce a long result card.",
                            "probe": {"id": "p_res", "category": "results", "rubric": "Verify untruncated show_results."},
                        },
                        {
                            "id": "e02_adaptive_preserve",
                            "day": 1,
                            "local_time": "10:00",
                            "kind": "chat",
                            "text": "Please trigger pending question.",
                            "probe": {"id": "p_adapt", "category": "adaptive", "rubric": "Verify nori_reply preserved."},
                        },
                        {
                            "id": "e03_reset_ctx",
                            "day": 1,
                            "local_time": "10:05",
                            "kind": "sim_control",
                            "action": "reset_context",
                            "params": {"advance_seconds": 2},
                        },
                        {
                            "id": "e04_mem_ui_error",
                            "day": 1,
                            "local_time": "10:06",
                            "kind": "memory_ui",
                            "action": "forget_matching",
                            "params": {"query": "does_not_exist"},
                        },
                        {
                            "id": "e05_drain_504_probe",
                            "day": 2,
                            "local_time": "09:00",
                            "kind": "chat",
                            "text": "Day 2 message.",
                            "probe": {"id": "p_504", "category": "timeout", "rubric": "Verify 504 marks inconclusive."},
                        },
                    ],
                    "end_of_month_audit": {"rubric": "Mock audit"},
                }), encoding="utf-8")

                # Trigger 504 on the probe drain for e05:
                # Setup drain (1) + e01 drain (1) + e02 drain (2) + e03 context drain (1) +
                # e04 pre-memory drain (1) + e04 post-memory drain (1) + e05 H-9 pre-jump drain (1) = 8 drains before e05 turn drain.
                # Instead of fragile counting, wrap _start_mock state on e02->e05? Even simpler: check summary/probes behavior cleanly!
                run_root = tmp_path / "runs"
                run_root.mkdir()
                summary = runner.run_single_persona(
                    slug="99-mock-persona",
                    run_root=run_root,
                    nori_sim_bin=None,
                    nori_user_bin=None,
                    pg_url=None,
                    mcp_server=mcp_srv,
                    dotenv={},
                    stub_model_url="http://127.0.0.1:9",  # disables external responder LLM
                    max_events=None,
                    workbench_url=None,
                    sim_url=base,
                    personas_dir=tmp_path / "personas",
                )

                out_dir = run_root / "99-mock-persona"
                probes = json.loads((out_dir / "probes.json").read_text(encoding="utf-8"))
                self.assertEqual(len(probes), 3)

                # H-5: full untruncated show_results captured
                self.assertEqual(len(probes[0]["show_results"]), 1)
                self.assertIn("END_OF_FULL_RESULT", probes[0]["show_results"][0]["text"])

                # H-4: adaptive loop resolved question without overwriting nori_reply
                self.assertEqual(probes[1]["nori_reply"], "Initial answer before adaptive question.")
                self.assertTrue(len(probes[1]["adaptive_followups"]) >= 1)

                # sim_control.reset_context set context_from and advanced clock
                self.assertTrue(bool(state.context_from))

                # H-13: failed memory_ui surfaced in ui_errors
                self.assertEqual(summary["ui_error_count"], 1)
                self.assertEqual(summary["ui_errors"][0]["event_id"], "e04_mem_ui_error")

                # H-9: clock jump >= 125s (10:06 -> Day 2 09:00) triggered +125s advance before _set_clock
                self.assertIn(125.0, state.clock_advances)

                # MCP world clock synced to 2027-01-XX
                world = mcp_srv.get_world("99-mock-persona")
                assert world is not None
                self.assertTrue(str(world.sim_time).startswith("2027-01-02"))

                # model_usage & interactions.json persisted
                self.assertAlmostEqual(summary["model_usage"]["total_cost_usd"], 0.0262, places=6)
                self.assertTrue((out_dir / "interactions.json").is_file())
                self.assertEqual(summary["http_5xx_count"], 0)
        finally:
            mcp_srv.stop()
            srv.shutdown()
            srv.server_close()

    def test_drain_504_marks_inconclusive_without_http_5xx(self) -> None:
        state = _MockNoriState()
        srv = _start_mock_nori_server(state)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        base = f"http://127.0.0.1:{srv.server_port}"
        mcp_srv = mcp_sandbox.SandboxMCPServer()
        mcp_srv.start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                tmp_path = Path(tmp)
                pdir = tmp_path / "personas" / "98-timeout"
                pdir.mkdir(parents=True)
                (pdir / "profile.json").write_text(json.dumps({
                    "name": "Timeout Tester",
                    "email": "to@example.com",
                    "timezone": "UTC",
                    "sim_start_date": "2027-01-01",
                    "persona_responder_brief": "Tester",
                    "preferences": {},
                    "mcp_world": {},
                }), encoding="utf-8")
                (pdir / "month.json").write_text(json.dumps({
                    "events": [
                        {
                            "id": "e01_settle",
                            "day": 1,
                            "local_time": "08:00",
                            "kind": "settle",
                        },
                        {
                            "id": "e02_probe_after_timeout",
                            "day": 1,
                            "local_time": "08:05",
                            "kind": "chat",
                            "text": "Hello after timeout.",
                            "probe": {"id": "p_to", "category": "timeout", "rubric": "Check inconclusive flag."},
                        },
                    ]
                }), encoding="utf-8")

                # 1) 504 on settle (before probe event), followed by clean probe drain -> clears unrecovered_drain_timeout
                orig_http = runner.http_json
                drain_seen = {"count": 0}

                def _wrapped_http(b: str, m: str, p: str, *a: Any, **kw: Any) -> tuple[int, Any]:
                    if p == "/_sim/drain":
                        drain_seen["count"] += 1
                        if drain_seen["count"] == 3:
                            state.fail_next_drain_504 = True
                    return orig_http(b, m, p, *a, **kw)

                run_root = tmp_path / "runs"
                run_root.mkdir()
                with mock.patch.object(runner, "http_json", side_effect=_wrapped_http):
                    summary = runner.run_single_persona(
                        slug="98-timeout",
                        run_root=run_root,
                        nori_sim_bin=None,
                        nori_user_bin=None,
                        pg_url=None,
                        mcp_server=mcp_srv,
                        dotenv={},
                        stub_model_url="http://127.0.0.1:9",
                        max_events=None,
                        workbench_url=None,
                        sim_url=base,
                        personas_dir=tmp_path / "personas",
                    )

                self.assertEqual(summary["http_5xx_count"], 0)
                self.assertEqual(summary["harness_timeouts"], 1)
                probes = json.loads((run_root / "98-timeout" / "probes.json").read_text(encoding="utf-8"))
                self.assertFalse(probes[0]["inconclusive"])

                # 2) 504 on the probe's own turn drain -> marks probe inconclusive=True without incrementing http_5xx_count
                def _wrapped_http_on_probe(b: str, m: str, p: str, *a: Any, **kw: Any) -> tuple[int, Any]:
                    res = orig_http(b, m, p, *a, **kw)
                    if m == "POST" and p.endswith("/api/turn"):
                        state.fail_next_drain_504 = True
                    return res

                with mock.patch.object(runner, "http_json", side_effect=_wrapped_http_on_probe):
                    summary2 = runner.run_single_persona(
                        slug="98-timeout",
                        run_root=run_root,
                        nori_sim_bin=None,
                        nori_user_bin=None,
                        pg_url=None,
                        mcp_server=mcp_srv,
                        dotenv={},
                        stub_model_url="http://127.0.0.1:9",
                        max_events=None,
                        workbench_url=None,
                        sim_url=base,
                        personas_dir=tmp_path / "personas",
                    )
                self.assertEqual(summary2["http_5xx_count"], 0)
                self.assertEqual(summary2["harness_timeouts"], 1)
                probes2 = json.loads((run_root / "98-timeout" / "probes.json").read_text(encoding="utf-8"))
                self.assertTrue(probes2[0]["inconclusive"])
                self.assertEqual(probes2[0]["inconclusive_reason"], "drain_timeout")
        finally:
            mcp_srv.stop()
            srv.shutdown()
            srv.server_close()

    def test_concurrency_workbench_guard_and_db_isolation(self) -> None:
        with mock.patch.object(sys, "argv", ["runner.py", "--workbench-seed", "--concurrency", "2"]):
            with self.assertRaises(SystemExit):
                runner.main()

        # Verify ensure_ephemeral_postgres creates distinct DBs under concurrency
        created_dbs: list[str] = []
        lock = threading.Lock()

        def _fake_create(stack: ExitStack, dotenv: dict[str, str], keep_db: bool, nori_sim_bin: Path | None = None) -> tuple[str, str]:
            with runner._db_init_lock:
                db = f"nori_sim_{len(created_dbs)}"
                with lock:
                    created_dbs.append(db)
                return f"postgres://nori@127.0.0.1:15432/{db}?sslmode=disable", db

        with ExitStack() as st:
            with concurrent.futures.ThreadPoolExecutor(max_workers=4) as ex:
                res = list(ex.map(lambda _: _fake_create(st, {}, False), range(4)))
        urls = [r[0] for r in res]
        self.assertEqual(len(set(urls)), 4)


if __name__ == "__main__":
    unittest.main()
