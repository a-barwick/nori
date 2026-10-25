#!/bin/bash
set -Eeuo pipefail
case "${1:-}" in
  start)
    mountpoint -q /srv/bot-guest || mount /dev/disk/by-id/google-bot-guest-data /srv/bot-guest
    test -f /srv/bot-guest/READY
    test -f /srv/bot-guest/rootfs.ext4
    systemctl start bot-firecracker
    ;;
  request-stop)
    pid=$(systemctl show bot-firecracker --property MainPID --value)
    [ "$pid" != 0 ] || exit 0
    # systemd in the guest reboots orderly, stopping/flushing Chromium first.
    curl --max-time 3 --fail --silent --show-error --unix-socket /run/bot-vm/api.socket \
      -X PUT -H 'Content-Type: application/json' \
      -d '{"action_type":"SendCtrlAltDel"}' http://localhost/actions
    for attempt in $(seq 1 45); do
      kill -0 "$pid" 2>/dev/null || exit 0
      sleep 1
    done
    echo 'Guest did not shut down gracefully' >&2
    exit 1
    ;;
  stop)
    systemctl stop bot-firecracker
    [ "$(systemctl show bot-firecracker --property ExecMainStatus --value)" = 0 ]
    [ "$(systemctl show bot-firecracker --property Result --value)" = success ]
    sync
    if mountpoint -q /srv/bot-guest; then umount /srv/bot-guest; fi
    echo BOT_COMPUTER_GUEST_STOPPED
    ;;
  status)
    systemctl show bot-firecracker -p ActiveState -p SubState -p MainPID
    findmnt /srv/bot-guest || true
    ;;
  logs) tail -200 /var/log/bot-vm-console.log ;;
  *) echo 'Usage: bot-vm start|stop|status|logs' >&2; exit 2 ;;
esac
