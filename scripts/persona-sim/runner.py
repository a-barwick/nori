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
from contextlib import ExitStack
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Callable
from urllib.parse import quote
from uuid import uuid4
from zoneinfo import ZoneInfo

from db_env import (
    CORE,
    PERSONAS_DIR,
    ROOT,
    RUNS_DIR,
    _clip_utf8,
    _db_init_lock,
    _parse_iso_utc,
    _supports_user_id_flag,
    build_binaries,
    ensure_ephemeral_postgres,
    http_json,
    persona_runtime_env,
    pick_free_port,
    run_cmd,
)
from event_handlers import (
    _CRON_DOW_ORDER,
    _CRON_DOW_TO_BYDAY,
    _VALID_ACTION_CLASSES,
    _build_automation_trigger,
    _collect_strings,
    _compute_edited_memory_text,
    _exec_automation_ui,
    _exec_goal_ui,
    _exec_interaction_reply,
    _exec_library_ui,
    _exec_memory_ui,
    _exec_permission_ui,
    _interaction_matches,
    _library_item_matches,
    _match_query,
    _narrow_library_matches,
    _next_event_handles_interaction,
    _normalize_action_classes,
    _normalize_completion_criteria,
    _normalize_deadline,
    _parse_cron_dow,
    _parse_cron_num_field,
    _with_http_status,
)
from grader import grade_run
from llm_client import (
    AdaptivePersonaResponder,
    LLMClient,
    load_dotenv,
    resolve_dotenv_path,
    strip_raw_provider_keys,
)
from mcp_sandbox import READ_ONLY_TOOL_NAMES, SandboxMCPServer
from stub_server import (
    _extract_chat_user_text,
    _extract_responses_text,
    _extract_stub_email,
    _flatten_chat_content,
    _stub_autoreview_decision,
    build_stub_model_server,
)


def compute_event_utc(sim_start_date: str, day: int, local_time: str, tz_name: str) -> datetime:
    base = datetime.strptime(sim_start_date, "%Y-%m-%d").date()
    ev_date = base + timedelta(days=day - 1)
    hh, mm = map(int, local_time.split(":"))
    tz = ZoneInfo(tz_name)
    dt_local = datetime(ev_date.year, ev_date.month, ev_date.day, hh, mm, tzinfo=tz)
    return dt_local.astimezone(ZoneInfo("UTC"))


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
            return (uncached * 2.0 + cached * 0.10 + cache_write * 2.50 + out_tok * 7.50) / 1_000_000.0
        return (uncached * 1.0 + cached * 0.05 + cache_write * 1.25 + out_tok * 5.0) / 1_000_000.0
    if "gpt-6-luna" in model or "gpt-6.1-luna" in model or "gpt-6.2-luna" in model:
        if inp > 272_000:
            return (uncached * 0.20 + cached * 0.02 + cache_write * 0.25 + out_tok * 0.75) / 1_000_000.0
        return (uncached * 0.10 + cached * 0.01 + cache_write * 0.125 + out_tok * 0.50) / 1_000_000.0
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


def resolve_max_days(max_days: int | None = None, weeks: int | None = None) -> int | None:
    """Resolve --max-days / --weeks into an inclusive simulated day cap (None = full 30-day month)."""
    if max_days is not None and weeks is not None:
        raise ValueError("Pass either --max-days or --weeks, not both.")
    if max_days is not None:
        if max_days < 1:
            raise ValueError(f"--max-days must be >= 1, got {max_days}")
        return max_days
    if weeks is not None:
        if weeks < 1:
            raise ValueError(f"--weeks must be >= 1, got {weeks}")
        return None if weeks >= 4 else weeks * 7
    return None


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
    max_days: int | None = None,
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
            user_id = str(uuid4())
            if nori_user_bin:
                env = strip_raw_provider_keys(dict(os.environ))
                env.update(strip_raw_provider_keys(dotenv))
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

            env = persona_runtime_env(dotenv, effective_pg_url, artifacts_dir, cred_key_file, profile["email"], port, stub_model_url)

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

        st_pref, res_pref = apply_preferences(base_url, user_id, profile.get("preferences") or {})
        if st_pref >= 500:
            http_5xx_count += 1
        if st_pref >= 400:
            raise RuntimeError(f"Initial apply_preferences failed ({st_pref}): {res_pref}. Runtime log: {out_dir / 'nori-sim.log'}")

        tool_ids = connect_sandbox_mcp(
            base_url, user_id, mcp_url,
            (profile.get("mcp_world") or {}).get("server_name") or f"{profile['name']} Workspace",
            profile.get("initial_permissions") or [],
        )
        st_intro, res_intro = http_json(base_url, "GET", f"/users/{user_id}/v1/snapshot")
        if st_intro >= 500:
            http_5xx_count += 1
        if st_intro >= 400:
            raise RuntimeError(f"Initial snapshot failed ({st_intro}): {res_intro}")
        _drain(60)

        all_events = month["events"]
        events = list(all_events)
        if max_days is not None:
            events = [ev for ev in events if int(ev["day"]) <= max_days]
        if max_events is not None:
            events = events[:max_events]
        horizon_truncated = len(events) < len(all_events)

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

        if has_sim_api:
            _advance_clock(1200)
            _drain(240)
            final_snap = _get_state()
            (snaps_dir / "z_final_eom.json").write_text(json.dumps(final_snap, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

        interactions_all = _fetch_all_interactions(base_url, user_id)

    raw_usage_records = final_snap.get("model_usage") or []
    model_usage = aggregate_model_usage(raw_usage_records)
    mcp_audit = world.audit()
    approvals_log = [it for it in interactions_all if str(it.get("kind", "")).lower() == "approval"]

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
        "max_days": max_days,
        "horizon_truncated": horizon_truncated,
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


CORE_PRESET_PERSONAS: tuple[str, ...] = (
    "02-devon-brooks",
    "03-priya-subramanian",
    "04-carlos-mendoza",
    "08-tariq-al-mansoor",
    "09-sora-takahashi",
)


def select_personas(preset: str | None, persona: str | None) -> list[str]:
    all_dirs = sorted(d.name for d in PERSONAS_DIR.iterdir() if d.is_dir() and (d / "profile.json").is_file())
    if persona:
        if persona not in all_dirs:
            raise ValueError(f"Unknown persona {persona!r}; available: {', '.join(all_dirs)}")
        return [persona]
    target_preset = preset or "smoke"
    if target_preset == "core":
        return [slug for slug in CORE_PRESET_PERSONAS if slug in all_dirs]
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
    parser.add_argument("--preset", choices=["smoke", "core", "memory-pack", "full"], default=None, help="Persona subset preset")
    parser.add_argument("--persona", help="Run a single persona slug (e.g. 01-maya-lin)")
    parser.add_argument("--concurrency", type=int, default=1, help="Number of personas to run in parallel")
    parser.add_argument("--tag", default="run", help="Label for output folder under .sim-runs/")
    parser.add_argument("--stub-models", action="store_true", help="Use local deterministic model stub (free harness smoke test)")
    parser.add_argument("--max-events", type=int, default=None, help="Optional cap on events per persona (for fast smoke checks)")
    parser.add_argument("--max-days", type=int, default=None, help="Optional cap on simulated days per persona (e.g. 7 for 1 week, 14 for 2 weeks)")
    parser.add_argument("--weeks", type=int, default=None, help="Optional simulated duration in weeks (1=7d, 2=14d, 3=21d, 4=full 30d month)")
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

    effective_max_days = resolve_max_days(args.max_days, args.weeks)
    if args.outer_model:
        os.environ["NORI_AGENT_MODEL"] = args.outer_model
    if args.outer_reasoning:
        os.environ["NORI_OUTER_REASONING_EFFORT"] = args.outer_reasoning
    if args.inner_model:
        os.environ["MODEL_NAME"] = args.inner_model
    if args.inner_reasoning:
        os.environ["NORI_INNER_REASONING_EFFORT"] = args.inner_reasoning

    if args.stub_models and args.workbench_seed:
        parser.error("--stub-models requires an isolated simulation runtime")

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
                max_days=effective_max_days,
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
