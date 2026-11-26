#!/usr/bin/env bash
set -euo pipefail

if ! getent group nori-connector >/dev/null; then
  groupadd --system nori-connector
fi
if ! id -u nori-connector >/dev/null 2>&1; then
  useradd --system --gid nori-connector --home-dir /var/lib/nori-connector --no-create-home --shell /usr/sbin/nologin nori-connector
fi
install -d -m 0700 -o nori-connector -g nori-connector /var/lib/nori-connector
if [ -d /run/systemd/system ]; then
  systemctl daemon-reload
  systemctl enable --now nori-connector.service
fi
