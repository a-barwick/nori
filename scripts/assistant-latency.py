#!/usr/bin/env python3
"""Latency and token/cost baseline for Nori runs and turns on a local stack.

For each task run of one user (completed, failed, or cancelled), prints the exact
ID-correlated path from user input to delivered result:

  ack       user message saved -> assistant acknowledgment saved (same turn_id)
  dispatch  user message saved -> task run started
  exec      task run started -> task run finished
  deliver   task finished -> terminal delivery assistant message saved
  total     user message -> terminal delivery assistant message

and aggregates every usage record linked to the turn, task run, and terminal event
(tokens, cache reads/writes, cache hit %, cost, unknown count).

Reads only: GET /users/{id}/v1/snapshot on core (`cmd/nori` or `cmd/nori-local`).
Usage:
  scripts/assistant-latency.py --core http://127.0.0.1:8080 --user <uuid> [--runs id,id] [--all]
"""
import argparse
import json
import sys
import urllib.request
from datetime import datetime


def ts(s):
    if not s:
        return None
    s = s.replace("Z", "+00:00")
    if "." in s:
        head, rest = s.split(".", 1)
        frac, tz = rest[:-6], rest[-6:]
        s = f"{head}.{frac[:6].ljust(6, '0')}{tz}"
    return datetime.fromisoformat(s)


def secs(a, b):
    return None if a is None or b is None else (b - a).total_seconds()


def find_origin_user_message(run, messages, msg_by_id, started):
    origin = run.get("origin") or {}
    origin_msg_id = origin.get("message_id") or ""
    if origin_msg_id:
        for cand in (origin_msg_id, f"{origin_msg_id}-user"):
            if cand in msg_by_id:
                return msg_by_id[cand]
    return next(
        (m for m in reversed(messages) if m.get("role") == "user" and started and ts(m.get("created_at")) <= started),
        None,
    )


def find_delivery_message(run, messages, usage, finished):
    run_id = run["id"]
    term_input_id = f"event-{run_id}-terminal"
    for m in messages:
        if m.get("role") != "assistant":
            continue
        if m.get("run_id") == run_id and finished and ts(m.get("created_at")) >= finished:
            return m
    term_usage = [u for u in usage if u.get("input_id") == term_input_id and u.get("created_at")]
    if term_usage:
        term_ts = min(ts(u["created_at"]) for u in term_usage)
        cand = next((m for m in messages if m.get("role") == "assistant" and ts(m.get("created_at")) >= term_ts), None)
        if cand is not None:
            return cand
    origin = run.get("origin") or {}
    origin_msg_id = origin.get("message_id") or ""
    for m in messages:
        if m.get("role") != "assistant" or not finished:
            continue
        if ts(m.get("created_at")) < finished:
            continue
        if origin_msg_id and m.get("id") == origin_msg_id:
            continue
        return m
    return None


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--core", default="http://127.0.0.1:8080")
    p.add_argument("--user", required=True)
    p.add_argument("--runs", default="", help="comma-separated run ids (default: all terminal runs)")
    p.add_argument("--include-running", action="store_true", help="include running tasks")
    p.add_argument("--json", action="store_true", help="emit JSON report instead of markdown table")
    a = p.parse_args()

    core = a.core.rstrip("/")
    with urllib.request.urlopen(f"{core}/users/{a.user}/v1/snapshot") as r:
        snap = json.load(r)
    messages = sorted(snap.get("messages") or [], key=lambda m: m.get("created_at") or "")
    msg_by_id = {m["id"]: m for m in messages if m.get("id")}
    usage = snap.get("usage") or []
    wanted = set(filter(None, a.runs.split(",")))

    allowed_statuses = {"completed", "failed", "cancelled"}
    if a.include_running:
        allowed_statuses.add("running")

    runs = [
        r for r in (snap.get("runs") or [])
        if r.get("status") in allowed_statuses and (not wanted or r["id"] in wanted)
    ]
    runs.sort(key=lambda r: r.get("started_at") or "")

    rows = []
    for run in runs:
        started, finished = ts(run.get("started_at")), ts(run.get("finished_at"))
        user = find_origin_user_message(run, messages, msg_by_id, started)
        ack = next(
            (m for m in messages if user and m.get("role") == "assistant" and m.get("turn_id") and m.get("turn_id") == user.get("turn_id")),
            None,
        )
        result = find_delivery_message(run, messages, usage, finished)

        links = {run["id"], f"event-{run['id']}-terminal"}
        if user:
            if user.get("turn_id"):
                links.add(user["turn_id"])
            if user.get("id"):
                links.add(user["id"])
                links.add(user["id"].removesuffix("-user"))
        origin = run.get("origin") or {}
        if origin.get("message_id"):
            links.add(origin["message_id"])
            links.add(origin["message_id"].removesuffix("-user"))
        if result and result.get("turn_id"):
            links.add(result["turn_id"])

        calls = [u for u in usage if u.get("input_id") in links and u.get("request_status") != "not_sent"]
        in_tok = sum(u.get("input_tokens") or 0 for u in calls)
        cached_tok = sum(u.get("cached_tokens") or 0 for u in calls)
        write_tok = sum(u.get("cache_write_tokens") or 0 for u in calls)
        out_tok = sum(u.get("output_tokens") or 0 for u in calls)
        cache_pct = (100.0 * cached_tok / in_tok) if in_tok > 0 else 0.0
        cost = sum(u.get("cost_usd") or 0.0 for u in calls)
        unknown = sum(1 for u in calls if u.get("cost_status") == "unknown")

        rows.append({
            "run": run["id"][:8],
            "status": run.get("status", "?"),
            "ack": secs(ts(user["created_at"]) if user else None, ts(ack["created_at"]) if ack else None),
            "dispatch": secs(ts(user["created_at"]) if user else None, started),
            "exec": secs(started, finished),
            "deliver": secs(finished, ts(result["created_at"]) if result else None),
            "total": secs(ts(user["created_at"]) if user else None, ts(result["created_at"]) if result else None),
            "calls": len(calls),
            "in_tok": in_tok,
            "cached": cached_tok,
            "write": write_tok,
            "out_tok": out_tok,
            "cache%": cache_pct,
            "cost": cost,
            "unknown": unknown,
        })

    all_sent = [u for u in usage if u.get("request_status") != "not_sent"]
    summary = {
        "total_calls": len(all_sent),
        "total_input_tokens": sum(u.get("input_tokens") or 0 for u in all_sent),
        "total_cached_tokens": sum(u.get("cached_tokens") or 0 for u in all_sent),
        "total_cache_write_tokens": sum(u.get("cache_write_tokens") or 0 for u in all_sent),
        "total_output_tokens": sum(u.get("output_tokens") or 0 for u in all_sent),
        "total_cost_usd": sum(u.get("cost_usd") or 0.0 for u in all_sent),
        "total_unknown_calls": sum(1 for u in all_sent if u.get("cost_status") == "unknown"),
    }
    if summary["total_input_tokens"] > 0:
        summary["overall_cache_pct"] = 100.0 * summary["total_cached_tokens"] / summary["total_input_tokens"]
    else:
        summary["overall_cache_pct"] = 0.0

    if a.json:
        print(json.dumps({"runs": rows, "summary": summary}, indent=2))
        return 0

    cols = ["run", "status", "ack", "dispatch", "exec", "deliver", "total", "calls", "in_tok", "cached", "write", "out_tok", "cache%", "cost", "unknown"]
    print("| " + " | ".join(cols) + " |")
    print("|" + "---|" * len(cols))
    for r in rows:
        cells = []
        for c in cols:
            v = r[c]
            if v is None:
                cells.append("n/a")
            elif c == "cost":
                cells.append(f"{v:.4f}")
            elif c == "cache%":
                cells.append(f"{v:.1f}%")
            elif isinstance(v, float):
                cells.append(f"{v:.1f}")
            else:
                cells.append(str(v))
        print("| " + " | ".join(cells) + " |")
    print(
        f"\nSnapshot totals: calls={summary['total_calls']} in={summary['total_input_tokens']} "
        f"cached={summary['total_cached_tokens']} ({summary['overall_cache_pct']:.1f}%) "
        f"write={summary['total_cache_write_tokens']} out={summary['total_output_tokens']} "
        f"cost=${summary['total_cost_usd']:.4f} unknown={summary['total_unknown_calls']}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
