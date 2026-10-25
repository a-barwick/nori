#!/bin/bash
set -Eeuo pipefail
case "${1:-}" in
  start)
    mountpoint -q /srv/nori-guest || mount /dev/disk/by-id/google-nori-guest-data /srv/nori-guest
    test -f /srv/nori-guest/READY
    test -f /srv/nori-guest/rootfs.ext4
    systemctl start nori-firecracker
    ;;
  request-stop)
    pid=$(systemctl show nori-firecracker --property MainPID --value)
    [ "$pid" != 0 ] || exit 0
    # systemd in the guest reboots orderly, stopping/flushing Chromium first.
    curl --max-time 3 --fail --silent --show-error --unix-socket /run/nori-vm/api.socket \
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
    systemctl stop nori-firecracker
    [ "$(systemctl show nori-firecracker --property ExecMainStatus --value)" = 0 ]
    [ "$(systemctl show nori-firecracker --property Result --value)" = success ]
    sync
    if mountpoint -q /srv/nori-guest; then umount /srv/nori-guest; fi
    echo NORI_COMPUTER_GUEST_STOPPED
    ;;
  status)
    systemctl show nori-firecracker -p ActiveState -p SubState -p MainPID
    findmnt /srv/nori-guest || true
    ;;
  logs) tail -200 /var/log/nori-vm-console.log ;;
  *) echo 'Usage: nori-vm start|stop|status|logs' >&2; exit 2 ;;
esac
