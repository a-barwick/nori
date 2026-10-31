"""Turn snaps/ into capture/run.json, keeping only the filmed part of the thread.
  python3 build_run.py "<start of the first filmed message>" <run id prefix> [<run id prefix> ...]
Copy the task screenshots the film opens from art/ to public/real/ yourself."""
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
first, ids = sys.argv[1], tuple(sys.argv[2:])
out, start = [], None
for f in sorted((HERE / "snaps").glob("*.json")):
    s = json.loads(f.read_text())
    if start is None:
        opening = next((m for m in s["messages"] if m["text"].startswith(first)), None)
        if not opening:
            continue
        start = opening["created_at"]
    out.append({"ts": int(f.stem), "session": {
        "paused": False,
        "messages": [m for m in s["messages"] if m["created_at"] >= start],
        "runs": [r for r in s["runs"] if r["id"].startswith(ids)],
        "dispatches": [d for d in s.get("dispatches", []) if d["created_at"] >= start],
        "conversation": s["conversation"],
    }})
target = HERE.parent / "capture/run.json"
target.write_text(json.dumps(out))
print(len(out), "snapshots", target.stat().st_size, "bytes")
