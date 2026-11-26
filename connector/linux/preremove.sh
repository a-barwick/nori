#!/usr/bin/env bash
set -euo pipefail

if [ -d /run/systemd/system ]; then
  systemctl disable --now nori-connector.service
fi
