#!/usr/bin/env python3
"""Run a persona VST suite with one isolated runner process (and one ephemeral DB) per persona,
then grade all bundles together and optionally diff against a baseline scorecard.

Standalone runner processes also allocate individual databases for isolated personas.
The suite owns retries, aggregate grading, resource limits and baseline comparison.

Starts are staggered so concurrent schema migrations don't exhaust Postgres's shared lock table
(`out of shared memory`, SQLSTATE 53200, with the default max_locks_per_transaction=64).

Examples:
  python3 scripts/persona-sim/suite.py --tag baseline
  python3 scripts/persona-sim/suite.py --area memory --tag mem-check
  python3 scripts/persona-sim/suite.py --tag cand --compare .sim-runs/<baseline>/scorecard.json
  python3 scripts/persona-sim/suite.py --persona 03-priya-subramanian --persona 07-elena-rostova --tag retry
  python3 scripts/persona-sim/suite.py --collect-only .sim-runs/<a>/01-maya-lin .sim-runs/<b>/02-devon-brooks --tag merged
"""

from __future__ import annotations

import argparse
import json
import os
import secrets
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from contextlib import ExitStack
from pathlib import Path
from typing import Any

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
CORE = ROOT / "core"
RUNS_DIR = ROOT / ".sim-runs"
sys.path.insert(0, str(HERE))

import grader  # noqa: E402
from llm_client import (  # noqa: E402
    LLMClient,
    load_dotenv,
    resolve_dotenv_path,
    strip_raw_provider_keys,
)
from runner import resolve_max_days, select_personas  # noqa: E402

_start_lock = threading.Lock()
_last_start = [0.0]
_abort = threading.Event()
_budget_abort = threading.Event()
_cost_lock = threading.Lock()
_active_procs_lock = threading.Lock()
_active_procs: set[subprocess.Popen[Any]] = set()

FATAL_PROVIDER_MARKERS = (
    "HTTP 402",
    "HTTP 401",
    "HTTP 403",
    "insufficient_quota",
    "budget exceeded",
    "local_budget_exceeded",
)

AREA_PERSONAS: dict[str, list[str]] = {
    "smoke": ["01-maya-lin"],
    "core": [
        "02-devon-brooks",
        "03-priya-subramanian",
        "04-carlos-mendoza",
        "08-tariq-al-mansoor",
        "09-sora-takahashi",
    ],
    "memory": ["01-maya-lin", "03-priya-subramanian", "05-hannah-okafor", "09-sora-takahashi"],
    "permissions": ["02-devon-brooks", "04-carlos-mendoza", "08-tariq-al-mansoor"],
    "approvals": ["02-devon-brooks", "04-carlos-mendoza", "08-tariq-al-mansoor"],
    "uploads": ["01-maya-lin", "03-priya-subramanian", "07-elena-rostova", "10-jordan-reyes"],
    "automations": ["02-devon-brooks", "06-marcus-vance", "08-tariq-al-mansoor", "09-sora-takahashi", "10-jordan-reyes"],
    "goals": ["04-carlos-mendoza", "09-sora-takahashi"],
    "library": ["01-maya-lin", "03-priya-subramanian", "10-jordan-reyes"],
    "security": ["03-priya-subramanian", "06-marcus-vance", "10-jordan-reyes"],
    "full": [
        "01-maya-lin",
        "02-devon-brooks",
        "03-priya-subramanian",
        "04-carlos-mendoza",
        "05-hannah-okafor",
        "06-marcus-vance",
        "07-elena-rostova",
        "08-tariq-al-mansoor",
        "09-sora-takahashi",
        "10-jordan-reyes",
    ],
}


def select_area_personas(areas: list[str]) -> list[str]:
    """Return sorted unique persona slugs covering the requested feature areas."""
    chosen: set[str] = set()
    for area in areas:
        if area not in AREA_PERSONAS:
            raise ValueError(f"Unknown area {area!r}; available: {', '.join(sorted(AREA_PERSONAS))}")
        chosen.update(AREA_PERSONAS[area])
    return sorted(chosen)


def _probe_chat_endpoint(
    url: str,
    api_key: str,
    model: str,
    use_completion_tokens: bool = False,
) -> tuple[int, str]:
    payload: dict[str, Any] = {
        "model": model,
        "messages": [{"role": "user", "content": "ok"}],
    }
    if use_completion_tokens:
        payload["max_completion_tokens"] = 16
    else:
        payload["max_tokens"] = 16
    req = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, ""
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        try:
            msg = json.loads(body)["error"]["message"]
        except Exception:
            msg = body[:300]
        return exc.code, msg


def preflight_credits(
    min_usd: float,
    extra: list[str] | None = None,
    env: dict[str, str] | None = None,
) -> None:
    """Validate configured model providers (OpenAI direct, OpenRouter, or gateway) before running."""
    if extra and "--stub-models" in extra:
        print("[suite] --stub-models enabled; skipping live provider preflight.", flush=True)
        return

    if env is None:
        env = {**load_dotenv(resolve_dotenv_path(ROOT)), **os.environ}

    provider = (env.get("NORI_MODEL_PROVIDER") or "").strip().lower()
    if provider == "gateway":
        gw_url = (env.get("NORI_MODEL_GATEWAY_URL") or "").strip()
        gw_key = (env.get("NORI_MODEL_GATEWAY_API_KEY") or "").strip()
        outer_gw = (env.get("NORI_AGENT_MODEL") or "").strip()
        inner_gw = (env.get("MODEL_NAME") or "").strip()
        missing = [
            name for name, val in (
                ("NORI_MODEL_GATEWAY_URL", gw_url),
                ("NORI_MODEL_GATEWAY_API_KEY", gw_key),
                ("NORI_AGENT_MODEL", outer_gw),
                ("MODEL_NAME", inner_gw),
            ) if not val
        ]
        if missing:
            raise SystemExit(
                f"[suite] ABORT: NORI_MODEL_PROVIDER=gateway requires {', '.join(missing)}"
            )
        print(f"[suite] gateway mode configured ({gw_url}; outer={outer_gw}, inner={inner_gw})", flush=True)
        return

    outer_model = (env.get("NORI_AGENT_MODEL") or "gpt-6.1-sol").strip()
    inner_model = (env.get("MODEL_NAME") or "openai/gpt-6.1-terra").strip()
    uses_openai_inner = inner_model == "gpt-6.1-terra"

    openai_key = (env.get("OPENAI_API_KEY") or "").strip()
    if not openai_key:
        raise SystemExit(
            f"[suite] ABORT: OPENAI_API_KEY is required for outer model {outer_model!r} (and/or direct inner model)."
        )
    try:
        status, msg = _probe_chat_endpoint(
            "https://api.openai.com/v1/chat/completions",
            openai_key,
            outer_model,
            use_completion_tokens=outer_model.startswith(("gpt-6", "gpt-5", "o1", "o3", "o4")),
        )
        if status in (401, 402, 403) or (status == 429 and "insufficient_quota" in msg.lower()):
            raise SystemExit(
                f"[suite] ABORT: OpenAI outer model {outer_model!r} rejected preflight (HTTP {status}): {msg}"
            )
        if status >= 400:
            print(f"[suite] WARN OpenAI preflight call returned HTTP {status}: {msg}", flush=True)
        else:
            print(f"[suite] OpenAI preflight OK ({outer_model})", flush=True)
    except SystemExit:
        raise
    except Exception as exc:
        print(f"[suite] WARN OpenAI preflight call skipped: {exc}", flush=True)

    if uses_openai_inner:
        return

    or_key = (env.get("OPENROUTER_API_KEY") or "").strip()
    if not or_key:
        raise SystemExit(
            f"[suite] ABORT: OPENROUTER_API_KEY is required for OpenRouter inner model {inner_model!r} "
            "(or set MODEL_NAME=gpt-6.1-terra for OpenAI direct)."
        )

    if min_usd > 0:
        try:
            req = urllib.request.Request(
                "https://openrouter.ai/api/v1/credits",
                headers={"Authorization": f"Bearer {or_key}"},
            )
            with urllib.request.urlopen(req, timeout=20) as resp:
                data = json.loads(resp.read()).get("data") or {}
            remaining = float(data.get("total_credits", 0)) - float(data.get("total_usage", 0))
            print(f"[suite] OpenRouter credits remaining: ${remaining:.2f}", flush=True)
            if remaining < min_usd:
                raise SystemExit(
                    f"[suite] ABORT: OpenRouter credits ${remaining:.2f} < --min-credits ${min_usd:.2f}; top up before running."
                )
        except SystemExit:
            raise
        except Exception as exc:
            print(f"[suite] WARN OpenRouter credit preflight skipped: {exc}", flush=True)

    try:
        status, msg = _probe_chat_endpoint(
            "https://openrouter.ai/api/v1/chat/completions",
            or_key,
            inner_model,
            use_completion_tokens=False,
        )
        if status in (401, 402, 403) or (status == 429 and "insufficient_quota" in msg.lower()):
            raise SystemExit(
                f"[suite] ABORT: OpenRouter inner model {inner_model!r} rejected preflight (HTTP {status}): {msg}"
            )
        if status >= 400:
            print(f"[suite] WARN OpenRouter preflight call returned HTTP {status}: {msg}", flush=True)
        else:
            print(f"[suite] OpenRouter preflight OK ({inner_model})", flush=True)
    except SystemExit:
        raise
    except Exception as exc:
        print(f"[suite] WARN OpenRouter preflight call skipped: {exc}", flush=True)


def provider_fatal(bundle_or_log: Path) -> str | None:
    log = bundle_or_log / "nori-sim.log" if bundle_or_log.is_dir() else bundle_or_log
    if not log.is_file():
        return None
    text = log.read_text(encoding="utf-8", errors="replace")
    for marker in FATAL_PROVIDER_MARKERS:
        if marker in text:
            return marker
    return None


def _empty_usage() -> dict[str, Any]:
    return {
        "input_tokens": 0,
        "output_tokens": 0,
        "cached_tokens": 0,
        "cache_write_tokens": 0,
        "total_cost_usd": 0.0,
        "by_model": {},
    }


def _aggregate_usage_records(records: list[dict[str, Any]]) -> dict[str, Any]:
    out = _empty_usage()
    by_model: dict[str, dict[str, Any]] = {}
    for rec in records:
        if not isinstance(rec, dict):
            continue
        model = str(rec.get("model") or "unknown")
        provider = str(rec.get("provider") or "")
        inp = int(rec.get("input_tokens") or 0)
        out_tok = int(rec.get("output_tokens") or 0)
        cached = int(rec.get("cached_tokens") or 0)
        cache_write = int(rec.get("cache_write_tokens") or 0)
        cost = float(rec.get("cost_usd") or 0.0)

        out["input_tokens"] += inp
        out["output_tokens"] += out_tok
        out["cached_tokens"] += cached
        out["cache_write_tokens"] += cache_write
        out["total_cost_usd"] += cost

        entry = by_model.setdefault(model, {
            "provider": provider,
            "calls": 0,
            "input_tokens": 0,
            "output_tokens": 0,
            "cached_tokens": 0,
            "cache_write_tokens": 0,
            "cost_usd": 0.0,
        })
        if provider and not entry["provider"]:
            entry["provider"] = provider
        entry["calls"] += 1
        entry["input_tokens"] += inp
        entry["output_tokens"] += out_tok
        entry["cached_tokens"] += cached
        entry["cache_write_tokens"] += cache_write
        entry["cost_usd"] += cost

    out["total_cost_usd"] = round(out["total_cost_usd"], 6)
    for entry in by_model.values():
        entry["cost_usd"] = round(entry["cost_usd"], 6)
    out["by_model"] = by_model
    return out


def extract_persona_usage(summary: dict[str, Any], persona_dir: Path | None = None) -> dict[str, Any]:
    """Normalize Contract 3 summary['model_usage'] dict or raw list[state.Usage] fallback."""
    raw = summary.get("model_usage")
    if not raw and persona_dir is not None:
        snaps_dir = persona_dir / "snapshots"
        eom = snaps_dir / "z_final_eom.json"
        snap_path = eom if eom.is_file() else None
        if snap_path is None and snaps_dir.is_dir():
            snaps = sorted(snaps_dir.glob("*.json"))
            snap_path = snaps[-1] if snaps else None
        if snap_path is not None:
            try:
                raw = json.loads(snap_path.read_text(encoding="utf-8")).get("model_usage")
            except Exception:
                raw = None

    if isinstance(raw, list):
        return _aggregate_usage_records(raw)

    if isinstance(raw, dict):
        if isinstance(raw.get("records"), list) and not raw.get("by_model") and raw.get("total_cost_usd") is None:
            return _aggregate_usage_records(raw["records"])
        out = _empty_usage()
        out["input_tokens"] = int(raw.get("total_input_tokens", raw.get("input_tokens", 0)) or 0)
        out["output_tokens"] = int(raw.get("total_output_tokens", raw.get("output_tokens", 0)) or 0)
        out["cached_tokens"] = int(raw.get("total_cached_tokens", raw.get("cached_tokens", 0)) or 0)
        out["cache_write_tokens"] = int(raw.get("total_cache_write_tokens", raw.get("cache_write_tokens", 0)) or 0)
        out["total_cost_usd"] = round(float(raw.get("total_cost_usd", raw.get("cost_usd", 0.0)) or 0.0), 6)
        by_model_raw = raw.get("by_model")
        if isinstance(by_model_raw, dict) and by_model_raw:
            norm_models: dict[str, dict[str, Any]] = {}
            for mname, mdata in by_model_raw.items():
                if not isinstance(mdata, dict):
                    continue
                norm_models[str(mname)] = {
                    "provider": str(mdata.get("provider") or ""),
                    "calls": int(mdata.get("calls", 0) or 0),
                    "input_tokens": int(mdata.get("input_tokens", mdata.get("total_input_tokens", 0)) or 0),
                    "output_tokens": int(mdata.get("output_tokens", mdata.get("total_output_tokens", 0)) or 0),
                    "cached_tokens": int(mdata.get("cached_tokens", mdata.get("total_cached_tokens", 0)) or 0),
                    "cache_write_tokens": int(mdata.get("cache_write_tokens", mdata.get("total_cache_write_tokens", 0)) or 0),
                    "cost_usd": round(float(mdata.get("cost_usd", mdata.get("total_cost_usd", 0.0)) or 0.0), 6),
                }
            out["by_model"] = norm_models
        elif isinstance(raw.get("records"), list):
            rec_agg = _aggregate_usage_records(raw["records"])
            out["by_model"] = rec_agg["by_model"]
            if out["input_tokens"] == 0:
                out["input_tokens"] = rec_agg["input_tokens"]
                out["output_tokens"] = rec_agg["output_tokens"]
                out["cached_tokens"] = rec_agg["cached_tokens"]
                out["cache_write_tokens"] = rec_agg["cache_write_tokens"]
                out["total_cost_usd"] = rec_agg["total_cost_usd"]
        return out

    return _empty_usage()


def aggregate_suite_usage(persona_dirs: list[tuple[str, Path]]) -> dict[str, Any]:
    """Aggregate normalized model_usage across all completed persona bundles."""
    total = _empty_usage()
    per_persona: dict[str, dict[str, Any]] = {}
    by_model: dict[str, dict[str, Any]] = {}

    for slug, pdir in persona_dirs:
        sum_path = pdir / "summary.json"
        summary = json.loads(sum_path.read_text(encoding="utf-8")) if sum_path.is_file() else {}
        usage = extract_persona_usage(summary, pdir)
        per_persona[slug] = usage

        total["input_tokens"] += usage["input_tokens"]
        total["output_tokens"] += usage["output_tokens"]
        total["cached_tokens"] += usage["cached_tokens"]
        total["cache_write_tokens"] += usage["cache_write_tokens"]
        total["total_cost_usd"] += usage["total_cost_usd"]

        for mname, mdata in usage.get("by_model", {}).items():
            entry = by_model.setdefault(mname, {
                "provider": mdata.get("provider", ""),
                "calls": 0,
                "input_tokens": 0,
                "output_tokens": 0,
                "cached_tokens": 0,
                "cache_write_tokens": 0,
                "cost_usd": 0.0,
            })
            if mdata.get("provider") and not entry["provider"]:
                entry["provider"] = mdata["provider"]
            entry["calls"] += mdata.get("calls", 0)
            entry["input_tokens"] += mdata.get("input_tokens", 0)
            entry["output_tokens"] += mdata.get("output_tokens", 0)
            entry["cached_tokens"] += mdata.get("cached_tokens", 0)
            entry["cache_write_tokens"] += mdata.get("cache_write_tokens", 0)
            entry["cost_usd"] += mdata.get("cost_usd", 0.0)

    total["total_cost_usd"] = round(total["total_cost_usd"], 6)
    for entry in by_model.values():
        entry["cost_usd"] = round(entry["cost_usd"], 6)
    hit_rate = (
        round(total["cached_tokens"] / total["input_tokens"], 4)
        if total["input_tokens"] > 0
        else 0.0
    )
    return {
        "total_input_tokens": total["input_tokens"],
        "total_output_tokens": total["output_tokens"],
        "total_cached_tokens": total["cached_tokens"],
        "total_cache_write_tokens": total["cache_write_tokens"],
        "total_cost_usd": total["total_cost_usd"],
        "cache_hit_rate": hit_rate,
        "by_model": by_model,
        "personas": per_persona,
    }


def format_cost_table(cost_summary: dict[str, Any]) -> str:
    """Format a human-readable cost and cache-hit summary table."""
    lines = [
        "",
        "Model Usage & Cost Summary",
        f"{'Persona':<24} {'Input':>10} {'Cached (%)':>14} {'CacheWrite':>11} {'Output':>9} {'Cost USD':>10}  By Model",
        "-" * 96,
    ]
    for slug, u in sorted((cost_summary.get("personas") or {}).items()):
        inp = u["input_tokens"]
        cached = u["cached_tokens"]
        pct = (cached / inp * 100.0) if inp > 0 else 0.0
        models_str = ", ".join(
            f"{m}:${d['cost_usd']:.3f}" for m, d in sorted((u.get("by_model") or {}).items())
        ) or "-"
        lines.append(
            f"{slug:<24} {inp:>10,} {cached:>7,} ({pct:>4.1f}%) {u['cache_write_tokens']:>11,} "
            f"{u['output_tokens']:>9,} ${u['total_cost_usd']:>9.4f}  {models_str}"
        )
    lines.append("-" * 96)
    t_inp = cost_summary["total_input_tokens"]
    t_cached = cost_summary["total_cached_tokens"]
    t_pct = (t_cached / t_inp * 100.0) if t_inp > 0 else 0.0
    t_models = ", ".join(
        f"{m}:${d['cost_usd']:.3f}" for m, d in sorted((cost_summary.get("by_model") or {}).items())
    ) or "-"
    lines.append(
        f"{'TOTAL':<24} {t_inp:>10,} {t_cached:>7,} ({t_pct:>4.1f}%) "
        f"{cost_summary['total_cache_write_tokens']:>11,} {cost_summary['total_output_tokens']:>9,} "
        f"${cost_summary['total_cost_usd']:>9.4f}  {t_models}"
    )
    return "\n".join(lines)


def _staggered_wait(stagger: float) -> None:
    with _start_lock:
        wait = _last_start[0] + stagger - time.monotonic()
        if wait > 0:
            time.sleep(wait)
        _last_start[0] = time.monotonic()


def _find_bundle(sub_tag: str, slug: str, since: float) -> Path | None:
    cands = [p / slug for p in RUNS_DIR.glob(f"*-{sub_tag}") if (p / slug / "summary.json").is_file() and p.stat().st_mtime >= since - 5]
    return max(cands, key=lambda p: p.stat().st_mtime) if cands else None


def _terminate_active_procs() -> None:
    """Immediately terminate all running persona subprocesses when the kill switch trips."""
    with _active_procs_lock:
        procs = list(_active_procs)
    for proc in procs:
        if proc.poll() is None:
            try:
                proc.terminate()
            except OSError:
                pass


def resolve_suite_env(
    env_file: Path | None = None,
    allow_dev_key: bool = False,
    extra: list[str] | None = None,
    root: Path = ROOT,
    environ: dict[str, str] | None = None,
) -> dict[str, str]:
    """Resolve provider credentials for suite.py while refusing default local dev keys unless overridden.

    Resolution order for isolated test keys:
      1. Explicit --env-file (if provided) or <root>/.env.vst (if present)
      2. Dedicated *_TEST_API_KEY variables (OPENAI_TEST_API_KEY, OPENROUTER_TEST_API_KEY, ANTHROPIC_TEST_API_KEY)
      3. External NORI_MODEL_PROVIDER=gateway (if already configured)
      4. Default local .env dev keys ONLY when allow_dev_key=True
    """
    proc_env = dict(os.environ if environ is None else environ)
    default_dev_env_path = resolve_dotenv_path(root)
    dev_dotenv = load_dotenv(default_dev_env_path)

    if extra and "--stub-models" in extra:
        return {**dev_dotenv, **proc_env}

    if (proc_env.get("NORI_MODEL_PROVIDER") or "").strip().lower() == "gateway":
        return {**strip_raw_provider_keys(dev_dotenv), **proc_env}

    vst_path = env_file if env_file is not None else (root / ".env.vst")
    using_vst_file = vst_path.is_file() and vst_path.resolve() != default_dev_env_path.resolve()
    vst_dotenv = load_dotenv(vst_path) if using_vst_file else {}

    base_dev = dev_dotenv if allow_dev_key else strip_raw_provider_keys(dev_dotenv)
    merged = {**base_dev, **vst_dotenv, **proc_env}

    test_keys: dict[str, str] = {}
    for prefix in ("OPENAI", "OPENROUTER", "ANTHROPIC"):
        test_val = (merged.get(f"{prefix}_TEST_API_KEY") or "").strip()
        vst_base = (vst_dotenv.get(f"{prefix}_API_KEY") or "").strip()
        chosen = test_val or vst_base
        if chosen:
            test_keys[f"{prefix}_API_KEY"] = chosen

    if test_keys and not allow_dev_key:
        for base_var, val in test_keys.items():
            dev_val = (dev_dotenv.get(base_var) or "").strip()
            dev_explicit = (dev_dotenv.get(base_var.replace("_API_KEY", "_DEV_API_KEY")) or "").strip()
            prod_explicit = (dev_dotenv.get(base_var.replace("_API_KEY", "_PROD_API_KEY")) or "").strip()
            if val and val in (dev_val, dev_explicit, prod_explicit):
                raise SystemExit(
                    f"[suite] ABORT: test key for {base_var} matches the default local dev key in {default_dev_env_path}. "
                    "Use a dedicated test project key in .env.vst / *_TEST_API_KEY, or pass --allow-dev-key explicitly."
                )

    if test_keys:
        out = strip_raw_provider_keys(merged) if not allow_dev_key else dict(merged)
        out.update(test_keys)
        return out

    if not allow_dev_key:
        raise SystemExit(
            "[suite] ABORT: refusing to run persona-sim against the default local dev key. "
            "Provide isolated test keys via .env.vst or *_TEST_API_KEY (OPENAI_TEST_API_KEY / OPENROUTER_TEST_API_KEY / "
            "ANTHROPIC_TEST_API_KEY), or pass --allow-dev-key to override explicitly."
        )
    return merged


class LocalGateway:
    """Manages a local loopback `nori-gateway` process enforcing real-time --max-usd / --max-calls."""

    def __init__(
        self,
        upstream_env: dict[str, str],
        max_usd: float | None = None,
        max_calls: int | None = None,
        log_path: Path | None = None,
    ) -> None:
        self.upstream_env = upstream_env
        self.max_usd = max_usd or 0.0
        self.max_calls = max_calls or 0
        self.log_path = log_path
        self.token = secrets.token_urlsafe(32)
        self.url = ""
        self.addr = ""
        self._proc: subprocess.Popen[Any] | None = None
        self._tmp: tempfile.TemporaryDirectory[str] | None = None
        self._log_fh: Any = None

    def __enter__(self) -> "LocalGateway":
        self._tmp = tempfile.TemporaryDirectory(prefix="nori-gateway-")
        tmp_dir = Path(self._tmp.name)
        bin_path = tmp_dir / "nori-gateway"
        ready_file = tmp_dir / "ready.json"

        build = subprocess.run(
            ["go", "build", "-o", str(bin_path), "./cmd/nori-gateway"],
            cwd=CORE,
            text=True,
            capture_output=True,
            timeout=300,
        )
        if build.returncode != 0:
            raise RuntimeError(f"failed building cmd/nori-gateway: {(build.stderr or build.stdout)[:500]}")

        gw_env = {
            "PATH": os.environ.get("PATH", ""),
            "HOME": os.environ.get("HOME", ""),
            "NORI_GATEWAY_TOKEN": self.token,
        }
        for key in (
            "OPENAI_API_KEY",
            "OPENAI_TEST_API_KEY",
            "OPENROUTER_API_KEY",
            "OPENROUTER_TEST_API_KEY",
            "ANTHROPIC_API_KEY",
            "ANTHROPIC_TEST_API_KEY",
        ):
            val = (self.upstream_env.get(key) or "").strip()
            if val:
                gw_env[key] = val

        cmd = [
            str(bin_path),
            "--listen", "127.0.0.1:0",
            "--ready-file", str(ready_file),
        ]
        if self.max_usd > 0:
            cmd.extend(["--max-usd", f"{self.max_usd:.6f}"])
        if self.max_calls > 0:
            cmd.extend(["--max-calls", str(self.max_calls)])

        if self.log_path is not None:
            self._log_fh = self.log_path.open("w", encoding="utf-8")
            stdout_dst = self._log_fh
        else:
            stdout_dst = subprocess.DEVNULL

        self._proc = subprocess.Popen(
            cmd,
            cwd=tmp_dir,
            env=gw_env,
            stdout=stdout_dst,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )

        deadline = time.monotonic() + 20.0
        while time.monotonic() < deadline:
            if self._proc.poll() is not None:
                raise RuntimeError(f"nori-gateway exited early (rc={self._proc.returncode})")
            if ready_file.is_file():
                info = json.loads(ready_file.read_text(encoding="utf-8"))
                self.addr = str(info["addr"])
                self.url = str(info["url"])
                break
            time.sleep(0.05)
        if not self.url:
            self.__exit__(None, None, None)
            raise RuntimeError("nori-gateway did not become ready in time")

        print(
            f"[suite] local gateway ready at {self.url} "
            f"(max_usd={self.max_usd or 'unlimited'}, max_calls={self.max_calls or 'unlimited'})",
            flush=True,
        )
        return self

    def __exit__(self, exc_type: Any, exc: Any, tb: Any) -> None:
        if self._proc is not None and self._proc.poll() is None:
            self._proc.terminate()
            try:
                self._proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self._proc.kill()
                self._proc.wait()
        if self._log_fh is not None:
            try:
                self._log_fh.close()
            except Exception:
                pass
            self._log_fh = None
        if self._tmp is not None:
            self._tmp.cleanup()
            self._tmp = None

    def stats(self) -> dict[str, Any]:
        if not self.addr:
            return {}
        req = urllib.request.Request(
            f"http://{self.addr}/stats",
            headers={"Authorization": f"Bearer {self.token}"},
        )
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                return json.loads(resp.read().decode("utf-8"))
        except Exception:
            return {}

    def child_env(self, base_env: dict[str, str], outer_model: str, inner_model: str) -> dict[str, str]:
        """Build a sanitized child environment containing ONLY the short-lived gateway token."""
        env = strip_raw_provider_keys(base_env)
        for override in ("NORI_OUTER_PROVIDER", "NORI_INNER_PROVIDER", "NORI_MODEL_TEST_URL", "NORI_ENV_PROFILE"):
            env.pop(override, None)
        env.update({
            "NORI_MODEL_PROVIDER": "gateway",
            "NORI_MODEL_GATEWAY_URL": self.url,
            "NORI_MODEL_GATEWAY_API_KEY": self.token,
            "NORI_AGENT_MODEL": outer_model,
            "MODEL_NAME": inner_model,
            "SIM_GATEWAY_URL": self.url,
            "SIM_GATEWAY_TOKEN": self.token,
        })
        return env


def run_persona(
    slug: str,
    tag: str,
    logs: Path,
    keep_db: bool,
    stagger: float,
    retries: int,
    extra: list[str],
    max_usd: float | None = None,
    running_cost: list[float] | None = None,
    child_env: dict[str, str] | None = None,
    gateway: LocalGateway | None = None,
) -> tuple[str, Path | None, str]:
    for attempt in range(retries + 1):
        _staggered_wait(stagger)
        if _budget_abort.is_set() or (gateway is not None and gateway.stats().get("budget_exceeded")):
            _budget_abort.set()
            print(f"[suite] SKIP  {slug}: suite aborted after exceeding budget/call cap", flush=True)
            return slug, None, "budget_exceeded"
        if _abort.is_set():
            print(f"[suite] SKIP  {slug}: suite aborted after a fatal provider error", flush=True)
            return slug, None, "skipped"
        sub_tag = f"{tag}-{slug}" + (f"-r{attempt}" if attempt else "")
        cmd = [sys.executable, str(HERE / "runner.py"), "--persona", slug, "--no-grade", "--tag", sub_tag, *extra]
        if keep_db:
            cmd.append("--keep-db")
        started = time.time()
        log = logs / (f"{slug}.log" if not attempt else f"{slug}.r{attempt}.log")
        try:
            log_disp = log.relative_to(ROOT)
        except ValueError:
            log_disp = log
        print(f"[suite] start {slug} (attempt {attempt + 1}) -> {log_disp}", flush=True)
        with log.open("w", encoding="utf-8") as fh:
            proc = subprocess.Popen(cmd, cwd=ROOT, env=child_env, stdout=fh, stderr=subprocess.STDOUT)
            with _active_procs_lock:
                _active_procs.add(proc)
            try:
                while proc.poll() is None:
                    if gateway is not None and gateway.stats().get("budget_exceeded"):
                        _budget_abort.set()
                        _terminate_active_procs()
                        break
                    time.sleep(0.5)
                rc = proc.wait()
            finally:
                with _active_procs_lock:
                    _active_procs.discard(proc)
        bundle = _find_bundle(sub_tag, slug, started)
        if gateway is not None and gateway.stats().get("budget_exceeded"):
            _budget_abort.set()
            _terminate_active_procs()
            print(f"[suite] KILLED {slug}: local gateway tripped real-time budget/call kill switch", flush=True)
            return slug, bundle, "budget_exceeded"
        fatal = provider_fatal(bundle) if bundle is not None else provider_fatal(log)
        if fatal:
            if fatal == "local_budget_exceeded":
                _budget_abort.set()
                _terminate_active_procs()
                print(f"[suite] KILLED {slug}: hit local_budget_exceeded; aborting all personas", flush=True)
                return slug, bundle, "budget_exceeded"
            _abort.set()
            print(f"[suite] INVALID {slug}: provider error {fatal!r} in nori-sim.log; aborting remaining personas", flush=True)
            return slug, None, f"invalid:{fatal}"
        if rc == 0 and bundle is not None:
            if max_usd is not None and running_cost is not None:
                try:
                    s_data = json.loads((bundle / "summary.json").read_text(encoding="utf-8"))
                except Exception:
                    s_data = {}
                p_cost = extract_persona_usage(s_data, bundle)["total_cost_usd"]
                with _cost_lock:
                    running_cost[0] += p_cost
                    if running_cost[0] > max_usd:
                        _budget_abort.set()
                        _terminate_active_procs()
                        print(
                            f"[suite] BUDGET {slug}: cumulative cost ${running_cost[0]:.4f} exceeded --max-usd ${max_usd:.2f}; "
                            "aborting queued personas",
                            flush=True,
                        )
            print(f"[suite] done  {slug} ({time.time() - started:.0f}s)", flush=True)
            return slug, bundle, "ok"
        tail = log.read_text(encoding="utf-8", errors="replace").strip().splitlines()[-1:] or [""]
        print(f"[suite] FAIL  {slug} rc={rc}: {tail[0][:200]}", flush=True)
    return slug, None, "failed"


def rejudge_errors(run_dir: Path) -> list[tuple[str, str, str]]:
    """Re-run the judge for items that hit network errors (verdict == ERROR)."""
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
        mcp_audit = grader._load_json_if_exists(pdir / "mcp_audit.json") or {}
        approval_log = grader._extract_approval_log(pdir, summary)
        snaps = sorted((pdir / "snapshots").glob("*.json")) if (pdir / "snapshots").is_dir() else []
        final_snap = json.loads(snaps[-1].read_text(encoding="utf-8")) if snaps else {}
        for i, item in enumerate(p["tier_b_items"]):
            if item["verdict"] != "ERROR":
                continue
            for attempt in range(4):
                try:
                    if item["category"] == "end_of_month_audit":
                        new = grader._judge_eom_audit(judge, p["slug"], summary, final_snap, mcp_audit, approval_log)
                    else:
                        new = grader._judge_probe(judge, p["slug"], probes[item["id"]], mcp_audit, approval_log)
                    p["tier_b_items"][i] = new
                    redone.append((p["slug"], item["id"], new["verdict"]))
                    break
                except Exception:
                    time.sleep(5 * (attempt + 1))
        items = p["tier_b_items"]
        p["tier_b_score"] = grader.compute_tier_b_score(items)
        scored_cnt, err_cnt, incl_cnt, tb_status = grader.tier_b_counts_and_status(items)
        p["tier_b_scored_count"] = scored_cnt
        p["tier_b_error_count"] = err_cnt
        p["tier_b_inconclusive_count"] = incl_cnt
        p["tier_b_status"] = tb_status
    scores = [p["tier_b_score"] for p in sc["personas"] if p["tier_b_score"] is not None]
    sc["overall_semantic_score"] = round(sum(scores) / len(scores), 3) if scores else None
    if redone:
        sc["regraded_judge_errors"] = sc.get("regraded_judge_errors", []) + [{"slug": s, "id": i, "verdict": v} for s, i, v in redone]
        if sc.get("baseline_diff"):
            by = {p["slug"]: p for p in sc["personas"]}
            for d in sc["baseline_diff"]["personas"]:
                if d["slug"] not in by:
                    continue
                d["semantic_after"] = by[d["slug"]]["tier_b_score"]
                if d["semantic_before"] is not None and d["semantic_after"] is not None:
                    d["delta"] = round(d["semantic_after"] - d["semantic_before"], 3)
                else:
                    d["delta"] = None
        sc_path.write_text(json.dumps(sc, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        (run_dir / "scorecard.md").write_text(grader._render_markdown(sc), encoding="utf-8")
    return redone


def main() -> int:
    ap = argparse.ArgumentParser(description="Isolated persona VST suite: run, collect, grade, compare.")
    ap.add_argument("--preset", choices=["smoke", "core", "memory-pack", "full"], default=None)
    ap.add_argument(
        "--area",
        action="append",
        choices=sorted(AREA_PERSONAS),
        default=[],
        help="Feature-area persona subset (repeatable; overrides --preset, overridden by --persona).",
    )
    ap.add_argument("--persona", action="append", default=[], help="Persona slug (repeatable). Overrides --area and --preset.")
    ap.add_argument("--max-days", type=int, default=None, help="Cap simulated days per persona (e.g. 7 for 1 week, 14 for 2 weeks)")
    ap.add_argument("--weeks", type=int, default=None, help="Simulated duration in weeks (1=7d, 2=14d, 3=21d, 4=full 30d month)")
    ap.add_argument("--parallel", type=int, default=4, help="Personas running at once (default 4)")
    ap.add_argument("--stagger", type=float, default=45.0, help="Seconds between persona starts (default 45)")
    ap.add_argument("--retries", type=int, default=1, help="Re-runs for a persona whose runner fails (default 1)")
    ap.add_argument("--tag", default="suite")
    ap.add_argument("--compare", type=Path, help="Baseline scorecard.json to diff against")
    ap.add_argument("--keep-db", action="store_true", help="Keep each persona's ephemeral DB for inspection")
    ap.add_argument("--no-grade", action="store_true")
    ap.add_argument("--min-credits", type=float, default=15.0,
                    help="Abort before starting if OpenRouter credits are below this (USD; 0 disables). Default 15")
    ap.add_argument("--max-usd", type=float, default=None,
                    help="Real-time hard USD spend ceiling enforced across all personas + responder + judge.")
    ap.add_argument("--max-calls", type=int, default=None,
                    help="Real-time hard total LLM call ceiling enforced across all personas + responder + judge.")
    ap.add_argument("--env-file", type=Path, default=None,
                    help="Isolated test env file (defaults to .env.vst if present).")
    ap.add_argument("--allow-dev-key", action="store_true",
                    help="Explicitly allow running against the default local .env dev key instead of isolated test keys.")
    ap.add_argument("--outer-model", default=None, help="Outer conversation model ID (overrides NORI_AGENT_MODEL, e.g. gpt-6.2-sol)")
    ap.add_argument("--outer-reasoning", choices=["low", "medium", "high"], default=None,
                    help="Outer conversation reasoning effort (overrides NORI_OUTER_REASONING_EFFORT)")
    ap.add_argument("--inner-model", default=None, help="Inner task/flush/dream model ID (overrides MODEL_NAME, e.g. gpt-6.1-terra)")
    ap.add_argument("--inner-reasoning", choices=["low", "medium", "high"], default=None,
                    help="Inner task loop default reasoning effort (overrides NORI_INNER_REASONING_EFFORT)")
    ap.add_argument("--collect-only", nargs="+", type=Path, metavar="PERSONA_DIR",
                    help="Skip running; collect existing persona bundles into a new run dir and grade")
    args, extra = ap.parse_known_args()

    effective_max_days = resolve_max_days(args.max_days, args.weeks)
    if effective_max_days is not None:
        extra = [*extra, "--max-days", str(effective_max_days)]

    model_overrides: dict[str, str] = {}
    if args.outer_model:
        os.environ["NORI_AGENT_MODEL"] = args.outer_model
        extra = [*extra, "--outer-model", args.outer_model]
        model_overrides["outer_model"] = args.outer_model
    if args.outer_reasoning:
        os.environ["NORI_OUTER_REASONING_EFFORT"] = args.outer_reasoning
        extra = [*extra, "--outer-reasoning", args.outer_reasoning]
        model_overrides["outer_reasoning"] = args.outer_reasoning
    if args.inner_model:
        os.environ["MODEL_NAME"] = args.inner_model
        extra = [*extra, "--inner-model", args.inner_model]
        model_overrides["inner_model"] = args.inner_model
    if args.inner_reasoning:
        os.environ["NORI_INNER_REASONING_EFFORT"] = args.inner_reasoning
        extra = [*extra, "--inner-reasoning", args.inner_reasoning]
        model_overrides["inner_reasoning"] = args.inner_reasoning

    _abort.clear()
    _budget_abort.clear()

    stamp = time.strftime("%Y%m%d-%H%M%S", time.localtime())
    run_root = RUNS_DIR / f"{stamp}-{args.tag}"
    logs = run_root / "_logs"
    logs.mkdir(parents=True, exist_ok=True)

    results: list[tuple[str, Path | None, str]] = []
    running_cost = [0.0]
    gateway_stats: dict[str, Any] = {}
    with ExitStack() as stack:
        gw: LocalGateway | None = None
        child_env: dict[str, str] | None = None
        if args.collect_only:
            results = [(p.resolve().name, p.resolve(), "collected") for p in args.collect_only]
        else:
            suite_env = resolve_suite_env(
                env_file=args.env_file,
                allow_dev_key=args.allow_dev_key,
                extra=extra,
            )
            stub_mode = "--stub-models" in extra
            external_gateway = (suite_env.get("NORI_MODEL_PROVIDER") or "").strip().lower() == "gateway"
            if not stub_mode and not external_gateway:
                outer_model = (suite_env.get("NORI_AGENT_MODEL") or "gpt-6.1-sol").strip()
                inner_model = (suite_env.get("MODEL_NAME") or "openai/gpt-6.1-terra").strip()
                gw = stack.enter_context(
                    LocalGateway(
                        upstream_env=suite_env,
                        max_usd=args.max_usd,
                        max_calls=args.max_calls,
                        log_path=logs / "nori-gateway.log",
                    )
                )
                child_env = gw.child_env(os.environ, outer_model, inner_model)
                os.environ["SIM_GATEWAY_URL"] = gw.url
                os.environ["SIM_GATEWAY_TOKEN"] = gw.token
                status, msg = _probe_chat_endpoint(
                    f"{gw.url}/chat/completions",
                    gw.token,
                    outer_model,
                    use_completion_tokens=outer_model.startswith(("gpt-6", "gpt-5", "o1", "o3", "o4")),
                )
                if status >= 400:
                    raise SystemExit(
                        f"[suite] ABORT: gateway preflight for {outer_model!r} failed (HTTP {status}): {msg}"
                    )
                print(f"[suite] gateway preflight OK ({outer_model})", flush=True)
            else:
                preflight_credits(args.min_credits, extra=extra, env=suite_env)

            if args.persona:
                slugs = args.persona
            elif args.area:
                slugs = select_area_personas(args.area)
            else:
                slugs = select_personas(args.preset or "full", None)
            print(f"[suite] {len(slugs)} personas, parallel={args.parallel}, stagger={args.stagger}s -> {run_root.relative_to(ROOT)}", flush=True)
            with ThreadPoolExecutor(max_workers=max(1, args.parallel)) as ex:
                futs = [
                    ex.submit(
                        run_persona, s, args.tag, logs, args.keep_db, args.stagger,
                        args.retries, extra, args.max_usd, running_cost, child_env, gw,
                    )
                    for s in slugs
                ]
                results = [f.result() for f in futs]

        manifest = []
        copied_bundles: list[tuple[str, Path]] = []
        for slug, bundle, status in results:
            if bundle is not None and (fatal := provider_fatal(bundle)):
                print(f"[suite] EXCLUDE {slug}: provider error {fatal!r} in {bundle}", flush=True)
                bundle, status = None, f"invalid:{fatal}"
            if bundle is not None:
                dest = run_root / slug
                shutil.copytree(bundle, dest, dirs_exist_ok=True)
                copied_bundles.append((slug, dest))
            try:
                src_str = str(bundle.relative_to(ROOT)) if bundle else None
            except ValueError:
                src_str = str(bundle) if bundle else None
            manifest.append({"slug": slug, "status": status, "source": src_str})

        cost_summary = aggregate_suite_usage(copied_bundles)
        print(format_cost_table(cost_summary), flush=True)

        if gw is not None:
            gateway_stats = gw.stats()
            if gateway_stats.get("budget_exceeded"):
                _budget_abort.set()

        budget_exceeded = (
            args.max_usd is not None and cost_summary["total_cost_usd"] > args.max_usd
        ) or _budget_abort.is_set()
        if budget_exceeded and args.max_usd is not None:
            print(
                f"[suite] BUDGET EXCEEDED: total cost ${cost_summary['total_cost_usd']:.4f} > --max-usd ${args.max_usd:.2f}",
                flush=True,
            )

        manifest_payload = {
            "personas": manifest,
            "models": model_overrides,
            "max_days": effective_max_days,
            "cost_summary": cost_summary,
            "gateway_stats": gateway_stats,
            "max_usd": args.max_usd,
            "max_calls": args.max_calls,
            "budget_exceeded": budget_exceeded,
        }
        (run_root / "suite_manifest.json").write_text(json.dumps(manifest_payload, indent=2) + "\n", encoding="utf-8")

        failed = [m["slug"] for m in manifest if m["source"] is None]
        if failed:
            print(f"[suite] personas with no bundle: {', '.join(failed)}", flush=True)
        if args.no_grade or budget_exceeded or len(failed) == len(manifest):
            print(f"[suite] run dir: {run_root}")
            return 1 if (failed or budget_exceeded) else 0

        sc = grader.grade_run(run_root, compare_path=args.compare)
        sc["cost_summary"] = cost_summary
        (run_root / "scorecard.json").write_text(json.dumps(sc, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        redone = rejudge_errors(run_root)
        if redone:
            print(f"[suite] re-judged {len(redone)} judge-error items", flush=True)
        if gw is not None:
            gateway_stats = gw.stats()
            manifest_payload["gateway_stats"] = gateway_stats
            (run_root / "suite_manifest.json").write_text(json.dumps(manifest_payload, indent=2) + "\n", encoding="utf-8")
        print((run_root / "scorecard.md").read_text(encoding="utf-8"))
        print(f"[suite] scorecard: {run_root / 'scorecard.json'}")
        return 0 if (sc["all_tier_a_passed"] and not failed and not budget_exceeded) else 1


if __name__ == "__main__":
    raise SystemExit(main())
