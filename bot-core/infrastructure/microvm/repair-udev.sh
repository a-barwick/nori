#!/bin/bash
# Explicit one-off repair for the initial minbase image; never recreates data.
set -Eeuo pipefail
exec > >(tee -a /var/log/bot-computer-repair.log /dev/ttyS0) 2>&1
trap 'status=$?; echo "BOT_COMPUTER_FAIL repair-udev status=$status line=$LINENO"; exit "$status"' ERR
if pgrep -x firecracker >/dev/null; then echo 'Refusing repair with a running VMM' >&2; exit 1; fi
device=/dev/disk/by-id/google-bot-guest-data
[ "$(blkid -s TYPE -o value "$device")" = ext4 ]
[ "$(blkid -s LABEL -o value "$device")" = BOT_GUEST_DATA ]
mkdir -p /srv/bot-guest /mnt/bot-repair
mount "$device" /srv/bot-guest
grep -qx debian-bookworm-firecracker-1.17.0-kernel-6.1.188 /srv/bot-guest/READY
test -s /srv/bot-guest/rootfs.ext4
mount -o loop /srv/bot-guest/rootfs.ext4 /mnt/bot-repair
mount --bind /dev /mnt/bot-repair/dev
mount -t proc proc /mnt/bot-repair/proc
mount -t sysfs sysfs /mnt/bot-repair/sys
test ! -e /mnt/bot-repair/usr/sbin/policy-rc.d
printf '#!/bin/sh\nexit 101\n' > /mnt/bot-repair/usr/sbin/policy-rc.d
chmod 755 /mnt/bot-repair/usr/sbin/policy-rc.d
timeout 240 chroot /mnt/bot-repair /usr/bin/env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends udev
rm /mnt/bot-repair/usr/sbin/policy-rc.d
chroot /mnt/bot-repair dpkg-query -W udev
sync
umount /mnt/bot-repair/dev /mnt/bot-repair/proc /mnt/bot-repair/sys
umount /mnt/bot-repair
umount /srv/bot-guest
echo BOT_COMPUTER_UDEV_REPAIRED
curl --fail --silent --show-error -H 'Metadata-Flavor: Google' \
  http://metadata.google.internal/computeMetadata/v1/instance/attributes/bot-normal-startup > /run/bot-computer-normal-startup.sh
exec /bin/bash /run/bot-computer-normal-startup.sh
