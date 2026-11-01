"""Save every changed session snapshot and task screenshot from the live runtime.
  python3 rec.py <seconds>   writes snaps/<ms>.json and art/<artifact id>.png
Polling faster than 7 s gets HTTP 429 from Agent Runtime and slows the run itself."""
import hashlib
import json
import os
import sys
import time
from urllib.request import Request, urlopen

from lib import HERE, ROOT, call, ops

(HERE / "snaps").mkdir(exist_ok=True)
(HERE / "art").mkdir(exist_ok=True)
last = None
end = time.time() + float(sys.argv[1])
while time.time() < end:
    try:
        s = call("session", {})
    except Exception as e:
        print("poll error", str(e)[:120], flush=True)
        time.sleep(20)
        continue
    blob = json.dumps(s, sort_keys=True)
    digest = hashlib.sha1(blob.encode()).hexdigest()
    if digest != last:
        last = digest
        (HERE / "snaps" / f"{int(time.time() * 1000)}.json").write_text(blob)
    for r in s.get("runs", []):
        for a in r.get("artifacts") or []:
            path = HERE / "art" / f"{a['id']}.png"
            if path.exists():
                continue
            try:
                req = Request(f"{ROOT}/api/artifact", data=json.dumps({"run_id": r["id"], "artifact_id": a["id"]}).encode(), method="POST", headers={"Authorization": "Bearer " + poc.gcloud("auth", "print-access-token"), "Content-Type": "application/json", "x-goog-user-project": "bot-111549"})
                with urlopen(req, timeout=40, context=ops.SSL_CONTEXT) as res:
                    path.write_bytes(res.read())
                print("artifact", a["id"], flush=True)
            except Exception as e:
                print("artifact error", str(e)[:100], flush=True)
    time.sleep(7)
