#!/bin/sh
cd "$(dirname "$0")/site" || exit 1
exec python3 - "${1:-3220}" <<'PY'
import subprocess
import sys
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from shutil import which

with ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), SimpleHTTPRequestHandler) as server:
    url = f"http://127.0.0.1:{server.server_port}"
    print(f"Nori Dokyu: {url}", flush=True)
    if opener := which("open"):
        subprocess.Popen([opener, url])
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
PY
