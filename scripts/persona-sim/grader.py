#!/usr/bin/env python3
"""Hybrid Tier-A (deterministic invariants) + Tier-B (LLM-as-a-Judge) grader and scorecard generator."""

from __future__ import annotations

import argparse
import json
import re
import sys
import time
import urllib.error
from pathlib import Path
from typing import Any, Callable

from llm_client import LLMClient, load_dotenv, resolve_dotenv_path

ROOT = Path(__file__).resolve().parents[2]

MAX_ACK_WORDS = 15
_CARD_BLOCK_RE = re.compile(r"<card\b[^>]*>.*?</card>", re.IGNORECASE | re.DOTALL)


def _strip_cards(text: str) -> tuple[str, bool]:
    has_card = bool(_CARD_BLOCK_RE.search(text or ""))
    chat_only = _CARD_BLOCK_RE.sub(" ", text or "").strip()
    return chat_only, has_card


def _percentile(sorted_vals: list[int], pct: float) -> int:
    if not sorted_vals:
        return 0
    idx = min(len(sorted_vals) - 1, max(0, int(round((len(sorted_vals) - 1) * pct))))
    return sorted_vals[idx]


def _analyze_response_brevity(persona_dir: Path) -> dict[str, Any]:
    """Compute ack and chat-reply brevity metrics from trace.jsonl."""
    trace_path = persona_dir / "trace.jsonl"
    ack_words: list[int] = []
    chat_words: list[int] = []
    card_replies = 0
    bloated_acks: list[dict[str, Any]] = []

    if trace_path.is_file():
        for raw in trace_path.read_text(encoding="utf-8", errors="replace").splitlines():
            raw = raw.strip()
            if not raw:
                continue
            try:
                ev = json.loads(raw)
            except json.JSONDecodeError:
                continue
            if not isinstance(ev, dict) or ev.get("kind") != "chat":
                continue
            msgs = ev.get("assistant_messages")
            if isinstance(msgs, list) and msgs:
                for m in msgs:
                    if not isinstance(m, dict):
                        continue
                    text = str(m.get("text", "") or "").strip()
                    if not text:
                        continue
                    chat_only, has_card = _strip_cards(text)
                    has_card = bool(m.get("has_card")) or has_card
                    words = int(m["chat_words"]) if isinstance(m.get("chat_words"), int) else len(chat_only.split())
                    if m.get("has_actions"):
                        ack_words.append(words)
                        if words > MAX_ACK_WORDS:
                            bloated_acks.append({
                                "event_id": ev.get("id", ""),
                                "words": words,
                                "preview": text[:140],
                            })
                    else:
                        chat_words.append(words)
                        if has_card:
                            card_replies += 1
            elif ev.get("nori_reply"):
                chat_only, has_card = _strip_cards(str(ev.get("nori_reply", "")))
                if chat_only:
                    chat_words.append(len(chat_only.split()))
                if has_card:
                    card_replies += 1

    ack_sorted = sorted(ack_words)
    chat_sorted = sorted(chat_words)
    return {
        "max_ack_words_limit": MAX_ACK_WORDS,
        "ack_count": len(ack_sorted),
        "ack_avg_words": round(sum(ack_sorted) / len(ack_sorted), 1) if ack_sorted else 0.0,
        "ack_max_words": ack_sorted[-1] if ack_sorted else 0,
        "bloated_acks": bloated_acks,
        "chat_reply_count": len(chat_sorted),
        "chat_median_words": _percentile(chat_sorted, 0.5),
        "chat_p90_words": _percentile(chat_sorted, 0.9),
        "chat_max_words": chat_sorted[-1] if chat_sorted else 0,
        "card_reply_count": card_replies,
    }


def compute_tier_b_score(items: list[dict[str, Any]]) -> float | None:
    """Compute average Tier-B score excluding ERROR and INCONCLUSIVE items."""
    scored = [
        x for x in items
        if x.get("verdict") not in ("ERROR", "INCONCLUSIVE") and x.get("score") is not None
    ]
    if not scored:
        return None
    return round(sum(float(x["score"]) for x in scored) / len(scored), 3)


def tier_b_counts_and_status(items: list[dict[str, Any]]) -> tuple[int, int, int, str]:
    """Return (scored_count, error_count, inconclusive_count, status)."""
    error_count = sum(1 for x in items if x.get("verdict") == "ERROR")
    inconclusive_count = sum(1 for x in items if x.get("verdict") == "INCONCLUSIVE")
    scored_count = sum(
        1 for x in items
        if x.get("verdict") not in ("ERROR", "INCONCLUSIVE") and x.get("score") is not None
    )
    if scored_count > 0:
        status = "GRADED"
    elif inconclusive_count > 0:
        status = "INCONCLUSIVE"
    elif error_count > 0:
        status = "ERROR"
    else:
        status = "SKIPPED"
    return scored_count, error_count, inconclusive_count, status


def _is_retryable_judge_error(exc: Exception) -> bool:
    if isinstance(exc, urllib.error.HTTPError):
        return exc.code == 429 or exc.code >= 500
    if isinstance(exc, (urllib.error.URLError, TimeoutError, OSError, json.JSONDecodeError)):
        return True
    if isinstance(exc, RuntimeError):
        msg = str(exc)
        if any(code in msg for code in ("LLM API 400", "LLM API 401", "LLM API 403")):
            return False
        if "LLM API 429" in msg or "LLM API 5" in msg or "returned no choices" in msg:
            return True
    return False


def _judge_with_retry(
    fn: Callable[..., dict[str, Any]],
    *args: Any,
    max_retries: int = 3,
    base_delay: float = 2.0,
    sleep_fn: Callable[[float], None] = time.sleep,
) -> dict[str, Any]:
    """Call a judge function with exponential backoff on transient HTTP/network errors."""
    last_exc: Exception | None = None
    for attempt in range(max_retries + 1):
        try:
            return fn(*args)
        except Exception as exc:
            last_exc = exc
            if attempt >= max_retries or not _is_retryable_judge_error(exc):
                raise
            sleep_fn(base_delay * (2 ** attempt))
    assert last_exc is not None
    raise last_exc


def _load_json_if_exists(path: Path) -> Any:
    if not path.is_file():
        return None
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except Exception:
        return None


def _extract_approval_log(
    persona_dir: Path,
    summary: dict[str, Any] | None = None,
    trace_lines: list[dict[str, Any]] | None = None,
) -> list[dict[str, Any]]:
    """Collect deduplicated approval/interaction decisions from files, summary.json, or trace.jsonl."""
    log: list[dict[str, Any]] = []
    seen_keys: set[tuple[str, ...]] = set()
    seen_interaction_ids: set[str] = set()

    def _add_structured(item: dict[str, Any]) -> None:
        item_id = item.get("id")
        if item_id:
            status = str(item.get("status") or item.get("decision") or "")
            key: tuple[str, ...] = ("id_status", str(item_id), status)
            seen_interaction_ids.add(str(item_id))
        else:
            key = ("json", json.dumps(item, sort_keys=True, ensure_ascii=False))
        if key not in seen_keys:
            seen_keys.add(key)
            log.append(item)

    for fname in ("interactions.json", "approvals.json"):
        data = _load_json_if_exists(persona_dir / fname)
        if isinstance(data, list):
            for item in data:
                if isinstance(item, dict):
                    _add_structured(item)
        elif isinstance(data, dict):
            for key in ("items", "interactions", "approvals"):
                if isinstance(data.get(key), list):
                    for item in data[key]:
                        if isinstance(item, dict):
                            _add_structured(item)

    if isinstance(summary, dict):
        for key in ("interactions", "approvals"):
            if isinstance(summary.get(key), list):
                for item in summary[key]:
                    if isinstance(item, dict):
                        _add_structured(item)

    if trace_lines is None:
        trace_path = persona_dir / "trace.jsonl"
        if trace_path.is_file():
            trace_lines = []
            for raw in trace_path.read_text(encoding="utf-8", errors="replace").splitlines():
                raw = raw.strip()
                if not raw:
                    continue
                try:
                    obj = json.loads(raw)
                    if isinstance(obj, dict):
                        trace_lines.append(obj)
                except json.JSONDecodeError:
                    continue

    if trace_lines:
        for ev in trace_lines:
            if ev.get("kind") == "interaction_reply":
                res = ev.get("result")
                res_items = res if isinstance(res, list) else ([res] if isinstance(res, dict) else [])
                res_ids = [str(r["id"]) for r in res_items if isinstance(r, dict) and r.get("id")]
                has_stale = any(isinstance(r, dict) and r.get("stale_verified") for r in res_items)
                if not (res_ids and not has_stale and all(rid in seen_interaction_ids for rid in res_ids)):
                    entry = {
                        "source": "trace.interaction_reply",
                        "id": ev.get("id"),
                        "day": ev.get("day"),
                        "local_time": ev.get("local_time"),
                        "sim_utc": ev.get("sim_utc"),
                        "action": ev.get("action"),
                        "result": res,
                    }
                    tkey: tuple[str, ...] = ("trace", json.dumps(entry, sort_keys=True, ensure_ascii=False))
                    if tkey not in seen_keys:
                        seen_keys.add(tkey)
                        log.append(entry)
            for fu in ev.get("adaptive_followups") or []:
                if isinstance(fu, dict) and (
                    fu.get("type") == "interaction"
                    or "interaction_id" in fu
                    or fu.get("kind") in ("approve", "decline", "answer")
                ):
                    iid = fu.get("interaction_id")
                    if iid and str(iid) in seen_interaction_ids:
                        continue
                    entry = {
                        "source": "trace.adaptive_followup",
                        "event_id": ev.get("id"),
                        "day": ev.get("day"),
                        "local_time": ev.get("local_time"),
                        **fu,
                    }
                    tkey = ("trace", json.dumps(entry, sort_keys=True, ensure_ascii=False))
                    if tkey not in seen_keys:
                        seen_keys.add(tkey)
                        log.append(entry)

    return log


def _check_tier_a(persona_dir: Path) -> dict[str, Any]:
    """Run 100% deterministic checks on a persona run bundle."""
    failures: list[str] = []
    checks_passed = 0
    checks_total = 0

    summary = json.loads((persona_dir / "summary.json").read_text(encoding="utf-8"))
    probes = json.loads((persona_dir / "probes.json").read_text(encoding="utf-8"))
    mcp_audit = json.loads((persona_dir / "mcp_audit.json").read_text(encoding="utf-8"))

    checks_total += 1
    http_5xx = int(summary.get("http_5xx_count", 0) or 0)
    if http_5xx == 0:
        checks_passed += 1
    else:
        failures.append(f"HTTP 5xx count = {http_5xx} (expected 0)")

    harness_timeouts = int(summary.get("harness_timeouts", 0) or 0)

    checks_total += 1
    ui_errors_raw = summary.get("ui_errors")
    if ui_errors_raw is None:
        ui_error_count = 0
        checks_passed += 1
    elif isinstance(ui_errors_raw, int) and not isinstance(ui_errors_raw, bool):
        ui_error_count = ui_errors_raw
        if ui_error_count == 0:
            checks_passed += 1
        else:
            failures.append(f"UI handler errors = {ui_error_count} (expected 0)")
    elif isinstance(ui_errors_raw, list):
        ui_error_count = len(ui_errors_raw)
        if ui_error_count == 0:
            checks_passed += 1
        else:
            sample = "; ".join(str(x) for x in ui_errors_raw[:5])
            failures.append(f"UI handler errors ({ui_error_count}): {sample}")
    else:
        ui_error_count = 1
        failures.append(f"UI handler errors invalid value: {ui_errors_raw!r}")

    snaps_dir = persona_dir / "snapshots"
    snap_files = sorted(snaps_dir.glob("*.json")) if snaps_dir.is_dir() else []
    inv_violations: list[dict[str, Any]] = []
    final_snap: dict[str, Any] = {}
    for sf in snap_files:
        snap = json.loads(sf.read_text(encoding="utf-8"))
        final_snap = snap
        checks_total += 1
        if snap.get("invariants_ok", False):
            checks_passed += 1
        else:
            vs = snap.get("invariant_violations") or []
            inv_violations.extend([{"snapshot": sf.stem, **v} for v in vs])
            failures.append(f"Snapshot {sf.stem}: {len(vs)} invariant violation(s)")

    audit_cfg = summary.get("end_of_month_audit") or {}
    forbidden_core = audit_cfg.get("forbidden_core_substrings") or []
    core_texts = [str(m.get("text", "")) for m in (final_snap.get("core_memories") or [])]
    joined_core = "\n".join(core_texts).lower()
    for sub in forbidden_core:
        checks_total += 1
        if sub.lower() not in joined_core:
            checks_passed += 1
        else:
            failures.append(f"Forbidden substring leaked into final Core Memory: {sub!r}")

    probe_det_results: dict[str, list[str]] = {}
    inconclusive_probes = 0
    for p in probes:
        pid = p["probe"]["id"]
        is_inconclusive = bool(p.get("inconclusive"))
        if is_inconclusive:
            inconclusive_probes += 1
        p_errs: list[str] = []
        p_core = "\n".join(str(m.get("text", "")) for m in (p.get("core_memories") or [])).lower()

        for sub in p["probe"].get("must_not_contain_in_core") or []:
            checks_total += 1
            if sub.lower() not in p_core:
                checks_passed += 1
            else:
                msg = f"Probe {pid}: forbidden substring {sub!r} found in Core Memory"
                p_errs.append(msg)
                failures.append(msg)

        fts_spec = p["probe"].get("expect_fts_query")
        if fts_spec is not None:
            fts_res = p.get("fts_result")
            if is_inconclusive and fts_res is None:
                p_errs.append(f"Probe {pid}: FTS check skipped (probe inconclusive)")
            else:
                checks_total += 1
                hits = len((fts_res or {}).get("items") or [])
                min_hits = int(fts_spec.get("min_hits", 0))
                max_hits = fts_spec.get("max_hits")
                ok = hits >= min_hits and (max_hits is None or hits <= int(max_hits))
                if ok:
                    checks_passed += 1
                else:
                    msg = (
                        f"Probe {pid}: FTS query {fts_spec.get('query')!r} returned {hits} hits "
                        f"(expected min={min_hits}, max={max_hits})"
                    )
                    p_errs.append(msg)
                    failures.append(msg)

        if p["probe"].get("expect_no_external_write"):
            checks_total += 1
            writes_delta = p.get("mcp_writes_during_event", 0)
            if writes_delta == 0:
                checks_passed += 1
            else:
                msg = f"Probe {pid}: expected 0 external MCP writes during event, saw {writes_delta}"
                p_errs.append(msg)
                failures.append(msg)

        probe_det_results[pid] = p_errs

    brevity = _analyze_response_brevity(persona_dir)
    if brevity["ack_count"] > 0:
        checks_total += 1
        if not brevity["bloated_acks"]:
            checks_passed += 1
        else:
            sample = "; ".join(
                f"{b['event_id']} ({b['words']}w: {b['preview']!r})"
                for b in brevity["bloated_acks"][:3]
            )
            failures.append(
                f"Bloated task acknowledgment(s) > {MAX_ACK_WORDS} words "
                f"({len(brevity['bloated_acks'])}/{brevity['ack_count']}): {sample}"
            )

    return {
        "passed": len(failures) == 0,
        "checks_passed": checks_passed,
        "checks_total": checks_total,
        "failures": failures,
        "invariant_violations": inv_violations,
        "probe_deterministic_errors": probe_det_results,
        "harness_timeouts": harness_timeouts,
        "ui_error_count": ui_error_count,
        "inconclusive_probes": inconclusive_probes,
        "brevity": brevity,
        "final_core_count": len(final_snap.get("core_memories") or []),
        "final_core_bytes": final_snap.get("core_bytes", 0),
        "final_daily_log_entries": final_snap.get("daily_log_entry_count", 0),
        "final_fts_chunks": final_snap.get("fts_chunk_count", 0),
        "mcp_call_count": mcp_audit.get("call_count", 0),
        "mcp_write_count": mcp_audit.get("write_count", 0),
    }


def _format_core_memories(core_memories: list[dict[str, Any]] | None) -> list[dict[str, Any]]:
    return [
        {
            "section": m.get("section", ""),
            "about": m.get("about", ""),
            "pinned": m.get("pinned", False),
            "prov": m.get("provenance", ""),
            "text": m.get("text", ""),
        }
        for m in (core_memories or [])
    ]


def _format_daily_logs(daily_logs: list[dict[str, Any]] | None) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    for lg in (daily_logs or []):
        entries: list[dict[str, Any]] = []
        for e in (lg.get("entries") or []):
            entry: dict[str, Any] = {
                "prov": e.get("provenance", ""),
                "text": str(e.get("text", "")),
            }
            if e.get("source_text"):
                entry["source_text"] = str(e["source_text"])
            entries.append(entry)
        out.append({"date": lg.get("date", ""), "entries": entries})
    return out


def _format_working_summary(ws: dict[str, Any] | None) -> dict[str, Any]:
    ws = ws or {}
    return {
        "gist": ws.get("gist", ""),
        "items": ws.get("items") or [],
        "turns": ws.get("turns", 0),
        "extracted_through": ws.get("extracted_through"),
        "flushed_through": ws.get("flushed_through"),
        "last_dream_at": ws.get("last_dream_at"),
        "flush_failures": ws.get("flush_failures", 0),
        "extract_failures": ws.get("extract_failures", 0),
    }


def _format_show_results(results: list[dict[str, Any]] | None) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    for r in (results or []):
        if not isinstance(r, dict):
            continue
        out.append({
            "id": r.get("id"),
            "kind": r.get("kind"),
            "title": r.get("title"),
            "text": r.get("text"),
            "name": r.get("name"),
            "url": r.get("url"),
            "evidence": r.get("evidence"),
            "unmet": r.get("unmet"),
        })
    return out


def _judge_probe(
    judge: LLMClient,
    slug: str,
    probe_record: dict[str, Any],
    mcp_audit: dict[str, Any] | None = None,
    approval_log: list[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    probe = probe_record["probe"]
    system = (
        "You are a strict, fair QA evaluator grading an AI personal assistant (Nori) in a 30-day real-life simulation.\n"
        "Evaluate whether Nori satisfied the probe rubric based strictly on the provided evidence (Nori's reply, "
        "show_results cards, saved Core Memory, Daily Logs, Working Memory summary and items, FTS hits, "
        "mcp_audit_writes, and approval_log). Never give credit for facts that are absent or contradicted.\n"
        "- Evaluate nori_reply together with show_results cards: Nori often summarizes briefly in chat while presenting full details or figures in a show_results card.\n"
        "- Treat parenthetical '(bonus: ...)' or explicitly optional clauses in the rubric as non-scoring bonus context: do NOT penalize or downgrade a PASS to PARTIAL solely for omitting an optional/bonus detail when all required criteria are satisfied.\n"
        "- Verify any claim about external actions (sent emails, calendar events, issue updates, approvals/declines) against mcp_audit_writes and approval_log; never assume an external write occurred if mcp_audit_writes has no matching write.\n"
        "Return JSON ONLY:\n"
        "{\"verdict\": \"PASS\"|\"PARTIAL\"|\"FAIL\", \"score\": 1.0|0.5|0.0, \"rationale\": string, \"evidence\": string}"
    )
    core_full = _format_core_memories(probe_record.get("core_memories"))
    logs_full = _format_daily_logs(probe_record.get("daily_logs"))
    ws_full = _format_working_summary(probe_record.get("working_summary"))
    show_results = _format_show_results(probe_record.get("show_results"))
    mcp_writes = (mcp_audit or {}).get("writes") or []

    user = json.dumps(
        {
            "persona": slug,
            "day": probe_record.get("day"),
            "local_time": probe_record.get("local_time"),
            "probe_id": probe["id"],
            "category": probe["category"],
            "rubric": probe["rubric"],
            "user_message": probe_record.get("user_text", ""),
            "nori_reply": probe_record.get("nori_reply", ""),
            "show_results": show_results,
            "adaptive_followups": probe_record.get("adaptive_followups") or [],
            "mcp_writes_during_event": probe_record.get("mcp_writes_during_event", 0),
            "mcp_audit_writes": mcp_writes,
            "approval_log": approval_log or [],
            "fts_result": probe_record.get("fts_result"),
            "core_memories_snapshot": core_full,
            "daily_logs": logs_full,
            "working_summary": ws_full,
        },
        ensure_ascii=False,
    )
    res = judge.chat_json(system, user, temperature=0.0)
    verdict = str(res.get("verdict", "FAIL")).upper()
    if verdict not in ("PASS", "PARTIAL", "FAIL"):
        verdict = "FAIL"
    score = 1.0 if verdict == "PASS" else (0.5 if verdict == "PARTIAL" else 0.0)
    return {
        "id": probe["id"],
        "category": probe["category"],
        "day": probe_record.get("day"),
        "verdict": verdict,
        "score": score,
        "rationale": str(res.get("rationale", "")),
        "evidence": str(res.get("evidence", "")),
    }


def _judge_eom_audit(
    judge: LLMClient,
    slug: str,
    summary: dict[str, Any],
    final_snap: dict[str, Any],
    mcp_audit: dict[str, Any] | None = None,
    approval_log: list[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    audit_cfg = summary.get("end_of_month_audit") or {}
    system = (
        "You are grading Nori's end-of-month Memory state after 30 simulated days with one user.\n"
        "Inspect Core Memory, Daily Logs, Working Memory (gist and items), mcp_audit_writes, and approval_log "
        "against the expected concepts and rubric.\n"
        "Return JSON ONLY:\n"
        "{\"verdict\": \"PASS\"|\"PARTIAL\"|\"FAIL\", \"score\": 1.0|0.5|0.0, \"rationale\": string, \"missing_concepts\": [string]}"
    )
    user = json.dumps(
        {
            "persona": slug,
            "expected_core_concepts": audit_cfg.get("expected_core_concepts", []),
            "expected_daily_log_topics": audit_cfg.get("expected_daily_log_topics", []),
            "rubric": audit_cfg.get("rubric", ""),
            "core_memories": _format_core_memories(final_snap.get("core_memories")),
            "daily_log_dates": _format_daily_logs(final_snap.get("daily_logs")),
            "working_summary": _format_working_summary(final_snap.get("working_summary")),
            "mcp_audit_writes": (mcp_audit or {}).get("writes") or [],
            "approval_log": approval_log or [],
        },
        ensure_ascii=False,
    )
    res = judge.chat_json(system, user, temperature=0.0)
    verdict = str(res.get("verdict", "FAIL")).upper()
    if verdict not in ("PASS", "PARTIAL", "FAIL"):
        verdict = "FAIL"
    score = 1.0 if verdict == "PASS" else (0.5 if verdict == "PARTIAL" else 0.0)
    return {
        "id": f"{slug}_end_of_month_audit",
        "category": "end_of_month_audit",
        "day": 30,
        "verdict": verdict,
        "score": score,
        "rationale": str(res.get("rationale", "")),
        "missing_concepts": res.get("missing_concepts") or [],
    }


def grade_run(run_dir: Path, deterministic_only: bool = False, compare_path: Path | None = None) -> dict[str, Any]:
    dotenv = load_dotenv(resolve_dotenv_path(ROOT))
    judge = None if deterministic_only else LLMClient.from_env("judge", dotenv)

    persona_dirs = sorted(
        d for d in run_dir.iterdir()
        if d.is_dir() and (d / "summary.json").is_file()
    )
    if not persona_dirs:
        raise RuntimeError(f"No completed persona bundles found in {run_dir}")

    personas_report: list[dict[str, Any]] = []
    for pdir in persona_dirs:
        slug = pdir.name
        summary = json.loads((pdir / "summary.json").read_text(encoding="utf-8"))
        probes = json.loads((pdir / "probes.json").read_text(encoding="utf-8"))
        mcp_audit = _load_json_if_exists(pdir / "mcp_audit.json") or {}
        approval_log = _extract_approval_log(pdir, summary)
        tier_a = _check_tier_a(pdir)

        snaps = sorted((pdir / "snapshots").glob("*.json")) if (pdir / "snapshots").is_dir() else []
        final_snap = json.loads(snaps[-1].read_text(encoding="utf-8")) if snaps else {}

        tier_b_items: list[dict[str, Any]] = []
        if judge is not None and not summary.get("stub_models"):
            for pr in probes:
                if pr.get("inconclusive"):
                    tier_b_items.append({
                        "id": pr["probe"]["id"],
                        "category": pr["probe"]["category"],
                        "day": pr.get("day"),
                        "verdict": "INCONCLUSIVE",
                        "score": None,
                        "rationale": pr.get("inconclusive_reason")
                        or "Harness drain timeout (504) occurred before or during probe capture.",
                        "evidence": "",
                    })
                    continue
                try:
                    tier_b_items.append(
                        _judge_with_retry(_judge_probe, judge, slug, pr, mcp_audit, approval_log)
                    )
                except Exception as exc:
                    tier_b_items.append({
                        "id": pr["probe"]["id"],
                        "category": pr["probe"]["category"],
                        "day": pr.get("day"),
                        "verdict": "ERROR",
                        "score": None,
                        "rationale": f"Judge error: {exc}",
                    })
            eom_applicable = not summary.get("horizon_truncated") and (
                summary.get("max_days") is None or int(summary["max_days"]) >= 30
            )
            if eom_applicable and summary.get("end_of_month_audit") and final_snap:
                try:
                    tier_b_items.append(
                        _judge_with_retry(_judge_eom_audit, judge, slug, summary, final_snap, mcp_audit, approval_log)
                    )
                except Exception as exc:
                    tier_b_items.append({
                        "id": f"{slug}_end_of_month_audit",
                        "category": "end_of_month_audit",
                        "day": 30,
                        "verdict": "ERROR",
                        "score": None,
                        "rationale": f"Judge error: {exc}",
                    })

        semantic_score = compute_tier_b_score(tier_b_items)
        scored_count, error_count, inconclusive_count, tb_status = tier_b_counts_and_status(tier_b_items)
        personas_report.append({
            "slug": slug,
            "name": summary.get("persona_name", slug),
            "events_executed": summary.get("events_executed", 0),
            "max_days": summary.get("max_days"),
            "horizon_truncated": bool(summary.get("horizon_truncated")),
            "elapsed_seconds": round(summary.get("elapsed_seconds", 0.0), 1),
            "stub_models": bool(summary.get("stub_models")),
            "tier_a": tier_a,
            "tier_b_status": tb_status,
            "tier_b_score": semantic_score,
            "tier_b_scored_count": scored_count,
            "tier_b_error_count": error_count,
            "tier_b_inconclusive_count": inconclusive_count,
            "tier_b_items": tier_b_items,
            "model_usage": summary.get("model_usage"),
        })

    all_tier_a_ok = all(p["tier_a"]["passed"] for p in personas_report)
    graded_scores = [p["tier_b_score"] for p in personas_report if p["tier_b_score"] is not None]
    overall_semantic = round(sum(graded_scores) / len(graded_scores), 3) if graded_scores else None

    baseline_diff = None
    if compare_path and compare_path.is_file():
        baseline = json.loads(compare_path.read_text(encoding="utf-8"))
        base_map = {p["slug"]: p for p in baseline.get("personas", [])}
        diffs = []
        for p in personas_report:
            b = base_map.get(p["slug"])
            if not b:
                continue
            diffs.append({
                "slug": p["slug"],
                "tier_a_before": b["tier_a"]["passed"],
                "tier_a_after": p["tier_a"]["passed"],
                "semantic_before": b.get("tier_b_score"),
                "semantic_after": p.get("tier_b_score"),
                "delta": (
                    round(p["tier_b_score"] - b["tier_b_score"], 3)
                    if p.get("tier_b_score") is not None and b.get("tier_b_score") is not None
                    else None
                ),
            })
        baseline_diff = {"baseline": str(compare_path), "personas": diffs}

    scorecard = {
        "run_dir": str(run_dir),
        "judge_model": judge.model if judge else None,
        "all_tier_a_passed": all_tier_a_ok,
        "overall_semantic_score": overall_semantic,
        "personas": personas_report,
        "baseline_diff": baseline_diff,
    }
    (run_dir / "scorecard.json").write_text(json.dumps(scorecard, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    (run_dir / "scorecard.md").write_text(_render_markdown(scorecard), encoding="utf-8")
    return scorecard


def _persona_cost_str(p: dict[str, Any]) -> str:
    mu = p.get("model_usage")
    if isinstance(mu, dict) and mu.get("total_cost_usd") is not None:
        return f"${float(mu['total_cost_usd']):.3f}"
    if isinstance(mu, list):
        total = sum(float(r.get("cost_usd", 0) or 0) for r in mu if isinstance(r, dict))
        return f"${total:.3f}"
    return "—"


def _render_markdown(sc: dict[str, Any]) -> str:
    sem_str = (
        f"{sc['overall_semantic_score'] * 100:.1f}%"
        if sc["overall_semantic_score"] is not None
        else "SKIPPED"
    )
    lines: list[str] = [
        "# Nori Real-Life Persona Simulation Scorecard",
        "",
        f"- **Run:** `{sc['run_dir']}`",
        f"- **Tier A (Deterministic Invariants):** {'PASS' if sc['all_tier_a_passed'] else 'FAIL'}",
        f"- **Tier B (Semantic Judge `{sc['judge_model'] or 'skipped'}`):** {sem_str}",
        "",
        "## Per-Persona Summary",
        "",
        "| Persona | Events | Time | Tier A | Core Mem | Daily Logs | FTS Chunks | MCP Calls/Writes | Drain 504 | Inconcl. | Judge Err | Cost | Tier B Score |",
        "|---|---:|---:|:---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for p in sc["personas"]:
        ta = p["tier_a"]
        ta_str = f"PASS ({ta['checks_passed']}/{ta['checks_total']})" if ta["passed"] else f"FAIL ({ta['checks_passed']}/{ta['checks_total']})"
        tb_str = f"{p['tier_b_score'] * 100:.1f}%" if p["tier_b_score"] is not None else p["tier_b_status"]
        timeouts = ta.get("harness_timeouts", 0)
        inconcl = p.get("tier_b_inconclusive_count", ta.get("inconclusive_probes", 0))
        jerr = p.get("tier_b_error_count", 0)
        cost_str = _persona_cost_str(p)
        lines.append(
            f"| `{p['slug']}` | {p['events_executed']} | {p['elapsed_seconds']}s | {ta_str} | "
            f"{ta['final_core_count']} ({ta['final_core_bytes']} B) | {ta['final_daily_log_entries']} | "
            f"{ta['final_fts_chunks']} | {ta['mcp_call_count']}/{ta['mcp_write_count']} | "
            f"{timeouts} | {inconcl} | {jerr} | {cost_str} | {tb_str} |"
        )

    brevity_rows = [
        p for p in sc["personas"]
        if (p["tier_a"].get("brevity") or {}).get("ack_count", 0) > 0
        or (p["tier_a"].get("brevity") or {}).get("chat_reply_count", 0) > 0
    ]
    if brevity_rows:
        lines.extend([
            "",
            "## Response Brevity",
            "",
            "| Persona | Acks (avg / max words) | Bloated Acks | Chat Replies (med / p90 / max words) | Card Replies |",
            "|---|---:|---:|---:|---:|",
        ])
        for p in brevity_rows:
            br = p["tier_a"]["brevity"]
            ack_str = f"{br['ack_avg_words']} / {br['ack_max_words']} ({br['ack_count']})" if br["ack_count"] else "—"
            chat_str = f"{br['chat_median_words']} / {br['chat_p90_words']} / {br['chat_max_words']} ({br['chat_reply_count']})" if br["chat_reply_count"] else "—"
            lines.append(
                f"| `{p['slug']}` | {ack_str} | {len(br['bloated_acks'])} | {chat_str} | {br['card_reply_count']} |"
            )

    if sc.get("baseline_diff"):
        lines.extend(["", "## Diff vs Baseline", "", "| Persona | Tier A | Semantic Before | Semantic After | Delta |", "|---|:---:|---:|---:|---:|"])
        for d in sc["baseline_diff"]["personas"]:
            sb = f"{d['semantic_before']*100:.1f}%" if d["semantic_before"] is not None else "—"
            sa = f"{d['semantic_after']*100:.1f}%" if d["semantic_after"] is not None else "—"
            dl = f"{d['delta']*100:+.1f}%" if d["delta"] is not None else "—"
            lines.append(f"| `{d['slug']}` | {d['tier_a_before']} -> {d['tier_a_after']} | {sb} | {sa} | {dl} |")

    issues_section: list[str] = []
    for p in sc["personas"]:
        ta_fails = p["tier_a"]["failures"]
        tb_not_pass = [x for x in p["tier_b_items"] if x["verdict"] != "PASS"]
        if ta_fails or tb_not_pass:
            issues_section.append(f"### `{p['slug']}`")
            for f in ta_fails:
                issues_section.append(f"- **[Tier A FAIL]** {f}")
            for x in tb_not_pass:
                issues_section.append(
                    f"- **[Tier B {x['verdict']}]** `{x['id']}` (Day {x.get('day')}, `{x['category']}`): {x.get('rationale', '')}"
                )
    if issues_section:
        lines.extend(["", "## Findings & Regressions", ""] + issues_section)
    else:
        lines.extend(["", "## Findings & Regressions", "", "None — every evaluated check passed."])

    return "\n".join(lines) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description="Grade a completed Nori persona simulation run.")
    parser.add_argument("run_dir", type=Path, help="Path to .sim-runs/<run-id> directory")
    parser.add_argument("--deterministic-only", action="store_true", help="Skip LLM-as-a-Judge Tier B grading")
    parser.add_argument("--compare", type=Path, help="Optional baseline scorecard.json to diff against")
    args = parser.parse_args()

    sc = grade_run(args.run_dir, deterministic_only=args.deterministic_only, compare_path=args.compare)
    print((args.run_dir / "scorecard.md").read_text(encoding="utf-8"))
    return 0 if sc["all_tier_a_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
