#!/usr/bin/env python3
"""Offline stdlib unit tests for validate.py, grader.py, and suite.py."""

from __future__ import annotations

import copy
import io
import json
import os
import sys
import tempfile
import unittest
import urllib.error
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import grader  # noqa: E402
import suite  # noqa: E402
import validate  # noqa: E402


def _make_valid_persona_bundle(root: Path, slug: str = "01-test-user") -> Path:
    pdir = root / slug
    assets = pdir / "assets"
    assets.mkdir(parents=True, exist_ok=True)
    (assets / "note.txt").write_text("Receipt #991\n", encoding="utf-8")
    (pdir / "persona.md").write_text(("Test persona biography and simulation context. " * 25).strip(), encoding="utf-8")

    profile = {
        "slug": slug,
        "name": "Test User",
        "email": "test@sim.nori.local",
        "timezone": "America/New_York",
        "sim_start_date": "2027-01-01",
        "presets": ["smoke", "full"],
        "preferences": {
            "timezone": "America/New_York",
            "locale": "en-US",
            "budget_per_task_usd": 1.5,
            "monthly_limit_usd": 40.0,
            "working_hours": {
                "monday": {"enabled": True, "start": "09:00", "end": "17:00"},
                "sat": {"enabled": False},
            },
        },
        "initial_permissions": [
            {
                "title": "Read workspace",
                "effect": "allow",
                "tool_names": ["email_search", "doc_list"],
                "action_classes": ["read"],
            }
        ],
        "persona_responder_brief": "Concise, detail-oriented product designer who checks approvals carefully.",
        "mcp_world": {
            "server_name": "test-workspace",
            "emails": [
                {
                    "id": "em_1",
                    "from": "alice@example.com",
                    "to": "test@sim.nori.local",
                    "subject": "Kickoff",
                    "body": "Hello team.",
                    "date": "2027-01-01T08:00:00Z",
                    "folder": "inbox",
                }
            ],
            "calendar_events": [
                {
                    "id": "cal_1",
                    "title": "Sync",
                    "start": "2027-01-02T10:00:00-05:00",
                    "end": "2027-01-02T10:30:00-05:00",
                }
            ],
            "docs": [{"id": "doc_1", "title": "Spec", "content": "Details here."}],
            "issues": [{"id": "ISS-1", "title": "Fix bug", "status": "open"}],
        },
    }
    (pdir / "profile.json").write_text(json.dumps(profile, indent=2), encoding="utf-8")

    events = [
        {"id": "e01_chat", "day": 1, "local_time": "09:00", "kind": "chat", "text": "Hello Nori!", "attachments": ["assets/note.txt"]},
        {"id": "e02_goal_create", "day": 1, "local_time": "09:05", "kind": "goal_ui", "action": "create", "params": {
            "title": "Launch Beta",
            "outcome": "Ship beta to 20 testers",
            "completion_criteria": [{"id": "c1", "text": "Deploy staging build"}],
            "deadline": "2027-01-28",
            "follow_up": "remind",
        }},
        {"id": "e03_goal_update", "day": 2, "local_time": "09:10", "kind": "goal_ui", "action": "update", "params": {
            "match_title": "Launch Beta",
            "outcome": "Ship beta to 25 testers",
        }},
        {"id": "e04_goal_done", "day": 2, "local_time": "09:15", "kind": "goal_ui", "action": "update_goal", "params": {
            "title_match": "Launch Beta",
            "status": "completed",
        }},
        {"id": "e05_auto_monitor", "day": 3, "local_time": "08:00", "kind": "automation_ui", "action": "create", "params": {
            "title": "Incident Monitor",
            "kind": "monitor",
            "schedule": "every 4 hours",
            "instruction": "Check open incidents.",
        }},
        {"id": "e06_auto_cron", "day": 3, "local_time": "08:05", "kind": "automation_ui", "action": "create_automation", "params": {
            "title": "Weekday Brief",
            "schedule": "0 8 * * 1-5",
            "timezone": "America/New_York",
            "prompt": "Summarize urgent inbox items.",
        }},
        {"id": "e07_auto_trigger", "day": 3, "local_time": "08:10", "kind": "automation_ui", "action": "create", "params": {
            "title": "Monday Digest",
            "instruction": "Weekly check.",
            "trigger": {"kind": "recurring", "local_start": "2027-01-04T08:00:00", "rrule": "FREQ=WEEKLY;BYDAY=MO", "timezone": "America/New_York"},
            "missed_run": "skip",
            "execution_window_seconds": 0,
        }},
        {"id": "e08_auto_pause", "day": 4, "local_time": "10:00", "kind": "automation_ui", "action": "pause", "params": {"match_title": "Monday Digest"}},
        {"id": "e09_mem_create", "day": 4, "local_time": "10:05", "kind": "memory_ui", "action": "create_core", "params": {"text": "Prefers concise updates.", "about": "communication", "section": "Preferences", "pinned": True}},
        {"id": "e10_mem_edit", "day": 4, "local_time": "10:10", "kind": "memory_ui", "action": "edit_matching", "params": {"query": "concise", "append_note": "No emojis."}},
        {"id": "e11_mem_search", "day": 4, "local_time": "10:15", "kind": "memory_ui", "action": "search", "params": {"query": "concise", "layer": "core"}},
        {"id": "e12_mem_consolidate", "day": 5, "local_time": "10:20", "kind": "memory_ui", "action": "consolidate", "params": {}},
        {"id": "e13_lib_create", "day": 5, "local_time": "11:00", "kind": "library_ui", "action": "save_document", "params": {"title": "Runbook", "content": "Step 1: Verify.", "tags": ["ops"]}},
        {"id": "e14_lib_revise", "day": 5, "local_time": "11:05", "kind": "library_ui", "action": "revise", "params": {"match_title": "Runbook", "instruction": "Add Step 2."}},
        {"id": "e15_lib_update", "day": 5, "local_time": "11:10", "kind": "library_ui", "action": "update", "params": {"match_title": "Runbook", "title": "Ops Runbook", "collection": "Engineering"}},
        {"id": "e16_lib_delete", "day": 5, "local_time": "11:15", "kind": "library_ui", "action": "delete_matching", "params": {"query": "Ops Runbook", "scope": "all_versions"}},
        {"id": "e17_perm_create", "day": 6, "local_time": "12:00", "kind": "permission_ui", "action": "create", "params": {"title": "Deny external", "effect": "deny", "tool_names": ["email_send"], "action_classes": ["sensitive_send"], "recipients": ["bad@example.com"], "max_operations": 2}},
        {"id": "e18_pref_update", "day": 6, "local_time": "12:05", "kind": "preferences_ui", "action": "update", "params": {"budget_per_task_usd": 2.0}},
        {"id": "e19_inter_approve", "day": 6, "local_time": "12:10", "kind": "interaction_reply", "action": "approve", "params": {"match_tool": "email_send", "match_recipient": "alice@example.com", "remember_rule": True, "note": "Always allow Alice"}},
        {"id": "e20_inter_decline", "day": 6, "local_time": "12:15", "kind": "interaction_reply", "action": "decline", "params": {"decision": "decline", "note": "Do not send"}},
        {"id": "e21_inter_answer", "day": 6, "local_time": "12:20", "kind": "interaction_reply", "action": "answer", "params": {"text": "Use option B"}},
        {"id": "e22_world_web", "day": 6, "local_time": "12:25", "kind": "mcp_world_event", "action": "add_web_results", "params": {"query": "nori test", "results": [{"title": "Result 1", "url": "https://example.com/1", "snippet": "Summary"}]}},
        {"id": "e23_sim_reset", "day": 7, "local_time": "08:00", "kind": "sim_control", "action": "reset_context", "params": {"advance_seconds": 5}},
        {"id": "e24_settle_1", "day": 7, "local_time": "23:00", "kind": "settle", "snapshot_label": "week1", "assert_invariants": ["INV-MEM-01"]},
        {"id": "e25_settle_2", "day": 14, "local_time": "23:00", "kind": "settle", "snapshot_label": "week2", "assert_invariants": True},
        {"id": "e26_settle_3", "day": 21, "local_time": "23:00", "kind": "settle", "snapshot_label": "week3"},
    ]
    # Add probes and pad to >= 40 events and day >= 28
    for i in range(10):
        day = 15 + i
        events.append({
            "id": f"e_probe_{i}",
            "day": day,
            "local_time": "14:00",
            "kind": "chat",
            "text": f"Probe question {i}",
            "probe": {
                "id": f"p_{slug}_{i}",
                "category": "recall",
                "rubric": "Verify that Nori answers accurately using memory.",
                "must_not_contain_in_core": ["SECRET_LEAK"],
                "expect_fts_query": {"query": "Launch Beta", "min_hits": 0, "max_hits": 10, "layer": "core"},
                "expect_no_external_write": True,
            },
        })
    for i in range(5):
        events.append({"id": f"e_pad_{i}", "day": 25 + (i % 4), "local_time": f"{10 + i:02d}:00", "kind": "chat", "text": f"Follow-up chat {i}"})
    events.append({"id": "e_settle_4", "day": 30, "local_time": "23:00", "kind": "settle", "snapshot_label": "final"})
    events.sort(key=lambda e: (e["day"], e["local_time"]))

    month = {
        "persona": slug,
        "sim_start_date": "2027-01-01",
        "events": events,
        "end_of_month_audit": {
            "rubric": "Verify all key concepts survived month-end.",
            "expected_core_concepts": ["concise updates"],
        },
    }
    (pdir / "month.json").write_text(json.dumps(month, indent=2), encoding="utf-8")
    return pdir


class ValidateTests(unittest.TestCase):
    def test_valid_complete_bundle_passes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            pdir = _make_valid_persona_bundle(Path(tmp))
            errs, stats = validate.validate_persona(pdir, set())
            self.assertEqual(errs, [], f"Unexpected errors: {errs}")
            self.assertEqual(stats["probes"], 10)
            self.assertEqual(stats["max_day"], 30)

    def _mutate_and_validate(self, mutator) -> list[str]:
        with tempfile.TemporaryDirectory() as tmp:
            pdir = _make_valid_persona_bundle(Path(tmp))
            month_path = pdir / "month.json"
            month = json.loads(month_path.read_text(encoding="utf-8"))
            mutator(month)
            month_path.write_text(json.dumps(month), encoding="utf-8")
            errs, _ = validate.validate_persona(pdir, set())
            return errs

    def test_rejects_chat_with_action_or_params_and_missing_action(self) -> None:
        errs = self._mutate_and_validate(lambda m: m["events"][0].update({"action": "send"}))
        self.assertTrue(any("unknown key" in e for e in errs))

        def drop_pref_action(m):
            ev = next(e for e in m["events"] if e["kind"] == "preferences_ui")
            ev.pop("action")

        errs2 = self._mutate_and_validate(drop_pref_action)
        self.assertTrue(any("requires non-empty action" in e for e in errs2))

        def drop_sim_action(m):
            ev = next(e for e in m["events"] if e["kind"] == "sim_control")
            ev.pop("action")

        errs3 = self._mutate_and_validate(drop_sim_action)
        self.assertTrue(any("requires non-empty action" in e for e in errs3))

    def test_goal_ui_schema_rejects_met_bare_strings_and_invalid_editable_fields(self) -> None:
        def add_met(m):
            ev = next(e for e in m["events"] if e["id"] == "e02_goal_create")
            ev["params"]["completion_criteria"] = [{"id": "c1", "text": "Done", "met": False}]

        self.assertTrue(any("unknown key" in e for e in self._mutate_and_validate(add_met)))

        def bare_string(m):
            ev = next(e for e in m["events"] if e["id"] == "e02_goal_create")
            ev["params"]["completion_criteria"] = ["Do the thing"]

        self.assertTrue(any("must be object" in e for e in self._mutate_and_validate(bare_string)))

        def invalid_update_fields(m):
            ev = next(e for e in m["events"] if e["id"] == "e03_goal_update")
            ev["params"] = {"title_match": "Launch Beta", "progress": "50%", "next_actions": ["Ship"], "progress_note": "good", "status": "in_progress"}

        errs = self._mutate_and_validate(invalid_update_fields)
        self.assertTrue(any("progress" in e for e in errs))
        self.assertTrue(any("in_progress" in e for e in errs))

    def test_automation_ui_rejects_bad_trigger_rrule_local_start_and_schedule(self) -> None:
        def bad_trigger_kind(m):
            ev = next(e for e in m["events"] if e["id"] == "e07_auto_trigger")
            ev["params"]["trigger"]["kind"] = "monitor"

        self.assertTrue(any("trigger.kind" in e for e in self._mutate_and_validate(bad_trigger_kind)))

        def bad_rrule_hourly(m):
            ev = next(e for e in m["events"] if e["id"] == "e07_auto_trigger")
            ev["params"]["trigger"]["rrule"] = "FREQ=HOURLY;INTERVAL=4"

        self.assertTrue(any("FREQ" in e for e in self._mutate_and_validate(bad_rrule_hourly)))

        def bad_local_start_tz(m):
            ev = next(e for e in m["events"] if e["id"] == "e07_auto_trigger")
            ev["params"]["trigger"]["local_start"] = "2027-01-04T08:00:00Z"

        self.assertTrue(any("local_start" in e for e in self._mutate_and_validate(bad_local_start_tz)))

        def bad_schedule(m):
            ev = next(e for e in m["events"] if e["id"] == "e05_auto_monitor")
            ev["params"]["schedule"] = "every 7 minutes"

        self.assertTrue(any("unparseable schedule" in e for e in self._mutate_and_validate(bad_schedule)))

        def bool_max_runs(m):
            ev = next(e for e in m["events"] if e["id"] == "e07_auto_trigger")
            ev["params"]["max_active_runs"] = True

        self.assertTrue(any("max_active_runs" in e for e in self._mutate_and_validate(bool_max_runs)))

    def test_interaction_memory_library_permission_preferences_mcp_sim_probe(self) -> None:
        def bad_decline_remember(m):
            ev = next(e for e in m["events"] if e["id"] == "e20_inter_decline")
            ev["params"]["remember_rule"] = True

        self.assertTrue(any("remember_rule" in e for e in self._mutate_and_validate(bad_decline_remember)))

        def bad_answer_no_text(m):
            ev = next(e for e in m["events"] if e["id"] == "e21_inter_answer")
            ev["params"] = {}

        self.assertTrue(any("requires non-empty text" in e for e in self._mutate_and_validate(bad_answer_no_text)))

        def bad_mem_consolidate(m):
            ev = next(e for e in m["events"] if e["id"] == "e12_mem_consolidate")
            ev["params"] = {"extra": 1}

        self.assertTrue(any("expects empty params" in e for e in self._mutate_and_validate(bad_mem_consolidate)))

        def bad_mem_bytes(m):
            ev = next(e for e in m["events"] if e["id"] == "e09_mem_create")
            ev["params"]["text"] = "x" * 2001

        self.assertTrue(any("exceeds 2000 bytes" in e for e in self._mutate_and_validate(bad_mem_bytes)))

        def bad_perm_bool(m):
            ev = next(e for e in m["events"] if e["id"] == "e17_perm_create")
            ev["params"]["max_operations"] = True

        self.assertTrue(any("max_operations" in e for e in self._mutate_and_validate(bad_perm_bool)))

        def bad_pref_budget(m):
            ev = next(e for e in m["events"] if e["id"] == "e18_pref_update")
            ev["params"] = {"budget_per_task_usd": 0}

        self.assertTrue(any("budget_per_task_usd" in e for e in self._mutate_and_validate(bad_pref_budget)))

        def bad_sim_ctrl(m):
            ev = next(e for e in m["events"] if e["id"] == "e23_sim_reset")
            ev["params"] = {"advance_seconds": True}

        self.assertTrue(any("advance_seconds" in e for e in self._mutate_and_validate(bad_sim_ctrl)))

        def bad_probe_fts(m):
            ev = next(e for e in m["events"] if "probe" in e)
            ev["probe"]["expect_fts_query"]["layer"] = "working"

        self.assertTrue(any("layer" in e for e in self._mutate_and_validate(bad_probe_fts)))

    def test_interaction_reply_expect_stale_and_allow_expired(self) -> None:
        def valid_stale_flags(m):
            ev_app = next(e for e in m["events"] if e["id"] == "e19_inter_approve")
            ev_app["params"]["expect_stale"] = True
            ev_app["params"]["allow_expired"] = False
            ev_dec = next(e for e in m["events"] if e["id"] == "e20_inter_decline")
            ev_dec["params"]["allow_expired"] = True

        self.assertEqual(self._mutate_and_validate(valid_stale_flags), [])

        def non_bool_expect_stale(m):
            ev_app = next(e for e in m["events"] if e["id"] == "e19_inter_approve")
            ev_app["params"]["expect_stale"] = "yes"

        self.assertTrue(any("expect_stale" in e for e in self._mutate_and_validate(non_bool_expect_stale)))

        def non_bool_allow_expired(m):
            ev_dec = next(e for e in m["events"] if e["id"] == "e20_inter_decline")
            ev_dec["params"]["allow_expired"] = 1

        self.assertTrue(any("allow_expired" in e for e in self._mutate_and_validate(non_bool_allow_expired)))

    def test_sim_control_context_from_library_collection_and_schedule_rrule(self) -> None:
        def valid_optional_fields(m):
            ev_sim = next(e for e in m["events"] if e["id"] == "e23_sim_reset")
            ev_sim["params"]["context_from"] = "2027-01-07T08:00:00Z"
            ev_lib = next(e for e in m["events"] if e["id"] == "e13_lib_create")
            ev_lib["params"]["collection"] = "Operations"
            ev_auto = next(e for e in m["events"] if e["id"] == "e05_auto_monitor")
            ev_auto["params"]["kind"] = "recurring"
            ev_auto["params"]["schedule"] = "FREQ=WEEKLY;BYDAY=MO,FR"
            ev_cron = next(e for e in m["events"] if e["id"] == "e06_auto_cron")
            ev_cron["params"]["schedule"] = "RRULE:FREQ=DAILY;INTERVAL=2"

        self.assertEqual(self._mutate_and_validate(valid_optional_fields), [])

        def bad_context_from(m):
            ev_sim = next(e for e in m["events"] if e["id"] == "e23_sim_reset")
            ev_sim["params"]["context_from"] = "2027-01-07"

        self.assertTrue(any("context_from" in e for e in self._mutate_and_validate(bad_context_from)))

        def bad_library_collection(m):
            ev_lib = next(e for e in m["events"] if e["id"] == "e13_lib_create")
            ev_lib["params"]["collection"] = "   "

        self.assertTrue(any("collection" in e for e in self._mutate_and_validate(bad_library_collection)))

        def bad_schedule_hourly_rrule(m):
            ev_auto = next(e for e in m["events"] if e["id"] == "e05_auto_monitor")
            ev_auto["params"]["schedule"] = "FREQ=HOURLY;INTERVAL=4"

        self.assertTrue(any("FREQ" in e for e in self._mutate_and_validate(bad_schedule_hourly_rrule)))

        def bad_schedule_kind_once(m):
            ev_auto = next(e for e in m["events"] if e["id"] == "e05_auto_monitor")
            ev_auto["params"]["kind"] = "once"

        self.assertTrue(any("kind" in e for e in self._mutate_and_validate(bad_schedule_kind_once)))

    def test_goal_ui_update_requires_update_field(self) -> None:
        def selector_only_match_title(m):
            ev = next(e for e in m["events"] if e["id"] == "e03_goal_update")
            ev["params"] = {"match_title": "Launch Beta"}

        self.assertTrue(any("requires at least one update field" in e for e in self._mutate_and_validate(selector_only_match_title)))

        def selector_only_title(m):
            ev = next(e for e in m["events"] if e["id"] == "e03_goal_update")
            ev["params"] = {"title": "Launch Beta"}

        self.assertTrue(any("requires at least one update field" in e for e in self._mutate_and_validate(selector_only_title)))

        def rename_with_selector(m):
            ev = next(e for e in m["events"] if e["id"] == "e03_goal_update")
            ev["params"] = {"match_title": "Launch Beta", "title": "Launch GA"}

        self.assertEqual(self._mutate_and_validate(rename_with_selector), [])


class GraderTests(unittest.TestCase):
    def _make_run_bundle(
        self,
        tmp_path: Path,
        slug: str = "01-maya-lin",
        summary_extra: dict | None = None,
        probes: list[dict] | None = None,
        core_count: int = 50,
        log_days: int = 20,
    ) -> Path:
        run_dir = tmp_path / "run"
        pdir = run_dir / slug
        snaps = pdir / "snapshots"
        snaps.mkdir(parents=True, exist_ok=True)

        core_memories = [
            {"section": "Profile", "about": f"fact_{i}", "pinned": i == 0, "provenance": "extracted", "text": f"Core memory item {i}"}
            for i in range(core_count)
        ]
        long_text = "Detailed event record " + ("alpha " * 60)
        daily_logs = [
            {
                "date": f"2027-01-{d + 1:02d}",
                "entries": [{"provenance": "extracted", "text": f"Day {d + 1} entry {j}: {long_text}"} for j in range(12)],
            }
            for d in range(log_days)
        ]
        ws = {
            "gist": "Summary gist",
            "items": [{"id": "ws1", "kind": "constraint", "quote": "Use $1,200 cap", "input_id": "in1", "note": "budget"}],
            "turns": 18,
            "extracted_through": "2027-01-30T10:00:00Z",
            "flushed_through": "2027-01-30T09:00:00Z",
        }
        (snaps / "z_final_eom.json").write_text(
            json.dumps({
                "invariants_ok": True,
                "invariant_violations": [],
                "core_memories": core_memories,
                "core_bytes": 4096,
                "daily_logs": daily_logs,
                "daily_log_entry_count": log_days * 12,
                "fts_chunk_count": core_count + log_days * 12,
                "working_summary": ws,
            }),
            encoding="utf-8",
        )

        summary = {
            "slug": slug,
            "persona_name": "Maya Lin",
            "events_executed": 54,
            "elapsed_seconds": 12.3,
            "http_5xx_count": 0,
            "harness_timeouts": 0,
            "ui_errors": [],
            "stub_models": False,
            "end_of_month_audit": {
                "rubric": "Verify full month retention.",
                "expected_core_concepts": ["Core memory item 49"],
            },
        }
        if summary_extra:
            summary.update(summary_extra)
        (pdir / "summary.json").write_text(json.dumps(summary), encoding="utf-8")

        if probes is None:
            probes = [
                {
                    "day": 26,
                    "local_time": "10:00",
                    "user_text": "Show cashflow summary",
                    "nori_reply": "See attached summary card.",
                    "show_results": [{"id": "res_1", "kind": "report", "title": "Cashflow", "text": "$47,300 AR"}],
                    "adaptive_followups": [],
                    "mcp_writes_during_event": 0,
                    "fts_result": {"items": [{"id": "1"}]},
                    "core_memories": core_memories,
                    "daily_logs": daily_logs,
                    "working_summary": ws,
                    "probe": {
                        "id": "p_1",
                        "category": "recall",
                        "rubric": "Check $47,300 AR (bonus: list shipper names).",
                        "expect_fts_query": {"query": "cashflow", "min_hits": 1},
                        "expect_no_external_write": True,
                    },
                }
            ]
        (pdir / "probes.json").write_text(json.dumps(probes), encoding="utf-8")
        (pdir / "mcp_audit.json").write_text(
            json.dumps({
                "call_count": 5,
                "write_count": 1,
                "writes": [{"ts": "2027-01-24T12:00:00Z", "tool": "email_send", "receipt_id": "rcpt_1", "arguments": {"to": "mateo@example.com"}}],
            }),
            encoding="utf-8",
        )
        (pdir / "approvals.json").write_text(
            json.dumps([{"id": "int_1", "kind": "approval", "decision": "approve", "tool": "email_send"}]),
            encoding="utf-8",
        )
        return run_dir

    def test_tier_a_separates_harness_timeouts_and_fails_on_ui_errors(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            run_dir = self._make_run_bundle(
                Path(tmp),
                summary_extra={"http_5xx_count": 0, "harness_timeouts": 3, "ui_errors": []},
            )
            ta = grader._check_tier_a(run_dir / "01-maya-lin")
            self.assertTrue(ta["passed"])
            self.assertEqual(ta["harness_timeouts"], 3)

            run_dir2 = self._make_run_bundle(
                Path(tmp) / "u",
                summary_extra={"http_5xx_count": 0, "ui_errors": ["goal_ui.create failed: 400"]},
            )
            ta2 = grader._check_tier_a(run_dir2 / "01-maya-lin")
            self.assertFalse(ta2["passed"])
            self.assertEqual(ta2["ui_error_count"], 1)
            self.assertTrue(any("UI handler errors" in f for f in ta2["failures"]))

            run_dir3 = self._make_run_bundle(
                Path(tmp) / "h5",
                summary_extra={"http_5xx_count": 1},
            )
            ta3 = grader._check_tier_a(run_dir3 / "01-maya-lin")
            self.assertFalse(ta3["passed"])
            self.assertTrue(any("HTTP 5xx count = 1" in f for f in ta3["failures"]))

    def test_judge_probe_and_eom_untruncated_enriched_context_and_h11_prompt(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            run_dir = self._make_run_bundle(Path(tmp), core_count=50, log_days=20)
            pdir = run_dir / "01-maya-lin"
            probe_rec = json.loads((pdir / "probes.json").read_text(encoding="utf-8"))[0]
            mcp_audit = json.loads((pdir / "mcp_audit.json").read_text(encoding="utf-8"))
            approval_log = grader._extract_approval_log(pdir)

            captured_calls: list[tuple[str, dict]] = []

            class FakeJudge:
                model = "gpt-4.1"

                def chat_json(self, system: str, user: str, temperature: float = 0.0) -> dict:
                    captured_calls.append((system, json.loads(user)))
                    return {"verdict": "PASS", "rationale": "ok", "evidence": "card"}

            res = grader._judge_probe(FakeJudge(), "01-maya-lin", probe_rec, mcp_audit, approval_log)
            self.assertEqual(res["verdict"], "PASS")
            sys_prompt, payload = captured_calls[0]
            self.assertIn("show_results", sys_prompt)
            self.assertIn("bonus", sys_prompt.lower())
            self.assertIn("mcp_audit_writes", sys_prompt)
            self.assertEqual(len(payload["core_memories_snapshot"]), 50)
            self.assertEqual(len(payload["daily_logs"]), 20)
            self.assertEqual(len(payload["daily_logs"][19]["entries"]), 12)
            self.assertGreater(len(payload["daily_logs"][19]["entries"][11]["text"]), 300)
            self.assertEqual(payload["working_summary"]["items"][0]["quote"], "Use $1,200 cap")
            self.assertEqual(payload["show_results"][0]["text"], "$47,300 AR")
            self.assertEqual(len(payload["mcp_audit_writes"]), 1)
            self.assertEqual(len(payload["approval_log"]), 1)

            summary = json.loads((pdir / "summary.json").read_text(encoding="utf-8"))
            snap = json.loads((pdir / "snapshots" / "z_final_eom.json").read_text(encoding="utf-8"))
            grader._judge_eom_audit(FakeJudge(), "01-maya-lin", summary, snap, mcp_audit, approval_log)
            _, eom_payload = captured_calls[1]
            self.assertEqual(len(eom_payload["daily_log_dates"]), 20)
            self.assertEqual(len(eom_payload["daily_log_dates"][0]["entries"]), 12)
            self.assertEqual(len(eom_payload["working_summary"]["items"]), 1)
            self.assertEqual(len(eom_payload["mcp_audit_writes"]), 1)

    def test_inconclusive_probe_skipped_and_retry_error_denominator_exclusion(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            p_base = {
                "day": 10,
                "local_time": "10:00",
                "user_text": "Check",
                "nori_reply": "I've accepted your task.",
                "fts_result": None,
                "core_memories": [],
                "daily_logs": [],
                "working_summary": {"gist": "", "items": []},
            }
            probes = [
                {**p_base, "inconclusive": True, "probe": {"id": "p_incl", "category": "recall", "rubric": "Check timeout probe handling.", "expect_fts_query": {"query": "x", "min_hits": 1}}},
                {**p_base, "inconclusive": False, "fts_result": {"items": [{"id": "1"}]}, "probe": {"id": "p_ok", "category": "recall", "rubric": "Check normal probe passes."}},
                {**p_base, "inconclusive": False, "fts_result": {"items": [{"id": "1"}]}, "probe": {"id": "p_err", "category": "recall", "rubric": "Check persistent error probe."}},
            ]
            run_dir = self._make_run_bundle(Path(tmp), probes=probes, summary_extra={"harness_timeouts": 1})

            call_counts: dict[str, int] = {}

            class FlakyJudge:
                model = "gpt-4.1"

                def chat_json(self, system: str, user: str, temperature: float = 0.0) -> dict:
                    payload = json.loads(user)
                    pid = payload.get("probe_id", "eom")
                    call_counts[pid] = call_counts.get(pid, 0) + 1
                    if pid == "p_ok" and call_counts[pid] < 3:
                        raise urllib.error.URLError("transient reset")
                    if pid == "p_err":
                        raise RuntimeError("LLM API 401: bad key")
                    return {"verdict": "PASS", "rationale": "ok", "evidence": "e"}

            sleep_calls: list[float] = []
            orig_retry = grader._judge_with_retry

            def fast_retry(fn, *args, **kwargs):
                return orig_retry(fn, *args, sleep_fn=sleep_calls.append, **kwargs)

            with mock.patch.object(grader.LLMClient, "from_env", return_value=FlakyJudge()):
                with mock.patch.object(grader, "_judge_with_retry", side_effect=fast_retry):
                    sc = grader.grade_run(run_dir)

            self.assertNotIn("p_incl", call_counts)
            self.assertEqual(call_counts["p_ok"], 3)
            self.assertEqual(call_counts["p_err"], 1)  # 401 fails fast without sleeping
            self.assertEqual(sleep_calls, [2.0, 4.0])

            p_rep = sc["personas"][0]
            self.assertTrue(p_rep["tier_a"]["passed"])
            self.assertEqual(p_rep["tier_b_error_count"], 1)
            self.assertEqual(p_rep["tier_b_inconclusive_count"], 1)
            self.assertEqual(p_rep["tier_b_scored_count"], 2)  # p_ok + eom
            self.assertEqual(p_rep["tier_b_score"], 1.0)
            by_id = {it["id"]: it for it in p_rep["tier_b_items"]}
            self.assertEqual(by_id["p_incl"]["verdict"], "INCONCLUSIVE")
            self.assertIsNone(by_id["p_incl"]["score"])
            self.assertEqual(by_id["p_err"]["verdict"], "ERROR")
            self.assertIsNone(by_id["p_err"]["score"])

    def test_extract_approval_log_deduplicates_overlapping_sources(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            pdir = Path(tmp) / "04-carlos-mendoza"
            pdir.mkdir(parents=True)
            int_1 = {"id": "int_1", "kind": "approval", "status": "approved", "prompt": "Send email to Mateo"}
            int_2 = {"id": "int_2", "kind": "question", "status": "answered", "prompt": "Which account?"}
            int_3 = {"id": "int_3", "kind": "approval", "status": "expired", "prompt": "Send cancel email"}
            (pdir / "interactions.json").write_text(json.dumps([int_1, int_2, int_3]), encoding="utf-8")
            (pdir / "approvals.json").write_text(json.dumps([int_1, int_3]), encoding="utf-8")
            summary = {
                "interactions": [int_1, int_2, int_3],
                "approvals": [int_1, int_3],
            }
            trace_rows = [
                {
                    "kind": "interaction_reply",
                    "id": "e10_approve_mateo",
                    "day": 10,
                    "local_time": "10:00",
                    "action": "approve",
                    "result": [{"id": "int_1", "kind": "approve", "http_status": 200}],
                },
                {
                    "kind": "interaction_reply",
                    "id": "e17_stale_cancel",
                    "day": 17,
                    "local_time": "13:10",
                    "action": "decline",
                    "result": {"matched": True, "stale_verified": True, "expired_id": "int_3", "http_status": 409},
                },
                {
                    "kind": "chat",
                    "id": "e12_chat",
                    "day": 12,
                    "local_time": "09:00",
                    "adaptive_followups": [
                        {"type": "interaction", "interaction_id": "int_2", "kind": "answer", "text": "Checking"},
                        {"type": "interaction", "interaction_id": "int_4", "kind": "approve", "text": "Extra"},
                    ],
                },
            ]
            (pdir / "trace.jsonl").write_text(
                "\n".join(json.dumps(r) for r in trace_rows) + "\n",
                encoding="utf-8",
            )

            log = grader._extract_approval_log(pdir, summary)
            # int_1, int_2, int_3 (once each) + e17_stale_cancel (stale_verified) + int_4 (not in interactions.json)
            self.assertEqual(len(log), 5)
            ids = [x.get("id") or x.get("interaction_id") for x in log]
            self.assertEqual(ids.count("int_1"), 1)
            self.assertEqual(ids.count("int_2"), 1)
            self.assertEqual(ids.count("int_3"), 1)
            self.assertIn("e17_stale_cancel", ids)
            self.assertIn("int_4", ids)


class SuiteTests(unittest.TestCase):
    def test_area_selection_and_cli_precedence(self) -> None:
        self.assertEqual(suite.select_area_personas(["smoke"]), ["01-maya-lin"])
        self.assertEqual(
            suite.select_area_personas(["memory"]),
            ["01-maya-lin", "03-priya-subramanian", "05-hannah-okafor", "09-sora-takahashi"],
        )
        self.assertEqual(
            suite.select_area_personas(["permissions", "goals"]),
            ["02-devon-brooks", "04-carlos-mendoza", "08-tariq-al-mansoor", "09-sora-takahashi"],
        )
        self.assertEqual(len(suite.select_area_personas(["full"])), 10)
        with self.assertRaises(ValueError):
            suite.select_area_personas(["nonexistent"])

    def test_preflight_stub_gateway_openai_direct_and_openrouter(self) -> None:
        # 1. --stub-models skips live preflight completely
        with mock.patch("urllib.request.urlopen") as m_open:
            suite.preflight_credits(15.0, extra=["--stub-models"], env={})
            m_open.assert_not_called()

        # 2. Gateway mode requires gateway settings and skips OpenAI/OpenRouter HTTP calls
        with self.assertRaises(SystemExit):
            suite.preflight_credits(15.0, env={"NORI_MODEL_PROVIDER": "gateway"})
        with mock.patch("urllib.request.urlopen") as m_open:
            suite.preflight_credits(
                15.0,
                env={
                    "NORI_MODEL_PROVIDER": "gateway",
                    "NORI_MODEL_GATEWAY_URL": "https://gw.example.com/v1",
                    "NORI_MODEL_GATEWAY_API_KEY": "tok_123",
                    "NORI_AGENT_MODEL": "gw-sol",
                    "MODEL_NAME": "gw-terra",
                },
            )
            m_open.assert_not_called()

        # 3. Missing OPENAI_API_KEY aborts
        with self.assertRaises(SystemExit):
            suite.preflight_credits(15.0, env={"OPENROUTER_API_KEY": "or_123"})

        # 4. Direct OpenAI inner (MODEL_NAME=gpt-6.1-terra) skips OpenRouter credits
        called_urls: list[str] = []

        class OkResp:
            status = 200

            def __enter__(self):
                return self

            def __exit__(self, *a):
                return False

            def read(self):
                return b'{"data":{"total_credits":50,"total_usage":10}}'

        def fake_urlopen(req, timeout=20):
            called_urls.append(req.full_url)
            return OkResp()

        with mock.patch("urllib.request.urlopen", side_effect=fake_urlopen):
            suite.preflight_credits(15.0, env={"OPENAI_API_KEY": "oa_123", "MODEL_NAME": "gpt-6.1-terra"})
        self.assertEqual(called_urls, ["https://api.openai.com/v1/chat/completions"])

        # 5. OpenRouter inner checks credits + OpenRouter probe and aborts on low credits
        called_urls.clear()
        with mock.patch("urllib.request.urlopen", side_effect=fake_urlopen):
            suite.preflight_credits(15.0, env={"OPENAI_API_KEY": "oa_123", "OPENROUTER_API_KEY": "or_123", "MODEL_NAME": "openai/gpt-6.1-terra"})
        self.assertEqual(
            called_urls,
            [
                "https://api.openai.com/v1/chat/completions",
                "https://openrouter.ai/api/v1/credits",
                "https://openrouter.ai/api/v1/chat/completions",
            ],
        )

        def low_credits_urlopen(req, timeout=20):
            if "credits" in req.full_url:
                resp = OkResp()
                resp.read = lambda: b'{"data":{"total_credits":20,"total_usage":12}}'
                return resp
            return OkResp()

        with mock.patch("urllib.request.urlopen", side_effect=low_credits_urlopen):
            with self.assertRaises(SystemExit):
                suite.preflight_credits(15.0, env={"OPENAI_API_KEY": "oa_123", "OPENROUTER_API_KEY": "or_123"})

    def test_preflight_stale_nori_task_model_does_not_bypass_openrouter(self) -> None:
        probed: list[tuple[str, dict | None]] = []

        class OkResp:
            status = 200

            def __enter__(self):
                return self

            def __exit__(self, *a):
                return False

            def read(self):
                return b'{"data":{"total_credits":50,"total_usage":10}}'

        def capture_urlopen(req, timeout=20):
            body = json.loads(req.data.decode("utf-8")) if req.data else None
            probed.append((req.full_url, body))
            return OkResp()

        # Stale NORI_TASK_MODEL=gpt-6.1-terra with MODEL_NAME unset MUST NOT bypass OpenRouter preflight
        with mock.patch("urllib.request.urlopen", side_effect=capture_urlopen):
            with self.assertRaises(SystemExit) as ctx:
                suite.preflight_credits(
                    15.0,
                    env={"OPENAI_API_KEY": "oa_123", "NORI_TASK_MODEL": "gpt-6.1-terra"},
                )
            self.assertIn("OPENROUTER_API_KEY", str(ctx.exception))
            self.assertEqual([u for u, _ in probed], ["https://api.openai.com/v1/chat/completions"])

        # When OPENROUTER_API_KEY is present, it probes OpenRouter with default openai/gpt-6.1-terra
        probed.clear()
        with mock.patch("urllib.request.urlopen", side_effect=capture_urlopen):
            suite.preflight_credits(
                15.0,
                env={
                    "OPENAI_API_KEY": "oa_123",
                    "OPENROUTER_API_KEY": "or_123",
                    "NORI_TASK_MODEL": "gpt-6.1-terra",
                },
            )
        self.assertEqual(
            [u for u, _ in probed],
            [
                "https://api.openai.com/v1/chat/completions",
                "https://openrouter.ai/api/v1/credits",
                "https://openrouter.ai/api/v1/chat/completions",
            ],
        )
        self.assertEqual(probed[2][1]["model"], "openai/gpt-6.1-terra")

    def test_model_usage_aggregation_and_max_usd_collect_only(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            p1 = root / "01-maya-lin"
            p1.mkdir(parents=True)
            (p1 / "summary.json").write_text(json.dumps({
                "slug": "01-maya-lin",
                "http_5xx_count": 0,
                "model_usage": {
                    "total_input_tokens": 10000,
                    "total_output_tokens": 500,
                    "total_cached_tokens": 8000,
                    "total_cache_write_tokens": 1000,
                    "total_cost_usd": 1.25,
                    "by_model": {
                        "gpt-6.1-sol": {"provider": "openai", "calls": 5, "input_tokens": 10000, "output_tokens": 500, "cached_tokens": 8000, "cache_write_tokens": 1000, "cost_usd": 1.25}
                    },
                },
            }), encoding="utf-8")

            p2 = root / "02-devon-brooks"
            (p2 / "snapshots").mkdir(parents=True)
            (p2 / "summary.json").write_text(json.dumps({"slug": "02-devon-brooks", "http_5xx_count": 0}), encoding="utf-8")
            (p2 / "snapshots" / "z_final_eom.json").write_text(json.dumps({
                "model_usage": [
                    {"model": "openai/gpt-6.1-terra", "provider": "openrouter", "input_tokens": 2000, "output_tokens": 300, "cached_tokens": 0, "cache_write_tokens": 0, "cost_usd": 0.45},
                    {"model": "gpt-6.1-sol", "provider": "openai", "input_tokens": 4000, "output_tokens": 200, "cached_tokens": 2000, "cache_write_tokens": 500, "cost_usd": 0.60},
                ]
            }), encoding="utf-8")

            agg = suite.aggregate_suite_usage([("01-maya-lin", p1), ("02-devon-brooks", p2)])
            self.assertEqual(agg["total_input_tokens"], 16000)
            self.assertEqual(agg["total_cached_tokens"], 10000)
            self.assertEqual(agg["total_cache_write_tokens"], 1500)
            self.assertEqual(agg["total_output_tokens"], 1000)
            self.assertAlmostEqual(agg["total_cost_usd"], 2.30, places=4)
            self.assertAlmostEqual(agg["by_model"]["gpt-6.1-sol"]["cost_usd"], 1.85, places=4)

            table = suite.format_cost_table(agg)
            self.assertIn("01-maya-lin", table)
            self.assertIn("TOTAL", table)
            self.assertIn("62.5%", table)

            # End-to-end suite.main --collect-only --no-grade --max-usd 2.00 -> exit 1
            runs_dir = root / ".sim-runs"
            with mock.patch.object(suite, "RUNS_DIR", runs_dir):
                with mock.patch.object(sys, "argv", [
                    "suite.py", "--collect-only", str(p1), str(p2),
                    "--no-grade", "--max-usd", "2.00", "--tag", "budget-test",
                ]):
                    buf = io.StringIO()
                    with redirect_stdout(buf):
                        rc = suite.main()
            self.assertEqual(rc, 1)
            self.assertIn("BUDGET EXCEEDED", buf.getvalue())
            manifests = list(runs_dir.glob("*-budget-test/suite_manifest.json"))
            self.assertEqual(len(manifests), 1)
            mdata = json.loads(manifests[0].read_text(encoding="utf-8"))
            self.assertTrue(mdata["budget_exceeded"])
            self.assertAlmostEqual(mdata["cost_summary"]["total_cost_usd"], 2.30, places=4)

    def test_rejudge_errors_keeps_error_and_inconclusive_excluded(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            run_dir = Path(tmp) / "run"
            pdir = run_dir / "01-maya-lin"
            (pdir / "snapshots").mkdir(parents=True)
            (pdir / "summary.json").write_text(json.dumps({"slug": "01-maya-lin"}), encoding="utf-8")
            (pdir / "mcp_audit.json").write_text(json.dumps({"writes": []}), encoding="utf-8")
            (pdir / "probes.json").write_text(json.dumps([
                {"day": 5, "local_time": "10:00", "user_text": "hi", "nori_reply": "ok", "probe": {"id": "p1", "category": "recall", "rubric": "Must work."}},
                {"day": 6, "local_time": "10:00", "user_text": "hi2", "nori_reply": "ok2", "probe": {"id": "p2", "category": "recall", "rubric": "Must fail."}},
            ]), encoding="utf-8")
            (pdir / "snapshots" / "z.json").write_text(json.dumps({"core_memories": [], "daily_logs": []}), encoding="utf-8")

            sc = {
                "run_dir": str(run_dir),
                "judge_model": "gpt-4.1",
                "all_tier_a_passed": True,
                "overall_semantic_score": 1.0,
                "personas": [{
                    "slug": "01-maya-lin",
                    "name": "Maya",
                    "events_executed": 2,
                    "elapsed_seconds": 1.0,
                    "stub_models": False,
                    "tier_a": {"passed": True, "checks_passed": 1, "checks_total": 1, "failures": [], "final_core_count": 0, "final_core_bytes": 0, "final_daily_log_entries": 0, "final_fts_chunks": 0, "mcp_call_count": 0, "mcp_write_count": 0},
                    "tier_b_status": "GRADED",
                    "tier_b_score": None,
                    "tier_b_items": [
                        {"id": "p1", "category": "recall", "day": 5, "verdict": "ERROR", "score": None, "rationale": "net"},
                        {"id": "p2", "category": "recall", "day": 6, "verdict": "ERROR", "score": None, "rationale": "net"},
                        {"id": "p3", "category": "recall", "day": 7, "verdict": "INCONCLUSIVE", "score": None, "rationale": "504"},
                    ],
                }],
            }
            (run_dir / "scorecard.json").write_text(json.dumps(sc), encoding="utf-8")

            class PartialRejudge:
                model = "gpt-4.1"

                def chat_json(self, system: str, user: str, temperature: float = 0.0) -> dict:
                    if json.loads(user)["probe_id"] == "p1":
                        return {"verdict": "PASS", "rationale": "ok", "evidence": "ok"}
                    raise RuntimeError("still down")

            with mock.patch.object(suite.LLMClient, "from_env", return_value=PartialRejudge()):
                with mock.patch("time.sleep"):
                    redone = suite.rejudge_errors(run_dir)

            self.assertEqual(len(redone), 1)
            updated = json.loads((run_dir / "scorecard.json").read_text(encoding="utf-8"))
            self.assertEqual(updated["personas"][0]["tier_b_score"], 1.0)
            self.assertEqual(updated["personas"][0]["tier_b_scored_count"], 1)
            self.assertEqual(updated["personas"][0]["tier_b_error_count"], 1)
            self.assertEqual(updated["personas"][0]["tier_b_inconclusive_count"], 1)

    def test_rejudge_errors_updates_baseline_diff(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            run_dir = Path(tmp) / "run"
            pdir = run_dir / "01-maya-lin"
            (pdir / "snapshots").mkdir(parents=True)
            (pdir / "summary.json").write_text(json.dumps({"slug": "01-maya-lin"}), encoding="utf-8")
            (pdir / "mcp_audit.json").write_text(json.dumps({"writes": []}), encoding="utf-8")
            (pdir / "probes.json").write_text(json.dumps([
                {"day": 5, "local_time": "10:00", "user_text": "hi", "nori_reply": "ok", "probe": {"id": "p1", "category": "recall", "rubric": "Must work."}},
                {"day": 6, "local_time": "10:00", "user_text": "hi2", "nori_reply": "ok2", "probe": {"id": "p2", "category": "recall", "rubric": "Must work too."}},
            ]), encoding="utf-8")
            (pdir / "snapshots" / "z.json").write_text(json.dumps({"core_memories": [], "daily_logs": []}), encoding="utf-8")

            sc = {
                "run_dir": str(run_dir),
                "judge_model": "gpt-4.1",
                "all_tier_a_passed": True,
                "overall_semantic_score": 1.0,
                "personas": [{
                    "slug": "01-maya-lin",
                    "name": "Maya",
                    "events_executed": 2,
                    "elapsed_seconds": 1.0,
                    "stub_models": False,
                    "tier_a": {"passed": True, "checks_passed": 1, "checks_total": 1, "failures": [], "final_core_count": 0, "final_core_bytes": 0, "final_daily_log_entries": 0, "final_fts_chunks": 0, "mcp_call_count": 0, "mcp_write_count": 0},
                    "tier_b_status": "GRADED",
                    "tier_b_score": 1.0,
                    "tier_b_items": [
                        {"id": "p1", "category": "recall", "day": 5, "verdict": "ERROR", "score": None, "rationale": "net"},
                        {"id": "p2", "category": "recall", "day": 6, "verdict": "PASS", "score": 1.0, "rationale": "ok"},
                    ],
                }],
                "baseline_diff": {
                    "baseline": "base/scorecard.json",
                    "personas": [{
                        "slug": "01-maya-lin",
                        "tier_a_before": True,
                        "tier_a_after": True,
                        "semantic_before": 0.5,
                        "semantic_after": 1.0,
                        "delta": 0.5,
                    }],
                },
            }
            (run_dir / "scorecard.json").write_text(json.dumps(sc), encoding="utf-8")

            class PartialJudge:
                model = "gpt-4.1"

                def chat_json(self, system: str, user: str, temperature: float = 0.0) -> dict:
                    return {"verdict": "PARTIAL", "rationale": "half", "evidence": "part"}

            with mock.patch.object(suite.LLMClient, "from_env", return_value=PartialJudge()):
                redone = suite.rejudge_errors(run_dir)

            self.assertEqual(len(redone), 1)
            updated = json.loads((run_dir / "scorecard.json").read_text(encoding="utf-8"))
            self.assertEqual(updated["personas"][0]["tier_b_score"], 0.75)
            diff_row = updated["baseline_diff"]["personas"][0]
            self.assertEqual(diff_row["semantic_after"], 0.75)
            self.assertEqual(diff_row["delta"], 0.25)
            md = (run_dir / "scorecard.md").read_text(encoding="utf-8")
            self.assertIn("## Diff vs Baseline", md)
            self.assertIn("+25.0%", md)

    def test_resolve_suite_env_refuses_dev_key_unless_overridden(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            (root / ".env").write_text("OPENAI_API_KEY=sk-dev-123\nOPENROUTER_API_KEY=sk-or-dev\n", encoding="utf-8")
            # Refuses default local dev key when no .env.vst or *_TEST_API_KEY is present.
            with self.assertRaises(SystemExit) as cm:
                suite.resolve_suite_env(root=root, environ={})
            self.assertIn("refusing to run persona-sim against the default local dev key", str(cm.exception))

            # Explicit --allow-dev-key allows it.
            allowed = suite.resolve_suite_env(allow_dev_key=True, root=root, environ={})
            self.assertEqual(allowed["OPENAI_API_KEY"], "sk-dev-123")

            # Dedicated *_TEST_API_KEY succeeds without --allow-dev-key and never leaks dev keys from .env.
            isolated = suite.resolve_suite_env(
                root=root,
                environ={"OPENAI_TEST_API_KEY": "sk-test-999"},
            )
            self.assertEqual(isolated["OPENAI_API_KEY"], "sk-test-999")
            self.assertNotIn("OPENROUTER_API_KEY", isolated)

            # Reusing the same key value as dev .env is rejected.
            with self.assertRaises(SystemExit) as cm2:
                suite.resolve_suite_env(
                    root=root,
                    environ={"OPENAI_TEST_API_KEY": "sk-dev-123"},
                )
            self.assertIn("matches the default local dev key", str(cm2.exception))

            # .env.vst succeeds cleanly.
            (root / ".env.vst").write_text("OPENAI_API_KEY=sk-vst-555\n", encoding="utf-8")
            vst = suite.resolve_suite_env(root=root, environ={})
            self.assertEqual(vst["OPENAI_API_KEY"], "sk-vst-555")

    def test_gateway_child_env_strips_raw_keys_and_llm_client_routes_via_gateway(self) -> None:
        gw = suite.LocalGateway(upstream_env={"OPENAI_TEST_API_KEY": "sk-raw-openai"})
        gw.url = "http://127.0.0.1:19999/v1"
        base_env = {
            "PATH": "/usr/bin",
            "OPENAI_API_KEY": "sk-raw-openai",
            "OPENAI_TEST_API_KEY": "sk-raw-openai-test",
            "OPENROUTER_API_KEY": "sk-raw-or",
            "ANTHROPIC_API_KEY": "sk-raw-ant",
            "SIM_JUDGE_API_KEY": "sk-raw-judge",
        }
        child = gw.child_env(base_env, "gpt-6.1-sol", "openai/gpt-6.1-terra")
        for forbidden in (
            "OPENAI_API_KEY",
            "OPENAI_TEST_API_KEY",
            "OPENROUTER_API_KEY",
            "ANTHROPIC_API_KEY",
            "SIM_JUDGE_API_KEY",
        ):
            self.assertNotIn(forbidden, child)
        self.assertEqual(child["NORI_MODEL_PROVIDER"], "gateway")
        self.assertEqual(child["NORI_MODEL_GATEWAY_URL"], gw.url)
        self.assertEqual(child["NORI_MODEL_GATEWAY_API_KEY"], gw.token)
        self.assertEqual(child["SIM_GATEWAY_URL"], gw.url)
        self.assertEqual(child["SIM_GATEWAY_TOKEN"], gw.token)

        with mock.patch.dict(os.environ, child, clear=True):
            resp_client = suite.LLMClient.from_env("responder", dotenv={"OPENAI_API_KEY": "sk-should-be-ignored"})
            judge_client = suite.LLMClient.from_env("judge", dotenv={"OPENAI_API_KEY": "sk-should-be-ignored"})
        self.assertIsNotNone(resp_client)
        self.assertIsNotNone(judge_client)
        assert resp_client is not None and judge_client is not None
        self.assertEqual(resp_client.base_url, gw.url)
        self.assertEqual(resp_client.api_key, gw.token)
        self.assertEqual(resp_client.model, "gpt-6-luna")
        self.assertEqual(judge_client.base_url, gw.url)
        self.assertEqual(judge_client.api_key, gw.token)
        self.assertEqual(judge_client.model, "gpt-6-luna")

    def test_horizon_resolution_core_preset_and_eom_skip(self) -> None:
        self.assertEqual(suite.resolve_max_days(None, 1), 7)
        self.assertEqual(suite.resolve_max_days(None, 2), 14)
        self.assertEqual(suite.resolve_max_days(None, 3), 21)
        self.assertIsNone(suite.resolve_max_days(None, 4))
        self.assertEqual(suite.resolve_max_days(10, None), 10)
        with self.assertRaises(ValueError):
            suite.resolve_max_days(7, 1)
        with self.assertRaises(ValueError):
            suite.resolve_max_days(0, None)

        core = suite.select_personas("core", None)
        self.assertEqual(
            core,
            [
                "02-devon-brooks",
                "03-priya-subramanian",
                "04-carlos-mendoza",
                "08-tariq-al-mansoor",
                "09-sora-takahashi",
            ],
        )
        self.assertEqual(suite.select_area_personas(["core"]), core)

        with tempfile.TemporaryDirectory() as tmp:
            run_dir = GraderTests()._make_run_bundle(
                Path(tmp),
                summary_extra={"max_days": 7, "horizon_truncated": True},
            )
            called_ids: list[str] = []

            class FakeJudge:
                model = "gpt-6-luna"

                def chat_json(self, system: str, user: str, temperature: float = 0.0) -> dict:
                    payload = json.loads(user)
                    called_ids.append(payload.get("probe_id", "eom"))
                    return {"verdict": "PASS", "rationale": "ok", "evidence": "e"}

            with mock.patch.object(grader.LLMClient, "from_env", return_value=FakeJudge()):
                sc = grader.grade_run(run_dir)
            self.assertNotIn("eom", called_ids)
            self.assertEqual(sc["personas"][0]["max_days"], 7)
            self.assertTrue(sc["personas"][0]["horizon_truncated"])

    def test_luna_chat_json_and_polite_closing_offer_filter(self) -> None:
        from llm_client import AdaptivePersonaResponder, LLMClient, _is_polite_closing_offer_only

        self.assertTrue(_is_polite_closing_offer_only("Done! Would you like me to draft an email?"))
        self.assertTrue(_is_polite_closing_offer_only("All set. Anything else I can help with?"))
        self.assertFalse(_is_polite_closing_offer_only("Which recipient should I send this to?"))
        self.assertFalse(_is_polite_closing_offer_only("Should I use the May or June budget? Let me know if you want a PDF too."))

        called = {"n": 0}

        class SpyClient:
            def chat_json(self, *args, **kwargs):
                called["n"] += 1
                return {"needs_reply": True, "reply": "yes"}

        resp = AdaptivePersonaResponder(SpyClient(), "brief", "Maya")
        self.assertIsNone(resp.decide_chat_followup("Save this", "Saved! Would you like me to do anything else?", 1, "09:00"))
        self.assertEqual(called["n"], 0)

        captured_req: dict[str, object] = {}

        class FakeResp:
            def __enter__(self):
                return self

            def __exit__(self, *args):
                return False

            def read(self) -> bytes:
                return json.dumps({"choices": [{"message": {"content": "{\"ok\": true}"}}]}).encode("utf-8")

        def fake_urlopen(req, timeout=90):
            captured_req["body"] = json.loads(req.data.decode("utf-8"))
            return FakeResp()

        client = LLMClient("tok", "http://127.0.0.1:9999/v1", "gpt-6-luna", scope="sim_judge")
        with mock.patch("urllib.request.urlopen", side_effect=fake_urlopen):
            out = client.chat_json("sys", "usr", temperature=0.0)
        self.assertEqual(out, {"ok": True})
        body = captured_req["body"]
        assert isinstance(body, dict)
        self.assertNotIn("temperature", body)
        self.assertEqual(body["reasoning_effort"], "low")


if __name__ == "__main__":
    unittest.main()
