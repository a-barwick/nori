#!/bin/bash
# Explicit one-off repair for the initial minbase image; never recreates data.
set -Eeuo pipefail
exec > >(tee -a /var/log/nori-computer-repair.log /dev/ttyS0) 2>&1
trap 'status=$?; echo "NORI_COMPUTER_FAIL repair-udev status=$status line=$LINENO"; exit "$status"' ERR
if pgrep -x firecracker >/dev/null; then echo 'Refusing repair with a running VMM' >&2; exit 1; fi
device=/dev/disk/by-id/google-nori-guest-data
[ "$(blkid -s TYPE -o value "$device")" = ext4 ]
[ "$(blkid -s LABEL -o value "$device")" = BOT_GUEST_DATA ]
mkdir -p /srv/nori-guest /mnt/nori-repair
mount "$device" /srv/nori-guest
grep -qx debian-bookworm-firecracker-1.17.0-kernel-6.1.188 /srv/nori-guest/READY
test -s /srv/nori-guest/rootfs.ext4
mount -o loop /srv/nori-guest/rootfs.ext4 /mnt/nori-repair
mount --bind /dev /mnt/nori-repair/dev
mount -t proc proc /mnt/nori-repair/proc
mount -t sysfs sysfs /mnt/nori-repair/sys
test ! -e /mnt/nori-repair/usr/sbin/policy-rc.d
printf '#!/bin/sh\nexit 101\n' > /mnt/nori-repair/usr/sbin/policy-rc.d
chmod 755 /mnt/nori-repair/usr/sbin/policy-rc.d
timeout 240 chroot /mnt/nori-repair /usr/bin/env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends udev
rm /mnt/nori-repair/usr/sbin/policy-rc.d
chroot /mnt/nori-repair dpkg-query -W udev
sync
umount /mnt/nori-repair/dev /mnt/nori-repair/proc /mnt/nori-repair/sys
umount /mnt/nori-repair
umount /srv/nori-guest
echo NORI_COMPUTER_UDEV_REPAIRED
curl --fail --silent --show-error -H 'Metadata-Flavor: Google' \
  http://metadata.google.internal/computeMetadata/v1/instance/attributes/nori-normal-startup > /run/nori-computer-normal-startup.sh
exec /bin/bash /run/nori-computer-normal-startup.sh
