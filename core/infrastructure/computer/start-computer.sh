#!/bin/sh
set -eu
mkdir -p /home/agent/chromium /workspace/tasks
# Chromium binds its debugging listener to loopback; socat makes it reachable
# on the computer container. No CDP port is published on the host.
chromium --headless --no-sandbox --disable-dev-shm-usage \
  --no-first-run --no-default-browser-check \
  --user-data-dir=/home/agent/chromium \
  --remote-debugging-port=9223 about:blank &
chromium_pid=$!
socat TCP-LISTEN:9222,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:9223 &
proxy_pid=$!
shutdown() {
  trap '' TERM INT
  if ! python3 /usr/local/bin/close-browser.py; then
    echo "Chromium clean close failed; pending profile writes may be lost" >&2
  fi
  for attempt in 1 2 3 4 5; do
    kill -0 "$chromium_pid" 2>/dev/null || break
    sleep 1
  done
  kill "$chromium_pid" "$proxy_pid" 2>/dev/null || true
  wait "$chromium_pid" "$proxy_pid" 2>/dev/null || true
  exit 0
}
trap shutdown TERM INT
# Exit if either process fails so the restart policy can recover the computer.
while kill -0 "$chromium_pid" && kill -0 "$proxy_pid"; do
  sleep 2
done
kill "$chromium_pid" "$proxy_pid" 2>/dev/null || true
wait
exit 1
