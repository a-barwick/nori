#!/usr/bin/env python3
"""Run a persona VST suite with one isolated runner process (and one ephemeral DB) per persona,
then grade all bundles together and optionally diff against a baseline scorecard.

Why not `runner.py --concurrency N`? That shares one ephemeral DB across personas, and every
`nori-sim` worker pool then claims every user's work with its own virtual clock and artifact dir
(see docs/vst-findings.md, H-1). Running each persona in its own `runner.py --persona` process keeps
them fully isolated.

Starts are staggered so concurrent schema migrations don't exhaust Postgres's shared lock table
(`out of shared memory`, SQLSTATE 53200, with the default max_locks_per_transaction=64).

Examples:
  python3 scripts/persona-sim/suite.py --tag baseline
  python3 scripts/persona-sim/suite.py --tag cand --compare .sim-runs/<baseline>/scorecard.json
  python3 scripts/persona-sim/suite.py --persona 03-priya-subramanian --persona 07-elena-rostova --tag retry
  python3 scripts/persona-sim/suite.py --collect-only .sim-runs/<a>/01-maya-lin .sim-runs/<b>/02-devon-brooks --tag merged
"""

from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
RUNS_DIR = ROOT / ".sim-runs"
sys.path.insert(0, str(HERE))

import grader  # noqa: E402
from llm_client import LLMClient, load_dotenv, resolve_dotenv_path  # noqa: E402
from runner import select_personas  # noqa: E402

_start_lock = threading.Lock()
_last_start = [0.0]
_abort = threading.Event()

# Provider failures that invalidate a persona run (every task/flush/dream call fails).
FATAL_PROVIDER_MARKERS = ("HTTP 402", "HTTP 401", "HTTP 403", "insufficient_quota", "budget exceeded")


def preflight_credits(min_usd: float) -> None:
    """Refuse to start when the OpenRouter account (inner/flush/dream models) can't cover a run."""
    import urllib.error
    import urllib.request

    env = load_dotenv(resolve_dotenv_path(ROOT))
    key = (env.get("OPENROUTER_API_KEY") or "").strip()
    if not key or min_usd <= 0:
        return
    try:
        req = urllib.request.Request("https://openrouter.ai/api/v1/credits", headers={"Authorization": f"Bearer {key}"})
        with urllib.request.urlopen(req, timeout=20) as resp:
            data = json.loads(resp.read()).get("data") or {}
        remaining = float(data.get("total_credits", 0)) - float(data.get("total_usage", 0))
    except Exception as exc:  # network hiccup: warn, don't block
        print(f"[suite] WARN credit preflight skipped: {exc}", flush=True)
        return
    print(f"[suite] OpenRouter credits remaining: ${remaining:.2f}", flush=True)
    if remaining < min_usd:
        raise SystemExit(f"[suite] ABORT: OpenRouter credits ${remaining:.2f} < --min-credits ${min_usd:.2f}; top up before running.")
    # /credits can't see key or workspace budgets (those return 403 on use), so make one tiny live call.
    model = (env.get("NORI_TASK_MODEL") or "openai/gpt-6.1-terra").strip()
    probe = json.dumps({"model": model, "messages": [{"role": "user", "content": "ok"}], "max_tokens": 1}).encode()
    req = urllib.request.Request("https://openrouter.ai/api/v1/chat/completions", data=probe,
                                 headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=30):
            pass
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        try:
            msg = json.loads(body)["error"]["message"]
        except Exception:
            msg = body[:300]
        if exc.code in (401, 402, 403):
            raise SystemExit(f"[suite] ABORT: inner model {model!r} rejected the preflight call (HTTP {exc.code}): {msg}")
        print(f"[suite] WARN preflight call returned HTTP {exc.code}: {msg}", flush=True)
    except Exception as exc:
        print(f"[suite] WARN preflight call skipped: {exc}", flush=True)


def provider_fatal(bundle_or_log: Path) -> str | None:
    log = bundle_or_log / "nori-sim.log" if bundle_or_log.is_dir() else bundle_or_log
    if not log.is_file():
        return None
    text = log.read_text(encoding="utf-8", errors="replace")
    for marker in FATAL_PROVIDER_MARKERS:
        if marker in text:
            return marker
    return None


def _staggered_wait(stagger: float) -> None:
    with _start_lock:
        wait = _last_start[0] + stagger - time.monotonic()
        if wait > 0:
            time.sleep(wait)
        _last_start[0] = time.monotonic()


def _find_bundle(sub_tag: str, slug: str, since: float) -> Path | None:
    cands = [p / slug for p in RUNS_DIR.glob(f"*-{sub_tag}") if (p / slug / "summary.json").is_file() and p.stat().st_mtime >= since - 5]
    return max(cands, key=lambda p: p.stat().st_mtime) if cands else None


def run_persona(slug: str, tag: str, logs: Path, keep_db: bool, stagger: float, retries: int, extra: list[str]) -> tuple[str, Path | None, str]:
    for attempt in range(retries + 1):
        _staggered_wait(stagger)
        if _abort.is_set():
            print(f"[suite] SKIP  {slug}: suite aborted after a fatal provider error", flush=True)
            return slug, None, "skipped"
        sub_tag = f"{tag}-{slug}" + (f"-r{attempt}" if attempt else "")
        cmd = [sys.executable, str(HERE / "runner.py"), "--persona", slug, "--no-grade", "--tag", sub_tag, *extra]
        if keep_db:
            cmd.append("--keep-db")
        started = time.time()
        log = logs / (f"{slug}.log" if not attempt else f"{slug}.r{attempt}.log")
        print(f"[suite] start {slug} (attempt {attempt + 1}) -> {log.relative_to(ROOT)}", flush=True)
        with log.open("w", encoding="utf-8") as fh:
            rc = subprocess.run(cmd, cwd=ROOT, stdout=fh, stderr=subprocess.STDOUT).returncode
        bundle = _find_bundle(sub_tag, slug, started)
        fatal = provider_fatal(bundle) if bundle is not None else provider_fatal(log)
        if fatal:
            _abort.set()
            print(f"[suite] INVALID {slug}: provider error {fatal!r} in nori-sim.log; aborting remaining personas", flush=True)
            return slug, None, f"invalid:{fatal}"
        if rc == 0 and bundle is not None:
            print(f"[suite] done  {slug} ({time.time() - started:.0f}s)", flush=True)
            return slug, bundle, "ok"
        tail = log.read_text(encoding="utf-8", errors="replace").strip().splitlines()[-1:] or [""]
        print(f"[suite] FAIL  {slug} rc={rc}: {tail[0][:200]}", flush=True)
    return slug, None, "failed"


def rejudge_errors(run_dir: Path) -> list[tuple[str, str, str]]:
    """Re-run the judge for items that hit network errors (scored ERROR=0 by grader)."""
    sc_path = run_dir / "scorecard.json"
    sc = json.loads(sc_path.read_text(encoding="utf-8"))
    if not sc.get("judge_model"):
        return []
    judge = LLMClient.from_env("judge", load_dotenv(resolve_dotenv_path(ROOT)))
    redone: list[tuple[str, str, str]] = []
    for p in sc["personas"]:
        pdir = run_dir / p["slug"]
        probes = {pr["probe"]["id"]: pr for pr in json.loads((pdir / "probes.json").read_text(encoding="utf-8"))}
        summary = json.loads((pdir / "summary.json").read_text(encoding="utf-8"))
        snaps = sorted((pdir / "snapshots").glob("*.json"))
        final_snap = json.loads(snaps[-1].read_text(encoding="utf-8")) if snaps else {}
        for i, item in enumerate(p["tier_b_items"]):
            if item["verdict"] != "ERROR":
                continue
            for attempt in range(4):
                try:
                    if item["category"] == "end_of_month_audit":
                        new = grader._judge_eom_audit(judge, p["slug"], summary, final_snap)
                    else:
                        new = grader._judge_probe(judge, p["slug"], probes[item["id"]])
                    p["tier_b_items"][i] = new
                    redone.append((p["slug"], item["id"], new["verdict"]))
                    break
                except Exception:
                    time.sleep(5 * (attempt + 1))
        items = p["tier_b_items"]
        p["tier_b_score"] = round(sum(x["score"] for x in items) / len(items), 3) if items else None
    scores = [p["tier_b_score"] for p in sc["personas"] if p["tier_b_score"] is not None]
    sc["overall_semantic_score"] = round(sum(scores) / len(scores), 3) if scores else None
    if redone:
        sc["regraded_judge_errors"] = sc.get("regraded_judge_errors", []) + [{"slug": s, "id": i, "verdict": v} for s, i, v in redone]
        if sc.get("baseline_diff"):
            # keep the diff consistent with the re-judged scores
            by = {p["slug"]: p for p in sc["personas"]}
            for d in sc["baseline_diff"]["personas"]:
                d["semantic_after"] = by[d["slug"]]["tier_b_score"]
                if d["semantic_before"] is not None and d["semantic_after"] is not None:
                    d["delta"] = round(d["semantic_after"] - d["semantic_before"], 3)
        sc_path.write_text(json.dumps(sc, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        (run_dir / "scorecard.md").write_text(grader._render_markdown(sc), encoding="utf-8")
    return redone


def main() -> int:
    ap = argparse.ArgumentParser(description="Isolated persona VST suite: run, collect, grade, compare.")
    ap.add_argument("--preset", choices=["smoke", "memory-pack", "full"], default=None)
    ap.add_argument("--persona", action="append", default=[], help="Persona slug (repeatable). Overrides --preset.")
    ap.add_argument("--parallel", type=int, default=4, help="Personas running at once (default 4)")
    ap.add_argument("--stagger", type=float, default=45.0, help="Seconds between persona starts (default 45)")
    ap.add_argument("--retries", type=int, default=1, help="Re-runs for a persona whose runner fails (default 1)")
    ap.add_argument("--tag", default="suite")
    ap.add_argument("--compare", type=Path, help="Baseline scorecard.json to diff against")
    ap.add_argument("--keep-db", action="store_true", help="Keep each persona's ephemeral DB for inspection")
    ap.add_argument("--no-grade", action="store_true")
    ap.add_argument("--min-credits", type=float, default=15.0,
                    help="Abort before starting if OpenRouter credits are below this (USD; 0 disables). Default 15")
    ap.add_argument("--collect-only", nargs="+", type=Path, metavar="PERSONA_DIR",
                    help="Skip running; collect existing persona bundles into a new run dir and grade")
    args, extra = ap.parse_known_args()

    stamp = time.strftime("%Y%m%d-%H%M%S", time.localtime())
    run_root = RUNS_DIR / f"{stamp}-{args.tag}"
    logs = run_root / "_logs"
    logs.mkdir(parents=True, exist_ok=True)

    results: list[tuple[str, Path | None, str]] = []
    if args.collect_only:
        results = [(p.resolve().name, p.resolve(), "collected") for p in args.collect_only]
    else:
        preflight_credits(args.min_credits)
        slugs = args.persona or select_personas(args.preset or "full", None)
        print(f"[suite] {len(slugs)} personas, parallel={args.parallel}, stagger={args.stagger}s -> {run_root.relative_to(ROOT)}", flush=True)
        with ThreadPoolExecutor(max_workers=max(1, args.parallel)) as ex:
            futs = [ex.submit(run_persona, s, args.tag, logs, args.keep_db, args.stagger, args.retries, extra) for s in slugs]
            results = [f.result() for f in futs]

    manifest = []
    for slug, bundle, status in results:
        if bundle is not None and (fatal := provider_fatal(bundle)):
            print(f"[suite] EXCLUDE {slug}: provider error {fatal!r} in {bundle}", flush=True)
            bundle, status = None, f"invalid:{fatal}"
        if bundle is not None:
            shutil.copytree(bundle, run_root / slug, dirs_exist_ok=True)
        manifest.append({"slug": slug, "status": status, "source": str(bundle.relative_to(ROOT)) if bundle else None})
    (run_root / "suite_manifest.json").write_text(json.dumps({"personas": manifest}, indent=2) + "\n", encoding="utf-8")

    failed = [m["slug"] for m in manifest if m["source"] is None]
    if failed:
        print(f"[suite] personas with no bundle: {', '.join(failed)}", flush=True)
    if args.no_grade or len(failed) == len(manifest):
        print(f"[suite] run dir: {run_root}")
        return 1 if failed else 0

    sc = grader.grade_run(run_root, compare_path=args.compare)
    redone = rejudge_errors(run_root)
    if redone:
        print(f"[suite] re-judged {len(redone)} judge-error items", flush=True)
    print((run_root / "scorecard.md").read_text(encoding="utf-8"))
    print(f"[suite] scorecard: {run_root / 'scorecard.json'}")
    return 0 if sc["all_tier_a_passed"] and not failed else 1


if __name__ == "__main__":
    raise SystemExit(main())
