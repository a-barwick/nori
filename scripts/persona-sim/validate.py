#!/usr/bin/env python3
"""Validate all persona bundles under sim/personas/."""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from zoneinfo import ZoneInfo

ROOT = Path(__file__).resolve().parents[2]
PERSONAS_DIR = ROOT / "sim" / "personas"

SLUG_RE = re.compile(r"^[0-9]{2}-[a-z0-9-]+$")
DATE_RE = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}$")
TIME_RE = re.compile(r"^[0-9]{2}:[0-9]{2}$")
ID_RE = re.compile(r"^[a-z0-9_]+$")

VALID_PRESETS = {"smoke", "memory-pack", "full"}
VALID_KINDS = {
    "chat",
    "settle",
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


def validate_timezone(tz: str, ctx: str, errors: list[str]) -> None:
    try:
        ZoneInfo(tz)
    except Exception:
        errors.append(f"{ctx}: invalid IANA timezone {tz!r}")


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
    presets = profile.get("presets", [])
    if not isinstance(presets, list) or not presets or not set(presets).issubset(VALID_PRESETS):
        errors.append(f"{slug}/profile.json: invalid presets {presets!r}")
    if len(str(profile.get("persona_responder_brief", ""))) < 40:
        errors.append(f"{slug}/profile.json: persona_responder_brief must be >= 40 chars")

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

    for idx, ev in enumerate(events):
        ctx = f"{slug}/month.json:events[{idx}]"
        if not isinstance(ev, dict):
            errors.append(f"{ctx}: must be object")
            continue
        ev_id = ev.get("id", "")
        if not isinstance(ev_id, str) or not ID_RE.match(ev_id):
            errors.append(f"{ctx}: invalid id {ev_id!r}")
        elif ev_id in seen_event_ids:
            errors.append(f"{ctx}: duplicate event id {ev_id!r}")
        seen_event_ids.add(ev_id)

        day = ev.get("day")
        local_time = ev.get("local_time", "")
        if not isinstance(day, int) or day < 1 or day > 31:
            errors.append(f"{ctx}: invalid day {day!r}")
        else:
            max_day = max(max_day, day)
        if not isinstance(local_time, str) or not TIME_RE.match(local_time):
            errors.append(f"{ctx}: invalid local_time {local_time!r}")
        else:
            hh, mm = int(local_time[:2]), int(local_time[3:])
            if hh > 23 or mm > 59:
                errors.append(f"{ctx}: out-of-range local_time {local_time!r}")
        if isinstance(day, int) and isinstance(local_time, str) and TIME_RE.match(local_time):
            key = (day, local_time)
            if prev_key is not None and key < prev_key:
                errors.append(f"{ctx} ({ev_id}): non-monotonic timestamp {key} < {prev_key}")
            prev_key = key

        if "timezone" in ev and ev["timezone"]:
            validate_timezone(ev["timezone"], ctx, errors)

        kind = ev.get("kind")
        if kind not in VALID_KINDS:
            errors.append(f"{ctx}: unknown kind {kind!r}")
        if kind == "chat":
            chat_count += 1
            if not isinstance(ev.get("text"), str) or not ev["text"].strip():
                errors.append(f"{ctx} ({ev_id}): chat event requires non-empty text")
        elif kind == "settle":
            settle_count += 1
        elif kind in {"memory_ui", "mcp_world_event", "library_ui", "automation_ui", "goal_ui", "permission_ui", "interaction_reply"}:
            if not ev.get("action"):
                errors.append(f"{ctx} ({ev_id}): {kind} requires action")

        for att in ev.get("attachments", []) or []:
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
            if not isinstance(probe, dict):
                errors.append(f"{ctx} ({ev_id}): probe must be object")
            else:
                pid = probe.get("id", "")
                if not pid:
                    errors.append(f"{ctx} ({ev_id}): probe missing id")
                elif pid in global_probe_ids:
                    errors.append(f"{ctx} ({ev_id}): duplicate global probe id {pid!r}")
                else:
                    global_probe_ids.add(pid)
                if not probe.get("category"):
                    errors.append(f"{ctx} ({ev_id}): probe missing category")
                if len(str(probe.get("rubric", ""))) < 20:
                    errors.append(f"{ctx} ({ev_id}): probe rubric too short")

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
        "events": len(events),
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
