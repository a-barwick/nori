"""Print the run from the newest saved snapshot, so checking it adds no polling:
  python3 show.py [messages]"""
import json
import sys

from lib import HERE

s = json.loads(sorted((HERE / "snaps").glob("*.json"))[-1].read_text())
n = int(sys.argv[1]) if len(sys.argv) > 1 else 6
c = s.get("conversation", {})
print("conv", {k: c.get(k) for k in ["pending", "processing", "routing_blocked"]}, "paused", s.get("paused"))
for m in s["messages"][-n:]:
    print(m["role"][0], m["created_at"][11:19], m["text"][:700].replace("\n", " / "))
for r in s["runs"][:4]:
    print("run", r["id"][:8], r["status"], "ev", len(r.get("events") or []), "art", len(r.get("artifacts") or []), "usd", r.get("spent_usd"), (r.get("request") or "")[:80], "| Q:", (r.get("question") or {}).get("text"), "| ERR:", (r.get("error") or "")[:120])
