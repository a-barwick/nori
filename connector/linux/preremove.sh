#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" = "remove" ] && [ -d /run/systemd/system ]; then
  systemctl disable --now nori-connector.service
fi
