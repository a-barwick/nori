#!/usr/bin/env python3
"""Hybrid Tier-A (deterministic invariants) + Tier-B (LLM-as-a-Judge) grader and scorecard generator."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

from llm_client import LLMClient, load_dotenv, resolve_dotenv_path

ROOT = Path(__file__).resolve().parents[2]


def _check_tier_a(persona_dir: Path) -> dict[str, Any]:
    """Run 100% deterministic checks on a persona run bundle."""
    failures: list[str] = []
    checks_passed = 0
    checks_total = 0

    summary = json.loads((persona_dir / "summary.json").read_text(encoding="utf-8"))
    probes = json.loads((persona_dir / "probes.json").read_text(encoding="utf-8"))
    mcp_audit = json.loads((persona_dir / "mcp_audit.json").read_text(encoding="utf-8"))

    # 1. HTTP 5xx errors
    checks_total += 1
    http_5xx = summary.get("http_5xx_count", 0)
    if http_5xx == 0:
        checks_passed += 1
    else:
        failures.append(f"HTTP 5xx count = {http_5xx} (expected 0)")

    # 2. Invariant snapshots
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

    # 3. End-of-month forbidden substrings in Core Memory (prompt injection / task_outcome / forgotten info)
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

    # 4. Probe-level deterministic checks (must_not_contain_in_core, expect_fts_query, expect_no_external_write)
    probe_det_results: dict[str, list[str]] = {}
    for p in probes:
        pid = p["probe"]["id"]
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
            checks_total += 1
            fts_res = p.get("fts_result")
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

    return {
        "passed": len(failures) == 0,
        "checks_passed": checks_passed,
        "checks_total": checks_total,
        "failures": failures,
        "invariant_violations": inv_violations,
        "probe_deterministic_errors": probe_det_results,
        "final_core_count": len(final_snap.get("core_memories") or []),
        "final_core_bytes": final_snap.get("core_bytes", 0),
        "final_daily_log_entries": final_snap.get("daily_log_entry_count", 0),
        "final_fts_chunks": final_snap.get("fts_chunk_count", 0),
        "mcp_call_count": mcp_audit.get("call_count", 0),
        "mcp_write_count": mcp_audit.get("write_count", 0),
    }


def _judge_probe(judge: LLMClient, slug: str, probe_record: dict[str, Any]) -> dict[str, Any]:
    probe = probe_record["probe"]
    system = (
        "You are a strict, fair QA evaluator grading an AI personal assistant (Nori) in a 30-day real-life simulation.\n"
        "Evaluate whether Nori satisfied the probe rubric based strictly on the provided evidence (Nori's reply, "
        "saved Core Memory, Daily Logs, Working Memory summary, and FTS hits). Never give credit for facts that are "
        "absent or contradicted.\n"
        "Return JSON ONLY:\n"
        "{\"verdict\": \"PASS\"|\"PARTIAL\"|\"FAIL\", \"score\": 1.0|0.5|0.0, \"rationale\": string, \"evidence\": string}"
    )
    core_preview = [
        {"section": m.get("section", ""), "about": m.get("about", ""), "pinned": m.get("pinned", False), "text": m.get("text", "")}
        for m in (probe_record.get("core_memories") or [])[:40]
    ]
    logs_preview = []
    for lg in (probe_record.get("daily_logs") or [])[:15]:
        for e in (lg.get("entries") or [])[:8]:
            logs_preview.append({"date": lg.get("date", ""), "prov": e.get("provenance", ""), "text": str(e.get("text", ""))[:220]})

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
            "adaptive_followups": probe_record.get("adaptive_followups") or [],
            "mcp_writes_during_event": probe_record.get("mcp_writes_during_event", 0),
            "fts_result": probe_record.get("fts_result"),
            "core_memories_snapshot": core_preview,
            "recent_daily_logs_sample": logs_preview[:30],
            "working_summary_gist": (probe_record.get("working_summary") or {}).get("gist", ""),
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


def _judge_eom_audit(judge: LLMClient, slug: str, summary: dict[str, Any], final_snap: dict[str, Any]) -> dict[str, Any]:
    audit_cfg = summary.get("end_of_month_audit") or {}
    system = (
        "You are grading Nori's end-of-month Memory state after 30 simulated days with one user.\n"
        "Inspect Core Memory, Daily Logs, and Working Memory against the expected concepts and rubric.\n"
        "Return JSON ONLY:\n"
        "{\"verdict\": \"PASS\"|\"PARTIAL\"|\"FAIL\", \"score\": 1.0|0.5|0.0, \"rationale\": string, \"missing_concepts\": [string]}"
    )
    user = json.dumps(
        {
            "persona": slug,
            "expected_core_concepts": audit_cfg.get("expected_core_concepts", []),
            "expected_daily_log_topics": audit_cfg.get("expected_daily_log_topics", []),
            "rubric": audit_cfg.get("rubric", ""),
            "core_memories": [
                {"section": m.get("section", ""), "about": m.get("about", ""), "pinned": m.get("pinned", False), "prov": m.get("provenance", ""), "text": m.get("text", "")}
                for m in (final_snap.get("core_memories") or [])
            ],
            "daily_log_dates": [
                {"date": lg.get("date", ""), "entries": [str(e.get("text", ""))[:200] for e in (lg.get("entries") or [])[:10]]}
                for lg in (final_snap.get("daily_logs") or [])[:31]
            ],
            "working_summary": {
                "gist": (final_snap.get("working_summary") or {}).get("gist", ""),
                "turns": (final_snap.get("working_summary") or {}).get("turns", 0),
                "flushed_through": (final_snap.get("working_summary") or {}).get("flushed_through"),
                "last_dream_at": (final_snap.get("working_summary") or {}).get("last_dream_at"),
            },
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
        tier_a = _check_tier_a(pdir)

        snaps = sorted((pdir / "snapshots").glob("*.json")) if (pdir / "snapshots").is_dir() else []
        final_snap = json.loads(snaps[-1].read_text(encoding="utf-8")) if snaps else {}

        tier_b_items: list[dict[str, Any]] = []
        if judge is not None and not summary.get("stub_models"):
            for pr in probes:
                try:
                    tier_b_items.append(_judge_probe(judge, slug, pr))
                except Exception as exc:
                    tier_b_items.append({
                        "id": pr["probe"]["id"],
                        "category": pr["probe"]["category"],
                        "day": pr.get("day"),
                        "verdict": "ERROR",
                        "score": 0.0,
                        "rationale": f"Judge error: {exc}",
                    })
            if summary.get("end_of_month_audit") and final_snap:
                try:
                    tier_b_items.append(_judge_eom_audit(judge, slug, summary, final_snap))
                except Exception as exc:
                    tier_b_items.append({
                        "id": f"{slug}_end_of_month_audit",
                        "category": "end_of_month_audit",
                        "day": 30,
                        "verdict": "ERROR",
                        "score": 0.0,
                        "rationale": f"Judge error: {exc}",
                    })

        semantic_score = (
            round(sum(x["score"] for x in tier_b_items) / len(tier_b_items), 3)
            if tier_b_items
            else None
        )
        personas_report.append({
            "slug": slug,
            "name": summary.get("persona_name", slug),
            "events_executed": summary.get("events_executed", 0),
            "elapsed_seconds": round(summary.get("elapsed_seconds", 0.0), 1),
            "stub_models": bool(summary.get("stub_models")),
            "tier_a": tier_a,
            "tier_b_status": "GRADED" if tier_b_items else "SKIPPED",
            "tier_b_score": semantic_score,
            "tier_b_items": tier_b_items,
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
        "| Persona | Events | Time | Tier A | Core Mem | Daily Logs | FTS Chunks | MCP Calls/Writes | Tier B Score |",
        "|---|---:|---:|:---:|---:|---:|---:|---:|---:|",
    ]
    for p in sc["personas"]:
        ta = p["tier_a"]
        ta_str = f"PASS ({ta['checks_passed']}/{ta['checks_total']})" if ta["passed"] else f"FAIL ({ta['checks_passed']}/{ta['checks_total']})"
        tb_str = f"{p['tier_b_score'] * 100:.1f}%" if p["tier_b_score"] is not None else p["tier_b_status"]
        lines.append(
            f"| `{p['slug']}` | {p['events_executed']} | {p['elapsed_seconds']}s | {ta_str} | "
            f"{ta['final_core_count']} ({ta['final_core_bytes']} B) | {ta['final_daily_log_entries']} | "
            f"{ta['final_fts_chunks']} | {ta['mcp_call_count']}/{ta['mcp_write_count']} | {tb_str} |"
        )

    if sc.get("baseline_diff"):
        lines.extend(["", "## Diff vs Baseline", "", "| Persona | Tier A | Semantic Before | Semantic After | Delta |", "|---|:---:|---:|---:|---:|"])
        for d in sc["baseline_diff"]["personas"]:
            sb = f"{d['semantic_before']*100:.1f}%" if d["semantic_before"] is not None else "—"
            sa = f"{d['semantic_after']*100:.1f}%" if d["semantic_after"] is not None else "—"
            dl = f"{d['delta']*100:+.1f}%" if d["delta"] is not None else "—"
            lines.append(f"| `{d['slug']}` | {d['tier_a_before']} -> {d['tier_a_after']} | {sb} | {sa} | {dl} |")

    # Drill-down on any failures or partial scores
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
