#!/bin/sh
set -eu
mount -o remount,rw /sys/fs/cgroup
mkdir -p /profile /tasks
chromium --headless --no-sandbox --disable-dev-shm-usage --disable-background-networking --no-first-run --no-default-browser-check --user-data-dir=/profile --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 >/chromium.log 2>&1 &
exec /usr/local/bin/computer-agent -listen 0.0.0.0:8090 -tasks /tasks
