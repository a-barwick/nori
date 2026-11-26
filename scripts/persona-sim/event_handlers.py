#!/usr/bin/env python3
"""UI and interaction event execution helpers for persona-sim."""

from __future__ import annotations

import json
import re
from datetime import datetime, timedelta
from typing import Any
from urllib.parse import quote
from uuid import uuid4
from zoneinfo import ZoneInfo

from db_env import _clip_utf8, http_json
from mcp_sandbox import READ_ONLY_TOOL_NAMES


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
