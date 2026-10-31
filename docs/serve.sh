#!/bin/sh
# Serves the docs site locally and opens it. Stop with Ctrl-C.
cd "$(dirname "$0")/site" || exit 1
url=http://127.0.0.1:3220
(sleep 1; open "$url") &
echo "Nori docs: $url"
exec python3 -m http.server 3220 --bind 127.0.0.1
