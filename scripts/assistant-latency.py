#!/usr/bin/env python3
"""Latency and cost baseline for background tasks on a local Nori stack.

For each task run of one user, prints the path from user input to delivered
result, split into stages:

  ack       user message saved -> assistant acknowledgment saved (outer loop)
  dispatch  user message saved -> task run started (outer model, acceptance, wake)
  exec      task run started -> task run finished (inner Postgres-native runner)
  deliver   task finished -> result message saved in the conversation
  total     user message -> result message

and the cost of every model call linked to the turn, the task and its terminal
event, from the usage records the snapshot already carries.

Reads only: GET /users/{id}/v1/snapshot on core (`cmd/nori` or `cmd/nori-local`).
Usage:
  scripts/assistant-latency.py --core http://127.0.0.1:8080 --user <uuid> [--runs id,id]
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


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--core", default="http://127.0.0.1:8080")
    p.add_argument("--user", required=True)
    p.add_argument("--runs", default="", help="comma-separated run ids (default: all completed runs)")
    a = p.parse_args()

    core = a.core.rstrip("/")
    with urllib.request.urlopen(f"{core}/users/{a.user}/v1/snapshot") as r:
        snap = json.load(r)
    messages = sorted(snap["messages"], key=lambda m: m["created_at"])
    usage = snap.get("usage") or []
    wanted = set(filter(None, a.runs.split(",")))
    runs = [r for r in snap["runs"] if r["status"] == "completed" and (not wanted or r["id"] in wanted)]
    runs.sort(key=lambda r: r["started_at"])

    rows = []
    for run in runs:
        started, finished = ts(run["started_at"]), ts(run.get("finished_at"))
        # The task is dispatched during the turn that answers the last user message saved before it
        # started; the acknowledgment is that turn's assistant message, often saved after dispatch.
        user = next((m for m in reversed(messages) if m["role"] == "user" and ts(m["created_at"]) <= started), None)
        ack = next((m for m in messages if user and m["role"] == "assistant" and m["turn_id"] == user["turn_id"]), None)
        result = next((m for m in messages if m["role"] == "assistant" and finished and ts(m["created_at"]) >= finished), None)
        turn = user["turn_id"] if user else None
        links = {run["id"], f"event-{run['id']}-terminal"} | ({turn, user["id"].removesuffix("-user")} if user else set())
        calls = [u for u in usage if u.get("input_id") in links]
        cost = sum(u.get("cost_usd") or 0 for u in calls)
        unknown = sum(1 for u in calls if u.get("cost_status") == "unknown")
        rows.append({
            "run": run["id"][:8],
            "ack": secs(ts(user["created_at"]) if user else None, ts(ack["created_at"]) if ack else None),
            "dispatch": secs(ts(user["created_at"]) if user else None, started),
            "exec": secs(started, finished),
            "deliver": secs(finished, ts(result["created_at"]) if result else None),
            "total": secs(ts(user["created_at"]) if user else None, ts(result["created_at"]) if result else None),
            "calls": len(calls), "cost": cost, "unknown": unknown,
        })

    cols = ["run", "ack", "dispatch", "exec", "deliver", "total", "calls", "cost", "unknown"]
    print("| " + " | ".join(cols) + " |")
    print("|" + "---|" * len(cols))
    for r in rows:
        cells = []
        for c in cols:
            v = r[c]
            cells.append("n/a" if v is None else (f"{v:.4f}" if c == "cost" else f"{v:.1f}" if isinstance(v, float) else str(v)))
        print("| " + " | ".join(cells) + " |")


if __name__ == "__main__":
    sys.exit(main())
