#!/bin/sh
# Serves the docs site locally and opens it. Stop with Ctrl-C. Usage: docs/serve.sh [port]
cd "$(dirname "$0")/site" || exit 1
port=${1:-3220}
url=http://127.0.0.1:$port
(sleep 1; open "$url") &
echo "Nori Dokyu: $url"
exec python3 -m http.server "$port" --bind 127.0.0.1
