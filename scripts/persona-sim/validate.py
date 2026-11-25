#!/usr/bin/env python3
"""Validate all persona bundles under sim/personas/."""

from __future__ import annotations

import argparse
import json
import re
import sys
from datetime import datetime
from pathlib import Path
from typing import Any
from zoneinfo import ZoneInfo

ROOT = Path(__file__).resolve().parents[2]
PERSONAS_DIR = ROOT / "sim" / "personas"

SLUG_RE = re.compile(r"^[0-9]{2}-[a-z0-9-]+$")
DATE_RE = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}$")
TIME_RE = re.compile(r"^[0-9]{2}:[0-9]{2}$")
ID_RE = re.compile(r"^[a-z0-9_]+$")
LOCAL_START_RE = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}(?::[0-9]{2})?$")
RFC3339_RE = re.compile(
    r"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})$"
)
BYDAY_RE = re.compile(r"^(?:[+-]?[1-5])?(SU|MO|TU|WE|TH|FR|SA)$")
EVERY_HOURS_RE = re.compile(r"^every\s+(\d+)\s+hours?$", re.IGNORECASE)
CRON_DOW_NAMES = {"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}

VALID_PRESETS = {"smoke", "memory-pack", "full"}
VALID_KINDS = {
    "chat",
    "settle",
    "sim_control",
    "memory_ui",
    "preferences_ui",
    "permission_ui",
    "automation_ui",
    "goal_ui",
    "library_ui",
    "interaction_reply",
    "mcp_world_event",
}
VALID_EXTENSIONS = {".pdf", ".csv", ".ics", ".png", ".jpg", ".jpeg", ".webp", ".txt", ".md"}

SELECTOR_KEYS = {"query", "match_title", "title_match", "match_text", "match", "title"}
WEEKDAY_KEYS = {
    "mon", "tue", "wed", "thu", "fri", "sat", "sun",
    "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday",
}
VALID_TRIGGER_KINDS = {"once", "recurring", "event_relative", "watch"}
VALID_MISSED_RUN = {"skip", "latest", "all"}
VALID_FOLLOW_UP = {"", "none", "remind", "monitor"}
VALID_ACTION_CLASSES = {
    "read", "reversible_write", "sensitive_send", "unknown",
    "write", "send", "external_write",
}
VALID_RULE_EFFECTS = {"allow", "ask", "deny"}
VALID_RRULE_FREQS = {"DAILY", "WEEKLY", "MONTHLY", "YEARLY"}
VALID_WEEKDAYS = {"SU", "MO", "TU", "WE", "TH", "FR", "SA"}
VALID_SEARCH_LAYERS = {"", "core", "log"}
VALID_DELETE_SCOPES = {"version", "all_versions"}

MAX_MESSAGE_BYTES = 8192
MAX_INSTRUCTION_BYTES = 7168
MAX_MEMORY_TEXT_BYTES = 2000
MAX_MEMORY_ABOUT_BYTES = 80
MAX_MEMORY_SECTION_BYTES = 64
MAX_MEMORY_SEARCH_BYTES = 400
MAX_TITLE_BYTES = 200


def _is_int(val: Any) -> bool:
    return isinstance(val, int) and not isinstance(val, bool)


def _is_num(val: Any) -> bool:
    return isinstance(val, (int, float)) and not isinstance(val, bool)


def _is_nonempty_str(val: Any) -> bool:
    return isinstance(val, str) and bool(val.strip())


def _is_str_list(val: Any) -> bool:
    return isinstance(val, list) and all(isinstance(x, str) for x in val)


def _utf8_len(val: str) -> int:
    return len(val.encode("utf-8"))


def _check_allowed_keys(obj: dict[str, Any], allowed: set[str], ctx: str, errors: list[str]) -> None:
    unknown = sorted(set(obj.keys()) - allowed)
    if unknown:
        errors.append(f"{ctx}: unknown key(s) {unknown!r}")


def _validate_hhmm(val: Any, ctx: str, errors: list[str]) -> bool:
    if not isinstance(val, str) or not TIME_RE.match(val):
        errors.append(f"{ctx}: invalid HH:MM time {val!r}")
        return False
    hh, mm = int(val[:2]), int(val[3:])
    if hh > 23 or mm > 59:
        errors.append(f"{ctx}: out-of-range HH:MM time {val!r}")
        return False
    return True


def _validate_rfc3339(val: Any, ctx: str, errors: list[str]) -> None:
    if not isinstance(val, str) or not RFC3339_RE.match(val):
        errors.append(f"{ctx}: expected RFC3339 timestamp with timezone offset/Z, got {val!r}")
        return
    try:
        datetime.fromisoformat(val.replace("Z", "+00:00"))
    except ValueError:
        errors.append(f"{ctx}: invalid RFC3339 timestamp {val!r}")


def _validate_date_or_rfc3339(val: Any, ctx: str, errors: list[str]) -> None:
    if not isinstance(val, str) or not val.strip():
        errors.append(f"{ctx}: expected YYYY-MM-DD or RFC3339 timestamp, got {val!r}")
        return
    s = val.strip()
    if DATE_RE.match(s):
        try:
            datetime.strptime(s, "%Y-%m-%d")
        except ValueError:
            errors.append(f"{ctx}: invalid calendar date {val!r}")
        return
    _validate_rfc3339(s, ctx, errors)


def validate_timezone(tz: str, ctx: str, errors: list[str]) -> None:
    if not isinstance(tz, str) or not tz.strip():
        errors.append(f"{ctx}: invalid IANA timezone {tz!r}")
        return
    try:
        ZoneInfo(tz)
    except Exception:
        errors.append(f"{ctx}: invalid IANA timezone {tz!r}")


def _validate_selectors(params: dict[str, Any], ctx: str, errors: list[str], *, required: bool = True) -> bool:
    found_valid = False
    for key in SELECTOR_KEYS:
        if key in params:
            val = params[key]
            if not _is_nonempty_str(val):
                errors.append(f"{ctx}: selector {key!r} must be a non-empty string")
            else:
                found_valid = True
    if required and not found_valid:
        errors.append(f"{ctx}: requires at least one non-empty selector in {sorted(SELECTOR_KEYS)!r}")
    return found_valid


def _validate_rrule(value: Any, ctx: str, errors: list[str]) -> None:
    if not _is_nonempty_str(value):
        errors.append(f"{ctx}: rrule must be a non-empty string")
        return
    raw = value.strip()
    if raw.startswith("RRULE:"):
        raw = raw[len("RRULE:"):]
    freq = ""
    for part in raw.split(";"):
        if "=" not in part:
            errors.append(f"{ctx}: invalid RRULE part {part!r}")
            return
        key, val = part.split("=", 1)
        ukey = key.upper()
        if ukey == "FREQ":
            freq = val.upper()
            if freq not in VALID_RRULE_FREQS:
                errors.append(f"{ctx}: unsupported RRULE FREQ {val!r} (allowed: {sorted(VALID_RRULE_FREQS)})")
                return
        elif ukey == "INTERVAL":
            try:
                iv = int(val)
            except ValueError:
                errors.append(f"{ctx}: invalid RRULE INTERVAL {val!r}")
                return
            if iv < 1 or iv > 1000:
                errors.append(f"{ctx}: RRULE INTERVAL must be 1..1000, got {iv}")
                return
        elif ukey == "BYDAY":
            for item in val.upper().split(","):
                m = BYDAY_RE.match(item)
                if not m:
                    errors.append(f"{ctx}: invalid RRULE BYDAY {item!r}")
                    return
        elif ukey == "BYMONTHDAY":
            for item in val.split(","):
                try:
                    n = int(item)
                except ValueError:
                    errors.append(f"{ctx}: invalid RRULE BYMONTHDAY {item!r}")
                    return
                if n < -31 or n > 31 or n == 0:
                    errors.append(f"{ctx}: out-of-range RRULE BYMONTHDAY {n}")
                    return
        elif ukey == "BYMONTH":
            for item in val.split(","):
                try:
                    n = int(item)
                except ValueError:
                    errors.append(f"{ctx}: invalid RRULE BYMONTH {item!r}")
                    return
                if n < 1 or n > 12:
                    errors.append(f"{ctx}: out-of-range RRULE BYMONTH {n}")
                    return
        elif ukey == "BYHOUR":
            for item in val.split(","):
                try:
                    n = int(item)
                except ValueError:
                    errors.append(f"{ctx}: invalid RRULE BYHOUR {item!r}")
                    return
                if n < 0 or n > 23:
                    errors.append(f"{ctx}: out-of-range RRULE BYHOUR {n}")
                    return
        elif ukey == "BYMINUTE":
            for item in val.split(","):
                try:
                    n = int(item)
                except ValueError:
                    errors.append(f"{ctx}: invalid RRULE BYMINUTE {item!r}")
                    return
                if n < 0 or n > 59:
                    errors.append(f"{ctx}: out-of-range RRULE BYMINUTE {n}")
                    return
        elif ukey == "COUNT":
            try:
                c = int(val)
            except ValueError:
                errors.append(f"{ctx}: invalid RRULE COUNT {val!r}")
                return
            if c < 1:
                errors.append(f"{ctx}: RRULE COUNT must be >= 1")
                return
        elif ukey == "UNTIL":
            parsed = False
            for fmt in ("%Y%m%dT%H%M%SZ", "%Y%m%dT%H%M%S", "%Y%m%d"):
                try:
                    datetime.strptime(val, fmt)
                    parsed = True
                    break
                except ValueError:
                    pass
            if not parsed:
                errors.append(f"{ctx}: invalid RRULE UNTIL {val!r}")
                return
        elif ukey == "WKST":
            if val.upper() not in VALID_WEEKDAYS:
                errors.append(f"{ctx}: invalid RRULE WKST {val!r}")
                return
        else:
            errors.append(f"{ctx}: unsupported RRULE part {key!r}")
            return
    if not freq:
        errors.append(f"{ctx}: RRULE requires FREQ")


def _validate_cron_field(field: str, min_val: int, max_val: int, names: set[str] | None = None) -> bool:
    for part in field.split(","):
        if not part:
            return False
        step_split = part.split("/")
        if len(step_split) > 2:
            return False
        base = step_split[0]
        if len(step_split) == 2:
            if not step_split[1].isdigit() or int(step_split[1]) < 1:
                return False
        if base == "*":
            continue
        if "-" in base:
            bounds = base.split("-")
            if len(bounds) != 2:
                return False
            lo_s, hi_s = bounds
            if names and lo_s.upper() in names and hi_s.upper() in names:
                continue
            if not lo_s.isdigit() or not hi_s.isdigit():
                return False
            lo, hi = int(lo_s), int(hi_s)
            if lo < min_val or hi > max_val or lo > hi:
                return False
        else:
            if names and base.upper() in names:
                continue
            if not base.isdigit():
                return False
            n = int(base)
            if n < min_val or n > max_val:
                return False
    return True


def _validate_schedule_shorthand(schedule: Any, ctx: str, errors: list[str]) -> None:
    if not _is_nonempty_str(schedule):
        errors.append(f"{ctx}: schedule must be a non-empty string")
        return
    s = schedule.strip()
    up = s.upper()
    if up.startswith("FREQ=") or up.startswith("RRULE:"):
        _validate_rrule(s, ctx, errors)
        return
    low = s.lower()
    if low in ("every hour", "daily", "every day"):
        return
    m = EVERY_HOURS_RE.match(low)
    if m:
        hours = int(m.group(1))
        if hours < 1 or hours > 24 or (24 % hours != 0):
            errors.append(f"{ctx}: schedule {schedule!r} hours must divide 24 evenly (1..24)")
        return
    parts = s.split()
    if len(parts) == 5:
        ok = (
            _validate_cron_field(parts[0], 0, 59)
            and _validate_cron_field(parts[1], 0, 23)
            and _validate_cron_field(parts[2], 1, 31)
            and _validate_cron_field(parts[3], 1, 12)
            and _validate_cron_field(parts[4], 0, 7, CRON_DOW_NAMES)
        )
        if ok:
            return
    errors.append(f"{ctx}: unparseable schedule {schedule!r}")


def _validate_trigger(trigger: Any, ctx: str, errors: list[str]) -> None:
    if not isinstance(trigger, dict):
        errors.append(f"{ctx}: trigger must be an object")
        return
    kind = trigger.get("kind")
    if kind not in VALID_TRIGGER_KINDS:
        errors.append(f"{ctx}: trigger.kind {kind!r} must be one of {sorted(VALID_TRIGGER_KINDS)}")
        return
    if kind == "once":
        _check_allowed_keys(trigger, {"kind", "at"}, ctx, errors)
        _validate_rfc3339(trigger.get("at"), f"{ctx}.at", errors)
    elif kind == "recurring":
        _check_allowed_keys(trigger, {"kind", "local_start", "rrule", "timezone"}, ctx, errors)
        ls = trigger.get("local_start")
        if not isinstance(ls, str) or not LOCAL_START_RE.match(ls):
            errors.append(f"{ctx}.local_start: must match YYYY-MM-DDTHH:MM[:SS] without timezone offset, got {ls!r}")
        else:
            try:
                fmt = "%Y-%m-%dT%H:%M:%S" if len(ls) == 19 else "%Y-%m-%dT%H:%M"
                datetime.strptime(ls, fmt)
            except ValueError:
                errors.append(f"{ctx}.local_start: invalid calendar datetime {ls!r}")
        _validate_rrule(trigger.get("rrule"), f"{ctx}.rrule", errors)
        if "timezone" in trigger and trigger["timezone"] is not None:
            validate_timezone(trigger["timezone"], f"{ctx}.timezone", errors)
    elif kind == "event_relative":
        _check_allowed_keys(trigger, {"kind", "event", "offset_seconds"}, ctx, errors)
        ev_ref = trigger.get("event")
        if not isinstance(ev_ref, dict) or not _is_nonempty_str(ev_ref.get("provider_id")):
            errors.append(f"{ctx}.event: must be object with non-empty provider_id")
        if not _is_int(trigger.get("offset_seconds")):
            errors.append(f"{ctx}.offset_seconds: must be an integer")
    elif kind == "watch":
        _check_allowed_keys(trigger, {"kind", "source", "predicate_ref", "min_interval_seconds"}, ctx, errors)
        if not isinstance(trigger.get("source"), dict):
            errors.append(f"{ctx}.source: must be an object")
        if not _is_nonempty_str(trigger.get("predicate_ref")):
            errors.append(f"{ctx}.predicate_ref: must be a non-empty string")
        mis = trigger.get("min_interval_seconds")
        if not _is_int(mis) or mis < 60:
            errors.append(f"{ctx}.min_interval_seconds: must be integer >= 60")


def _validate_completion_criteria(criteria: Any, ctx: str, errors: list[str]) -> None:
    if not isinstance(criteria, list):
        errors.append(f"{ctx}: completion_criteria must be a list")
        return
    for i, item in enumerate(criteria):
        ictx = f"{ctx}[{i}]"
        if not isinstance(item, dict):
            errors.append(f"{ictx}: completion_criteria item must be object {{\"id\"?, \"text\"}}, got {type(item).__name__}")
            continue
        _check_allowed_keys(item, {"id", "text"}, ictx, errors)
        if not _is_nonempty_str(item.get("text")):
            errors.append(f"{ictx}: completion criterion requires non-empty 'text'")
        if "id" in item and not _is_nonempty_str(item.get("id")):
            errors.append(f"{ictx}: completion criterion 'id' must be non-empty string when present")


def _validate_preferences_obj(prefs: Any, ctx: str, errors: list[str], *, require_nonempty: bool) -> None:
    if not isinstance(prefs, dict):
        errors.append(f"{ctx}: preferences must be an object")
        return
    _check_allowed_keys(prefs, {"timezone", "locale", "budget_per_task_usd", "monthly_limit_usd", "working_hours"}, ctx, errors)
    if require_nonempty and not prefs:
        errors.append(f"{ctx}: preferences update requires at least one parameter")
        return
    if "timezone" in prefs:
        validate_timezone(prefs["timezone"], f"{ctx}.timezone", errors)
    if "locale" in prefs and not _is_nonempty_str(prefs["locale"]):
        errors.append(f"{ctx}.locale: must be a non-empty string")
    if "budget_per_task_usd" in prefs:
        v = prefs["budget_per_task_usd"]
        if not _is_num(v) or v <= 0:
            errors.append(f"{ctx}.budget_per_task_usd: must be a number > 0, got {v!r}")
    if "monthly_limit_usd" in prefs and prefs["monthly_limit_usd"] is not None:
        v = prefs["monthly_limit_usd"]
        if not _is_num(v) or v <= 0:
            errors.append(f"{ctx}.monthly_limit_usd: must be null or a number > 0, got {v!r}")
    if "working_hours" in prefs:
        wh = prefs["working_hours"]
        if not isinstance(wh, dict) or not wh:
            errors.append(f"{ctx}.working_hours: must be a non-empty object")
        else:
            for day_key, cfg in wh.items():
                wctx = f"{ctx}.working_hours[{day_key!r}]"
                if str(day_key).lower() not in WEEKDAY_KEYS:
                    errors.append(f"{wctx}: invalid weekday key {day_key!r}")
                if not isinstance(cfg, dict):
                    errors.append(f"{wctx}: must be an object")
                    continue
                _check_allowed_keys(cfg, {"enabled", "start", "end"}, wctx, errors)
                if not isinstance(cfg.get("enabled"), bool):
                    errors.append(f"{wctx}: 'enabled' must be a boolean")
                if "start" in cfg:
                    _validate_hhmm(cfg["start"], f"{wctx}.start", errors)
                if "end" in cfg:
                    _validate_hhmm(cfg["end"], f"{wctx}.end", errors)


def _validate_permission_rule(rule: Any, ctx: str, errors: list[str], *, require_action_classes: bool = False) -> None:
    if not isinstance(rule, dict):
        errors.append(f"{ctx}: permission rule must be an object")
        return
    _check_allowed_keys(
        rule,
        {"title", "effect", "tool_names", "action_classes", "recipients", "max_operations"},
        ctx,
        errors,
    )
    title = rule.get("title")
    if not _is_nonempty_str(title):
        errors.append(f"{ctx}: rule requires non-empty string 'title'")
    elif _utf8_len(title) > MAX_TITLE_BYTES:
        errors.append(f"{ctx}: rule 'title' exceeds {MAX_TITLE_BYTES} bytes")

    effect = rule.get("effect")
    if effect not in VALID_RULE_EFFECTS:
        errors.append(f"{ctx}: invalid rule effect {effect!r} (expected one of {sorted(VALID_RULE_EFFECTS)})")

    tool_names = rule.get("tool_names")
    if not _is_str_list(tool_names) or not tool_names or any(not t.strip() for t in tool_names):
        errors.append(f"{ctx}: 'tool_names' must be a non-empty list of non-empty strings")

    if "action_classes" in rule:
        ac = rule["action_classes"]
        if not _is_str_list(ac) or not ac or not set(ac).issubset(VALID_ACTION_CLASSES):
            errors.append(f"{ctx}: 'action_classes' must be a non-empty subset of {sorted(VALID_ACTION_CLASSES)}")
    elif require_action_classes:
        errors.append(f"{ctx}: missing 'action_classes'")

    if "recipients" in rule:
        rec = rule["recipients"]
        if not _is_str_list(rec) or any(not r.strip() for r in rec):
            errors.append(f"{ctx}: 'recipients' must be a list of non-empty strings")

    if "max_operations" in rule:
        mo = rule["max_operations"]
        if not _is_int(mo) or mo < 1:
            errors.append(f"{ctx}: 'max_operations' must be an integer >= 1, got {mo!r}")


def _validate_email_payload(email: Any, ctx: str, errors: list[str]) -> None:
    if not isinstance(email, dict):
        errors.append(f"{ctx}: email payload must be an object")
        return
    _check_allowed_keys(email, {"id", "from", "to", "subject", "body", "date", "folder", "untrusted"}, ctx, errors)
    for req in ("from", "subject", "body"):
        if not _is_nonempty_str(email.get(req)):
            errors.append(f"{ctx}: email requires non-empty string {req!r}")
    to = email.get("to")
    if not (_is_nonempty_str(to) or (_is_str_list(to) and bool(to) and all(x.strip() for x in to))):
        errors.append(f"{ctx}: email 'to' must be a non-empty string or non-empty list of strings")
    for opt in ("id", "date", "folder"):
        if opt in email and not isinstance(email[opt], str):
            errors.append(f"{ctx}: email {opt!r} must be a string")
    if "untrusted" in email and not isinstance(email["untrusted"], bool):
        errors.append(f"{ctx}: email 'untrusted' must be a boolean")


def _validate_calendar_payload(ev: Any, ctx: str, errors: list[str]) -> None:
    if not isinstance(ev, dict):
        errors.append(f"{ctx}: calendar event must be an object")
        return
    _check_allowed_keys(ev, {"id", "title", "start", "end", "location", "notes", "attendees"}, ctx, errors)
    for req in ("title", "start", "end"):
        if not _is_nonempty_str(ev.get(req)):
            errors.append(f"{ctx}: calendar event requires non-empty string {req!r}")
    for opt in ("id", "location", "notes"):
        if opt in ev and not isinstance(ev[opt], str):
            errors.append(f"{ctx}: calendar event {opt!r} must be a string")
    if "attendees" in ev and not _is_str_list(ev["attendees"]):
        errors.append(f"{ctx}: calendar event 'attendees' must be a list of strings")


def _validate_doc_payload(doc: Any, ctx: str, errors: list[str]) -> None:
    if not isinstance(doc, dict):
        errors.append(f"{ctx}: doc payload must be an object")
        return
    _check_allowed_keys(doc, {"id", "title", "content", "updated_at", "tags"}, ctx, errors)
    for req in ("title", "content"):
        if not _is_nonempty_str(doc.get(req)):
            errors.append(f"{ctx}: doc requires non-empty string {req!r}")
    for opt in ("id", "updated_at"):
        if opt in doc and not isinstance(doc[opt], str):
            errors.append(f"{ctx}: doc {opt!r} must be a string")
    if "tags" in doc and not _is_str_list(doc["tags"]):
        errors.append(f"{ctx}: doc 'tags' must be a list of strings")


def _validate_web_results_payload(params: Any, ctx: str, errors: list[str]) -> None:
    if not isinstance(params, dict):
        errors.append(f"{ctx}: web results payload must be an object")
        return
    _check_allowed_keys(params, {"query", "results"}, ctx, errors)
    if "query" in params and not isinstance(params["query"], str):
        errors.append(f"{ctx}: 'query' must be a string")
    results = params.get("results")
    if not isinstance(results, list) or not results:
        errors.append(f"{ctx}: 'results' must be a non-empty list of objects")
        return
    for i, item in enumerate(results):
        rctx = f"{ctx}.results[{i}]"
        if not isinstance(item, dict):
            errors.append(f"{rctx}: must be an object")
            continue
        _check_allowed_keys(item, {"title", "url", "snippet", "description", "source"}, rctx, errors)
        if not _is_nonempty_str(item.get("title")):
            errors.append(f"{rctx}: requires non-empty 'title'")
        if not _is_nonempty_str(item.get("url")):
            errors.append(f"{rctx}: requires non-empty 'url'")
        if not (_is_nonempty_str(item.get("snippet")) or _is_nonempty_str(item.get("description"))):
            errors.append(f"{rctx}: requires non-empty 'snippet' or 'description'")
        if "source" in item and not isinstance(item["source"], str):
            errors.append(f"{rctx}: 'source' must be a string")


def _validate_mcp_world(mcp_world: Any, ctx: str, errors: list[str]) -> None:
    if not isinstance(mcp_world, dict):
        errors.append(f"{ctx}: mcp_world must be an object")
        return
    _check_allowed_keys(
        mcp_world,
        {"server_name", "emails", "calendar_events", "docs", "issues", "web_results"},
        ctx,
        errors,
    )
    if "server_name" in mcp_world and not _is_nonempty_str(mcp_world["server_name"]):
        errors.append(f"{ctx}.server_name: must be a non-empty string")
    if "emails" in mcp_world:
        if not isinstance(mcp_world["emails"], list):
            errors.append(f"{ctx}.emails: must be a list")
        else:
            for i, em in enumerate(mcp_world["emails"]):
                _validate_email_payload(em, f"{ctx}.emails[{i}]", errors)
    if "calendar_events" in mcp_world:
        if not isinstance(mcp_world["calendar_events"], list):
            errors.append(f"{ctx}.calendar_events: must be a list")
        else:
            for i, cev in enumerate(mcp_world["calendar_events"]):
                _validate_calendar_payload(cev, f"{ctx}.calendar_events[{i}]", errors)
    if "docs" in mcp_world:
        if not isinstance(mcp_world["docs"], list):
            errors.append(f"{ctx}.docs: must be a list")
        else:
            for i, doc in enumerate(mcp_world["docs"]):
                _validate_doc_payload(doc, f"{ctx}.docs[{i}]", errors)
    if "issues" in mcp_world:
        if not isinstance(mcp_world["issues"], list):
            errors.append(f"{ctx}.issues: must be a list")
        else:
            for i, iss in enumerate(mcp_world["issues"]):
                ictx = f"{ctx}.issues[{i}]"
                if not isinstance(iss, dict):
                    errors.append(f"{ictx}: issue must be an object")
                    continue
                _check_allowed_keys(iss, {"id", "title", "status", "priority", "assignee", "notes"}, ictx, errors)
                for req in ("id", "title", "status"):
                    if not _is_nonempty_str(iss.get(req)):
                        errors.append(f"{ictx}: issue requires non-empty string {req!r}")
                for opt in ("priority", "assignee", "notes"):
                    if opt in iss and not isinstance(iss[opt], str):
                        errors.append(f"{ictx}: issue {opt!r} must be a string")
    if "web_results" in mcp_world:
        wr = mcp_world["web_results"]
        if not isinstance(wr, list):
            errors.append(f"{ctx}.web_results: must be a list")
        else:
            for i, item in enumerate(wr):
                if not isinstance(item, dict):
                    errors.append(f"{ctx}.web_results[{i}]: must be an object")


def _validate_probe(probe: Any, ctx: str, global_probe_ids: set[str], errors: list[str]) -> None:
    if not isinstance(probe, dict):
        errors.append(f"{ctx}: probe must be object")
        return
    _check_allowed_keys(
        probe,
        {"id", "category", "rubric", "must_not_contain_in_core", "expect_fts_query", "expect_no_external_write"},
        f"{ctx}.probe",
        errors,
    )
    pid = probe.get("id", "")
    if not _is_nonempty_str(pid):
        errors.append(f"{ctx}: probe missing id")
    elif pid in global_probe_ids:
        errors.append(f"{ctx}: duplicate global probe id {pid!r}")
    else:
        global_probe_ids.add(pid)
    if not _is_nonempty_str(probe.get("category")):
        errors.append(f"{ctx}: probe missing category")
    rubric = probe.get("rubric", "")
    if not isinstance(rubric, str) or len(rubric.strip()) < 20:
        errors.append(f"{ctx}: probe rubric too short")
    if "must_not_contain_in_core" in probe:
        if not _is_str_list(probe["must_not_contain_in_core"]):
            errors.append(f"{ctx}.probe.must_not_contain_in_core: must be a list of strings")
    if "expect_no_external_write" in probe:
        if not isinstance(probe["expect_no_external_write"], bool):
            errors.append(f"{ctx}.probe.expect_no_external_write: must be a boolean")
    if "expect_fts_query" in probe:
        fts = probe["expect_fts_query"]
        fctx = f"{ctx}.probe.expect_fts_query"
        if not isinstance(fts, dict):
            errors.append(f"{fctx}: must be an object")
        else:
            _check_allowed_keys(fts, {"query", "min_hits", "max_hits", "layer"}, fctx, errors)
            q = fts.get("query")
            if not _is_nonempty_str(q) or _utf8_len(q) > MAX_MEMORY_SEARCH_BYTES:
                errors.append(f"{fctx}.query: must be non-empty string <= {MAX_MEMORY_SEARCH_BYTES} bytes")
            min_hits = fts.get("min_hits")
            if not _is_int(min_hits) or min_hits < 0:
                errors.append(f"{fctx}.min_hits: must be integer >= 0")
            if "max_hits" in fts and fts["max_hits"] is not None:
                max_hits = fts["max_hits"]
                if not _is_int(max_hits) or max_hits < 0:
                    errors.append(f"{fctx}.max_hits: must be integer >= 0")
                elif _is_int(min_hits) and max_hits < min_hits:
                    errors.append(f"{fctx}.max_hits ({max_hits}) < min_hits ({min_hits})")
            if "layer" in fts and fts["layer"] not in VALID_SEARCH_LAYERS:
                errors.append(f"{fctx}.layer: must be one of {sorted(VALID_SEARCH_LAYERS)}, got {fts['layer']!r}")


def _validate_sim_control(action: str, params: dict[str, Any], ctx: str, errors: list[str]) -> None:
    if action != "reset_context":
        errors.append(f"{ctx}: unknown sim_control action {action!r}")
        return
    _check_allowed_keys(params, {"advance_seconds", "context_from"}, ctx, errors)
    if "advance_seconds" in params:
        adv = params["advance_seconds"]
        if not _is_int(adv) or adv < 0:
            errors.append(f"{ctx}.advance_seconds: must be integer >= 0, got {adv!r}")
    if "context_from" in params:
        _validate_rfc3339(params["context_from"], f"{ctx}.context_from", errors)


def _validate_goal_ui(action: str, params: dict[str, Any], ctx: str, errors: list[str]) -> None:
    if action in ("create", "create_goal"):
        _check_allowed_keys(
            params,
            {"title", "outcome", "description", "notes", "deadline", "target_date", "follow_up", "completion_criteria"},
            ctx,
            errors,
        )
        if not _is_nonempty_str(params.get("title")):
            errors.append(f"{ctx}: goal_ui.{action} requires non-empty 'title'")
        for opt in ("outcome", "description", "notes"):
            if opt in params and not _is_nonempty_str(params[opt]):
                errors.append(f"{ctx}.{opt}: must be a non-empty string")
        if "deadline" in params:
            _validate_date_or_rfc3339(params["deadline"], f"{ctx}.deadline", errors)
        if "target_date" in params:
            _validate_date_or_rfc3339(params["target_date"], f"{ctx}.target_date", errors)
        if "follow_up" in params and params["follow_up"] not in VALID_FOLLOW_UP:
            errors.append(f"{ctx}.follow_up: must be one of {sorted(VALID_FOLLOW_UP)}, got {params['follow_up']!r}")
        if "completion_criteria" in params:
            _validate_completion_criteria(params["completion_criteria"], f"{ctx}.completion_criteria", errors)
    elif action in ("update", "update_goal"):
        update_fields = {
            "outcome", "description", "notes", "completion_criteria",
            "deadline", "target_date", "follow_up", "status",
        }
        allowed = SELECTOR_KEYS | update_fields
        _check_allowed_keys(params, allowed, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
        has_non_title_sel = any(k in params for k in (SELECTOR_KEYS - {"title"}))
        has_update = any(k in params for k in update_fields) or ("title" in params and has_non_title_sel)
        if not has_update:
            errors.append(
                f"{ctx}: goal_ui.{action} requires at least one update field "
                "(outcome/description/notes/completion_criteria/deadline/target_date/follow_up/status, "
                "or title with another selector)"
            )
        for opt in ("outcome", "description", "notes"):
            if opt in params and not _is_nonempty_str(params[opt]):
                errors.append(f"{ctx}.{opt}: must be a non-empty string")
        if "deadline" in params:
            _validate_date_or_rfc3339(params["deadline"], f"{ctx}.deadline", errors)
        if "target_date" in params:
            _validate_date_or_rfc3339(params["target_date"], f"{ctx}.target_date", errors)
        if "follow_up" in params and params["follow_up"] not in VALID_FOLLOW_UP:
            errors.append(f"{ctx}.follow_up: must be one of {sorted(VALID_FOLLOW_UP)}, got {params['follow_up']!r}")
        if "completion_criteria" in params:
            _validate_completion_criteria(params["completion_criteria"], f"{ctx}.completion_criteria", errors)
        if "status" in params and params["status"] != "completed":
            errors.append(f"{ctx}.status: invalid GoalEditable update status {params['status']!r} (only 'completed' is mapped; use outcome/completion_criteria edits instead)")
    elif action == "complete":
        _check_allowed_keys(params, SELECTOR_KEYS | {"notes", "keep_monitors"}, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
        if "notes" in params and not isinstance(params["notes"], str):
            errors.append(f"{ctx}.notes: must be a string")
        if "keep_monitors" in params and not isinstance(params["keep_monitors"], bool):
            errors.append(f"{ctx}.keep_monitors: must be a boolean")
    else:
        errors.append(f"{ctx}: unknown goal_ui action {action!r}")


def _validate_automation_ui(action: str, params: dict[str, Any], ctx: str, errors: list[str]) -> None:
    if action in ("create", "create_automation"):
        allowed = {
            "title", "instruction", "prompt", "trigger", "schedule", "kind", "timezone",
            "budget_usd", "max_active_runs", "missed_run", "execution_window_seconds",
            "goal_id", "connection_ids", "destination_ids", "max_occurrences", "end_at",
        }
        _check_allowed_keys(params, allowed, ctx, errors)
        if not _is_nonempty_str(params.get("title")):
            errors.append(f"{ctx}: automation_ui.{action} requires non-empty 'title'")
        inst = params.get("instruction") if "instruction" in params else params.get("prompt")
        if not _is_nonempty_str(inst):
            errors.append(f"{ctx}: automation_ui.{action} requires non-empty 'instruction' or 'prompt'")
        elif _utf8_len(inst) > MAX_INSTRUCTION_BYTES:
            errors.append(f"{ctx}: instruction/prompt exceeds {MAX_INSTRUCTION_BYTES} bytes")

        has_trigger = "trigger" in params and params["trigger"] is not None
        has_schedule = "schedule" in params and params["schedule"] is not None
        if has_trigger and has_schedule:
            errors.append(f"{ctx}: specify either 'trigger' or 'schedule', not both")
        elif not has_trigger and not has_schedule:
            errors.append(f"{ctx}: automation_ui.{action} requires either 'trigger' or 'schedule'")
        elif has_trigger:
            if "kind" in params:
                if params["kind"] == "monitor":
                    errors.append(f"{ctx}: top-level kind='monitor' is only allowed with 'schedule' shorthand, not 'trigger'")
                elif params["kind"] not in VALID_TRIGGER_KINDS:
                    errors.append(f"{ctx}.kind: invalid automation kind {params['kind']!r}")
            _validate_trigger(params["trigger"], f"{ctx}.trigger", errors)
        else:
            if "kind" in params and params["kind"] not in {"recurring", "monitor"}:
                errors.append(
                    f"{ctx}.kind: when 'schedule' is specified, kind must be 'recurring' or 'monitor', got {params['kind']!r}"
                )
            _validate_schedule_shorthand(params["schedule"], f"{ctx}.schedule", errors)

        if "timezone" in params and params["timezone"] is not None:
            validate_timezone(params["timezone"], f"{ctx}.timezone", errors)
        if "budget_usd" in params:
            b = params["budget_usd"]
            if not _is_num(b) or b < 0:
                errors.append(f"{ctx}.budget_usd: must be number >= 0, got {b!r}")
        if "max_active_runs" in params:
            mar = params["max_active_runs"]
            if not _is_int(mar) or mar < 1 or mar > 10:
                errors.append(f"{ctx}.max_active_runs: must be integer 1..10, got {mar!r}")
        if "missed_run" in params and params["missed_run"] not in VALID_MISSED_RUN:
            errors.append(f"{ctx}.missed_run: must be one of {sorted(VALID_MISSED_RUN)}, got {params['missed_run']!r}")
        if "execution_window_seconds" in params:
            ews = params["execution_window_seconds"]
            if not _is_int(ews) or ews < 0:
                errors.append(f"{ctx}.execution_window_seconds: must be integer >= 0, got {ews!r}")
        if "goal_id" in params and not isinstance(params["goal_id"], str):
            errors.append(f"{ctx}.goal_id: must be a string")
        if "connection_ids" in params and not _is_str_list(params["connection_ids"]):
            errors.append(f"{ctx}.connection_ids: must be a list of strings")
        if "destination_ids" in params:
            dids = params["destination_ids"]
            if not _is_str_list(dids) or any(d != "dest_in_app" for d in dids):
                errors.append(f"{ctx}.destination_ids: elements must equal 'dest_in_app'")
        if "max_occurrences" in params:
            mo = params["max_occurrences"]
            if not _is_int(mo) or mo < 0:
                errors.append(f"{ctx}.max_occurrences: must be integer >= 0")
        if "end_at" in params and params["end_at"] is not None:
            _validate_rfc3339(params["end_at"], f"{ctx}.end_at", errors)
    elif action in ("pause", "pause_automation", "resume", "resume_automation", "cancel", "run_now"):
        _check_allowed_keys(params, SELECTOR_KEYS, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
    else:
        errors.append(f"{ctx}: unknown automation_ui action {action!r}")


def _validate_interaction_reply(action: str, params: dict[str, Any], ctx: str, errors: list[str]) -> None:
    if action not in {"approve", "decline", "answer"}:
        errors.append(f"{ctx}: unknown interaction_reply action {action!r}")
        return
    _check_allowed_keys(
        params,
        {
            "decision", "match_tool", "match_recipient",
            "note", "text", "reason", "comment", "remember_rule",
            "expect_stale", "allow_expired",
        },
        ctx,
        errors,
    )
    if "decision" in params:
        dec = params["decision"]
        if dec not in {"approve", "decline", "answer"}:
            errors.append(f"{ctx}.decision: invalid decision {dec!r}")
        elif dec != action:
            errors.append(f"{ctx}.decision: {dec!r} does not match action {action!r}")
    for sel in ("match_tool", "match_recipient"):
        if sel in params and not _is_nonempty_str(params[sel]):
            errors.append(f"{ctx}.{sel}: must be a non-empty string")
    text_val = ""
    for key in ("text", "note", "reason", "comment"):
        if key in params:
            val = params[key]
            if not isinstance(val, str):
                errors.append(f"{ctx}.{key}: must be a string")
            elif _utf8_len(val) > MAX_MESSAGE_BYTES:
                errors.append(f"{ctx}.{key}: exceeds {MAX_MESSAGE_BYTES} bytes")
            elif not text_val and val.strip():
                text_val = val.strip()
    if action == "answer" and not text_val:
        errors.append(f"{ctx}: interaction_reply.answer requires non-empty text/note/reason/comment")
    if "remember_rule" in params:
        rr = params["remember_rule"]
        if not isinstance(rr, bool):
            errors.append(f"{ctx}.remember_rule: must be a boolean, got {rr!r}")
        elif rr and action in ("decline", "answer"):
            errors.append(f"{ctx}.remember_rule: cannot be True on {action!r}")
    for flag in ("expect_stale", "allow_expired"):
        if flag in params and not isinstance(params[flag], bool):
            errors.append(f"{ctx}.{flag}: must be a boolean, got {params[flag]!r}")


def _validate_memory_ui(action: str, params: dict[str, Any], ctx: str, errors: list[str]) -> None:
    if action == "create_core":
        _check_allowed_keys(params, {"text", "about", "section", "pinned"}, ctx, errors)
        text = params.get("text")
        if not _is_nonempty_str(text):
            errors.append(f"{ctx}: memory_ui.create_core requires non-empty 'text'")
        elif _utf8_len(text) > MAX_MEMORY_TEXT_BYTES:
            errors.append(f"{ctx}.text: exceeds {MAX_MEMORY_TEXT_BYTES} bytes")
        if "about" in params:
            if not isinstance(params["about"], str):
                errors.append(f"{ctx}.about: must be a string")
            elif _utf8_len(params["about"]) > MAX_MEMORY_ABOUT_BYTES:
                errors.append(f"{ctx}.about: exceeds {MAX_MEMORY_ABOUT_BYTES} bytes")
        if "section" in params:
            if not isinstance(params["section"], str):
                errors.append(f"{ctx}.section: must be a string")
            elif _utf8_len(params["section"]) > MAX_MEMORY_SECTION_BYTES:
                errors.append(f"{ctx}.section: exceeds {MAX_MEMORY_SECTION_BYTES} bytes")
        if "pinned" in params and not isinstance(params["pinned"], bool):
            errors.append(f"{ctx}.pinned: must be a boolean")
    elif action == "consolidate":
        if params:
            errors.append(f"{ctx}: memory_ui.consolidate expects empty params, got {sorted(params.keys())!r}")
    elif action == "search":
        _check_allowed_keys(params, {"query", "layer"}, ctx, errors)
        q = params.get("query")
        if not _is_nonempty_str(q):
            errors.append(f"{ctx}: memory_ui.search requires non-empty 'query'")
        elif _utf8_len(q) > MAX_MEMORY_SEARCH_BYTES:
            errors.append(f"{ctx}.query: exceeds {MAX_MEMORY_SEARCH_BYTES} bytes")
        if "layer" in params and params["layer"] not in VALID_SEARCH_LAYERS:
            errors.append(f"{ctx}.layer: must be one of {sorted(VALID_SEARCH_LAYERS)}, got {params['layer']!r}")
    elif action in ("pin_matching", "unpin_matching", "demote_matching", "forget_matching"):
        _check_allowed_keys(params, SELECTOR_KEYS | {"about"}, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
        if "about" in params:
            if not isinstance(params["about"], str):
                errors.append(f"{ctx}.about: must be a string")
            elif _utf8_len(params["about"]) > MAX_MEMORY_ABOUT_BYTES:
                errors.append(f"{ctx}.about: exceeds {MAX_MEMORY_ABOUT_BYTES} bytes")
    elif action == "edit_matching":
        _check_allowed_keys(params, SELECTOR_KEYS | {"text", "new_text", "append_note", "about", "section"}, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
        has_replace = any(k in params for k in ("text", "new_text"))
        has_append = "append_note" in params
        if has_replace and has_append:
            errors.append(f"{ctx}: edit_matching cannot specify both text/new_text and append_note")
        edit_keys = [k for k in ("text", "new_text", "append_note", "about", "section") if k in params]
        if not edit_keys:
            errors.append(f"{ctx}: edit_matching requires at least one edit field (text/new_text/append_note/about/section)")
        for k in ("text", "new_text", "append_note"):
            if k in params:
                v = params[k]
                if not _is_nonempty_str(v):
                    errors.append(f"{ctx}.{k}: must be a non-empty string")
                elif _utf8_len(v) > MAX_MEMORY_TEXT_BYTES:
                    errors.append(f"{ctx}.{k}: exceeds {MAX_MEMORY_TEXT_BYTES} bytes")
        if "about" in params:
            if not _is_nonempty_str(params["about"]):
                errors.append(f"{ctx}.about: must be a non-empty string")
            elif _utf8_len(params["about"]) > MAX_MEMORY_ABOUT_BYTES:
                errors.append(f"{ctx}.about: exceeds {MAX_MEMORY_ABOUT_BYTES} bytes")
        if "section" in params:
            if not _is_nonempty_str(params["section"]):
                errors.append(f"{ctx}.section: must be a non-empty string")
            elif _utf8_len(params["section"]) > MAX_MEMORY_SECTION_BYTES:
                errors.append(f"{ctx}.section: exceeds {MAX_MEMORY_SECTION_BYTES} bytes")
    elif action == "promote_log_matching":
        _check_allowed_keys(params, SELECTOR_KEYS | {"text", "about", "section", "pinned"}, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
        if "text" in params:
            if not _is_nonempty_str(params["text"]):
                errors.append(f"{ctx}.text: must be a non-empty string")
            elif _utf8_len(params["text"]) > MAX_MEMORY_TEXT_BYTES:
                errors.append(f"{ctx}.text: exceeds {MAX_MEMORY_TEXT_BYTES} bytes")
        if "about" in params:
            if not isinstance(params["about"], str) or _utf8_len(params["about"]) > MAX_MEMORY_ABOUT_BYTES:
                errors.append(f"{ctx}.about: must be string <= {MAX_MEMORY_ABOUT_BYTES} bytes")
        if "section" in params:
            if not isinstance(params["section"], str) or _utf8_len(params["section"]) > MAX_MEMORY_SECTION_BYTES:
                errors.append(f"{ctx}.section: must be string <= {MAX_MEMORY_SECTION_BYTES} bytes")
        if "pinned" in params and not isinstance(params["pinned"], bool):
            errors.append(f"{ctx}.pinned: must be a boolean")
    elif action == "dismiss_working_matching":
        _check_allowed_keys(params, SELECTOR_KEYS, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
    else:
        errors.append(f"{ctx}: unknown memory_ui action {action!r}")


def _validate_library_ui(action: str, params: dict[str, Any], ctx: str, errors: list[str]) -> None:
    if action in ("create", "save_document"):
        _check_allowed_keys(params, {"title", "content", "tags", "collection"}, ctx, errors)
        title = params.get("title")
        if not _is_nonempty_str(title):
            errors.append(f"{ctx}: library_ui.{action} requires non-empty 'title'")
        elif _utf8_len(title) > MAX_TITLE_BYTES:
            errors.append(f"{ctx}.title: exceeds {MAX_TITLE_BYTES} bytes")
        if not _is_nonempty_str(params.get("content")):
            errors.append(f"{ctx}: library_ui.{action} requires non-empty 'content'")
        if "tags" in params and not _is_str_list(params["tags"]):
            errors.append(f"{ctx}.tags: must be a list of strings")
        if "collection" in params and not _is_nonempty_str(params["collection"]):
            errors.append(f"{ctx}.collection: must be a non-empty string")
    elif action == "delete_matching":
        _check_allowed_keys(params, SELECTOR_KEYS | {"scope"}, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
        if "scope" in params and params["scope"] not in VALID_DELETE_SCOPES:
            errors.append(f"{ctx}.scope: must be one of {sorted(VALID_DELETE_SCOPES)}, got {params['scope']!r}")
    elif action == "revise":
        _check_allowed_keys(params, SELECTOR_KEYS | {"instruction"}, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
        inst = params.get("instruction")
        if not _is_nonempty_str(inst):
            errors.append(f"{ctx}: library_ui.revise requires non-empty 'instruction'")
        elif _utf8_len(inst) > MAX_MESSAGE_BYTES:
            errors.append(f"{ctx}.instruction: exceeds {MAX_MESSAGE_BYTES} bytes")
    elif action == "update":
        _check_allowed_keys(params, SELECTOR_KEYS | {"title", "collection"}, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
        has_non_title_sel = any(k in params for k in (SELECTOR_KEYS - {"title"}))
        has_title = "title" in params
        has_collection = "collection" in params
        if has_title:
            if not _is_nonempty_str(params["title"]):
                errors.append(f"{ctx}.title: must be a non-empty string")
            elif _utf8_len(params["title"]) > MAX_TITLE_BYTES:
                errors.append(f"{ctx}.title: exceeds {MAX_TITLE_BYTES} bytes")
        if has_collection and not _is_nonempty_str(params["collection"]):
            errors.append(f"{ctx}.collection: must be a non-empty string")
        if not has_collection and not (has_title and has_non_title_sel):
            errors.append(f"{ctx}: library_ui.update requires 'collection' or a selector plus new 'title'")
    elif action == "search":
        _check_allowed_keys(params, SELECTOR_KEYS, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
    else:
        errors.append(f"{ctx}: unknown library_ui action {action!r}")


def _validate_permission_ui(action: str, params: dict[str, Any], ctx: str, errors: list[str]) -> None:
    if action in ("create", "create_rule", "add_rule"):
        _validate_permission_rule(params, ctx, errors, require_action_classes=False)
    elif action in ("revoke", "revoke_rule"):
        _check_allowed_keys(params, SELECTOR_KEYS, ctx, errors)
        _validate_selectors(params, ctx, errors, required=True)
    else:
        errors.append(f"{ctx}: unknown permission_ui action {action!r}")


def _validate_preferences_ui(action: str, params: dict[str, Any], ctx: str, errors: list[str]) -> None:
    if action not in ("update", "update_preferences"):
        errors.append(f"{ctx}: unknown preferences_ui action {action!r}")
        return
    _validate_preferences_obj(params, ctx, errors, require_nonempty=True)


def _validate_mcp_world_event(action: str, params: dict[str, Any], ctx: str, errors: list[str]) -> None:
    if action in ("add_email", "inject_email"):
        payload = params["email"] if set(params.keys()) == {"email"} and isinstance(params["email"], dict) else params
        _validate_email_payload(payload, ctx, errors)
    elif action == "add_calendar_event":
        payload = params["event"] if set(params.keys()) == {"event"} and isinstance(params["event"], dict) else params
        _validate_calendar_payload(payload, ctx, errors)
    elif action == "add_doc":
        payload = params["doc"] if set(params.keys()) == {"doc"} and isinstance(params["doc"], dict) else params
        _validate_doc_payload(payload, ctx, errors)
    elif action == "update_issue":
        _check_allowed_keys(
            params,
            {"id", "issue_id", "status", "assignee", "priority", "notes", "append_note", "title", "untrusted_comment"},
            ctx,
            errors,
        )
        iid = params.get("id") if "id" in params else params.get("issue_id")
        if not _is_nonempty_str(iid):
            errors.append(f"{ctx}: update_issue requires non-empty 'id' or 'issue_id'")
        for opt in ("status", "assignee", "priority", "notes", "append_note", "title"):
            if opt in params and not isinstance(params[opt], str):
                errors.append(f"{ctx}.{opt}: must be a string")
        if "untrusted_comment" in params and not isinstance(params["untrusted_comment"], bool):
            errors.append(f"{ctx}.untrusted_comment: must be a boolean")
    elif action in ("add_web_results", "seed_web_search"):
        _validate_web_results_payload(params, ctx, errors)
    else:
        errors.append(f"{ctx}: unknown mcp_world_event action {action!r}")


_ACTION_VALIDATORS = {
    "sim_control": _validate_sim_control,
    "goal_ui": _validate_goal_ui,
    "automation_ui": _validate_automation_ui,
    "interaction_reply": _validate_interaction_reply,
    "memory_ui": _validate_memory_ui,
    "library_ui": _validate_library_ui,
    "permission_ui": _validate_permission_ui,
    "preferences_ui": _validate_preferences_ui,
    "mcp_world_event": _validate_mcp_world_event,
}


def validate_persona(persona_dir: Path, global_probe_ids: set[str]) -> tuple[list[str], dict]:
    errors: list[str] = []
    slug = persona_dir.name
    if not SLUG_RE.match(slug):
        errors.append(f"{slug}: directory name must match ^[0-9]{{2}}-[a-z0-9-]+$")

    bible_path = persona_dir / "persona.md"
    profile_path = persona_dir / "profile.json"
    month_path = persona_dir / "month.json"

    for p in (bible_path, profile_path, month_path):
        if not p.is_file():
            errors.append(f"{slug}: missing required file {p.name}")
    if errors:
        return errors, {}

    bible_text = bible_path.read_text(encoding="utf-8")
    if len(bible_text.strip()) < 800:
        errors.append(f"{slug}/persona.md: persona bible too short ({len(bible_text)} chars, expected >= 800)")

    try:
        profile = json.loads(profile_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        return [f"{slug}/profile.json: invalid JSON: {exc}"], {}

    try:
        month = json.loads(month_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        return [f"{slug}/month.json: invalid JSON: {exc}"], {}

    if not isinstance(profile, dict):
        return [f"{slug}/profile.json: root must be object"], {}
    if not isinstance(month, dict):
        return [f"{slug}/month.json: root must be object"], {}

    # Validate profile.json
    required_profile = [
        "slug", "name", "email", "timezone", "sim_start_date",
        "presets", "preferences", "persona_responder_brief", "mcp_world",
    ]
    for field in required_profile:
        if field not in profile:
            errors.append(f"{slug}/profile.json: missing required field {field!r}")

    if profile.get("slug") != slug:
        errors.append(f"{slug}/profile.json: slug {profile.get('slug')!r} != dir {slug!r}")
    if "@" not in str(profile.get("email", "")):
        errors.append(f"{slug}/profile.json: invalid email {profile.get('email')!r}")
    if not DATE_RE.match(str(profile.get("sim_start_date", ""))):
        errors.append(f"{slug}/profile.json: invalid sim_start_date")
    if isinstance(profile.get("timezone"), str):
        validate_timezone(profile["timezone"], f"{slug}/profile.json", errors)
    else:
        errors.append(f"{slug}/profile.json: timezone must be a string")
    presets = profile.get("presets", [])
    if not isinstance(presets, list) or not presets or not set(presets).issubset(VALID_PRESETS):
        errors.append(f"{slug}/profile.json: invalid presets {presets!r}")
    if len(str(profile.get("persona_responder_brief", ""))) < 40:
        errors.append(f"{slug}/profile.json: persona_responder_brief must be >= 40 chars")

    if "preferences" in profile:
        _validate_preferences_obj(profile["preferences"], f"{slug}/profile.json:preferences", errors, require_nonempty=False)
    if "initial_permissions" in profile:
        ip = profile["initial_permissions"]
        if not isinstance(ip, list):
            errors.append(f"{slug}/profile.json: initial_permissions must be a list")
        else:
            for i, rule in enumerate(ip):
                _validate_permission_rule(rule, f"{slug}/profile.json:initial_permissions[{i}]", errors)
    if "mcp_world" in profile:
        _validate_mcp_world(profile["mcp_world"], f"{slug}/profile.json:mcp_world", errors)

    # Validate month.json
    if month.get("persona") != slug:
        errors.append(f"{slug}/month.json: persona {month.get('persona')!r} != {slug!r}")
    if month.get("sim_start_date") != profile.get("sim_start_date"):
        errors.append(f"{slug}/month.json: sim_start_date mismatch with profile.json")

    audit = month.get("end_of_month_audit")
    if not isinstance(audit, dict) or not audit.get("rubric") or not audit.get("expected_core_concepts"):
        errors.append(f"{slug}/month.json: missing or incomplete end_of_month_audit")

    events = month.get("events", [])
    if not isinstance(events, list) or len(events) < 40:
        errors.append(f"{slug}/month.json: expected >= 40 events, found {len(events) if isinstance(events, list) else 'non-list'}")

    seen_event_ids: set[str] = set()
    prev_key: tuple[int, str] | None = None
    probe_count = 0
    chat_count = 0
    settle_count = 0
    max_day = 0
    referenced_assets: set[str] = set()

    for idx, ev in enumerate(events if isinstance(events, list) else []):
        ctx = f"{slug}/month.json:events[{idx}]"
        if not isinstance(ev, dict):
            errors.append(f"{ctx}: must be object")
            continue
        ev_id = ev.get("id", "")
        if not isinstance(ev_id, str) or not ID_RE.match(ev_id):
            errors.append(f"{ctx}: invalid id {ev_id!r}")
        elif ev_id in seen_event_ids:
            errors.append(f"{ctx}: duplicate event id {ev_id!r}")
        seen_event_ids.add(str(ev_id))

        day = ev.get("day")
        local_time = ev.get("local_time", "")
        if not _is_int(day) or day < 1 or day > 31:
            errors.append(f"{ctx}: invalid day {day!r}")
        else:
            max_day = max(max_day, day)
        if not isinstance(local_time, str) or not TIME_RE.match(local_time):
            errors.append(f"{ctx}: invalid local_time {local_time!r}")
        else:
            hh, mm = int(local_time[:2]), int(local_time[3:])
            if hh > 23 or mm > 59:
                errors.append(f"{ctx}: out-of-range local_time {local_time!r}")
        if _is_int(day) and isinstance(local_time, str) and TIME_RE.match(local_time):
            key = (day, local_time)
            if prev_key is not None and key < prev_key:
                errors.append(f"{ctx} ({ev_id}): non-monotonic timestamp {key} < {prev_key}")
            prev_key = key

        if "timezone" in ev and ev["timezone"]:
            validate_timezone(ev["timezone"], ctx, errors)
        if "tags" in ev and ev["tags"] is not None and not _is_str_list(ev["tags"]):
            errors.append(f"{ctx} ({ev_id}): tags must be a list of strings")

        kind = ev.get("kind")
        if kind not in VALID_KINDS:
            errors.append(f"{ctx}: unknown kind {kind!r}")
        elif kind == "chat":
            chat_count += 1
            _check_allowed_keys(
                ev,
                {"id", "day", "local_time", "kind", "text", "timezone", "attachments", "wait_for_reply", "interrupt_active", "tags", "probe"},
                f"{ctx} ({ev_id})",
                errors,
            )
            text = ev.get("text")
            if not _is_nonempty_str(text):
                errors.append(f"{ctx} ({ev_id}): chat event requires non-empty text")
            elif _utf8_len(text) > MAX_MESSAGE_BYTES:
                errors.append(f"{ctx} ({ev_id}): chat text exceeds {MAX_MESSAGE_BYTES} bytes")
            for bfield in ("wait_for_reply", "interrupt_active"):
                if bfield in ev and not isinstance(ev[bfield], bool):
                    errors.append(f"{ctx} ({ev_id}): {bfield} must be a boolean")
            if "attachments" in ev and ev["attachments"] is not None and not _is_str_list(ev["attachments"]):
                errors.append(f"{ctx} ({ev_id}): attachments must be a list of strings")
        elif kind == "settle":
            settle_count += 1
            _check_allowed_keys(
                ev,
                {"id", "day", "local_time", "kind", "timezone", "snapshot_label", "assert_invariants", "tags", "probe"},
                f"{ctx} ({ev_id})",
                errors,
            )
            if "snapshot_label" in ev and ev["snapshot_label"] is not None and not isinstance(ev["snapshot_label"], str):
                errors.append(f"{ctx} ({ev_id}): snapshot_label must be a string")
            if "assert_invariants" in ev and not (
                isinstance(ev["assert_invariants"], bool) or _is_str_list(ev["assert_invariants"])
            ):
                errors.append(f"{ctx} ({ev_id}): assert_invariants must be a boolean or list of strings")
        else:
            _check_allowed_keys(
                ev,
                {"id", "day", "local_time", "kind", "action", "params", "timezone", "tags", "probe"},
                f"{ctx} ({ev_id})",
                errors,
            )
            action = ev.get("action")
            params = ev.get("params")
            if not _is_nonempty_str(action):
                errors.append(f"{ctx} ({ev_id}): {kind} requires non-empty action")
            if not isinstance(params, dict):
                errors.append(f"{ctx} ({ev_id}): {kind} requires object params")
            if _is_nonempty_str(action) and isinstance(params, dict):
                validator = _ACTION_VALIDATORS[kind]
                validator(action, params, f"{ctx} ({ev_id}) [{kind}.{action}]", errors)

        for att in (ev.get("attachments") or []) if isinstance(ev.get("attachments"), list) else []:
            if not isinstance(att, str):
                continue
            att_path = persona_dir / att
            referenced_assets.add(att)
            if not att_path.is_file():
                errors.append(f"{ctx} ({ev_id}): missing attachment {att}")
                continue
            ext = att_path.suffix.lower()
            if ext not in VALID_EXTENSIONS:
                errors.append(f"{ctx} ({ev_id}): unsupported attachment extension {ext}")
            raw = att_path.read_bytes()
            if len(raw) == 0:
                errors.append(f"{ctx} ({ev_id}): empty attachment {att}")
            if ext == ".pdf" and not raw.startswith(b"%PDF-"):
                errors.append(f"{ctx} ({ev_id}): invalid PDF magic header in {att}")
            if ext == ".png" and not raw.startswith(b"\x89PNG\r\n\x1a\n"):
                errors.append(f"{ctx} ({ev_id}): invalid PNG magic header in {att}")

        probe = ev.get("probe")
        if probe is not None:
            probe_count += 1
            _validate_probe(probe, f"{ctx} ({ev_id})", global_probe_ids, errors)

    if max_day < 28:
        errors.append(f"{slug}/month.json: timeline ends at day {max_day}, expected >= 28")
    if probe_count < 10:
        errors.append(f"{slug}/month.json: expected >= 10 probes, found {probe_count}")
    if settle_count < 4:
        errors.append(f"{slug}/month.json: expected >= 4 settle checkpoints, found {settle_count}")

    stats = {
        "slug": slug,
        "name": profile.get("name", ""),
        "timezone": profile.get("timezone", ""),
        "presets": presets,
        "events": len(events) if isinstance(events, list) else 0,
        "chats": chat_count,
        "settles": settle_count,
        "probes": probe_count,
        "assets": len(referenced_assets),
        "max_day": max_day,
    }
    return errors, stats


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate Nori persona simulation bundles.")
    parser.add_argument("--persona", help="Validate only a single persona slug")
    parser.add_argument("--expect-count", type=int, default=10, help="Expected number of personas when validating all")
    args = parser.parse_args()

    if not PERSONAS_DIR.is_dir():
        print(f"ERROR: personas dir not found: {PERSONAS_DIR}", file=sys.stderr)
        return 1

    if args.persona:
        dirs = [PERSONAS_DIR / args.persona]
    else:
        dirs = sorted(
            d for d in PERSONAS_DIR.iterdir()
            if d.is_dir() and SLUG_RE.match(d.name)
        )

    all_errors: list[str] = []
    if not args.persona and len(dirs) != args.expect_count:
        all_errors.append(f"Expected {args.expect_count} persona directories, found {len(dirs)}")

    global_probe_ids: set[str] = set()
    rows: list[dict] = []
    for d in dirs:
        errs, stats = validate_persona(d, global_probe_ids)
        all_errors.extend(errs)
        if stats:
            rows.append(stats)

    if rows:
        print(f"{'Slug':<22} {'Events':>6} {'Chats':>6} {'Settles':>7} {'Probes':>6} {'Assets':>6} {'Days':>4}  Presets")
        print("-" * 82)
        for r in rows:
            print(
                f"{r['slug']:<22} {r['events']:>6} {r['chats']:>6} {r['settles']:>7} "
                f"{r['probes']:>6} {r['assets']:>6} {r['max_day']:>4}  {','.join(r['presets'])}"
            )
        print("-" * 82)
        print(
            f"TOTAL: {len(rows)} personas | {sum(r['events'] for r in rows)} events | "
            f"{sum(r['chats'] for r in rows)} chats | {sum(r['probes'] for r in rows)} probes | "
            f"{sum(r['assets'] for r in rows)} assets"
        )

    if all_errors:
        print("\nVALIDATION ERRORS:", file=sys.stderr)
        for e in all_errors:
            print(f"  - {e}", file=sys.stderr)
        return 1

    print("\nALL PERSONA BUNDLES VALID.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
