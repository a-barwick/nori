#!/bin/bash
# Initial creation only; existing guest files are never rebuilt automatically.
set -Eeuo pipefail
store=/srv/bot-guest
image=$store/rootfs.ext4.incomplete
root=/mnt/bot-rootfs
test ! -e "$store/rootfs.ext4"
test ! -e "$image"
test ! -e "$store/READY"
truncate -s 16G "$image" # Sparse: do not preallocate the retained 20 GiB disk.
mkfs.ext4 -F "$image"
mkdir -p "$root"
mount -o loop "$image" "$root"
# The Ubuntu-supplied Debian archive keyring authenticates the Bookworm archive.
timeout 600 debootstrap --arch amd64 --variant minbase --include ca-certificates --force-check-gpg \
  --keyring=/usr/share/keyrings/debian-archive-keyring.gpg \
  bookworm "$root" https://deb.debian.org/debian
mount --bind /dev "$root/dev"
mount -t proc proc "$root/proc"
mount -t sysfs sysfs "$root/sys"
printf '#!/bin/sh\nexit 101\n' > "$root/usr/sbin/policy-rc.d"
chmod 755 "$root/usr/sbin/policy-rc.d"
cat > "$root/etc/apt/sources.list" <<'SOURCES'
deb https://deb.debian.org/debian bookworm main
deb https://deb.debian.org/debian bookworm-updates main
deb https://security.debian.org/debian-security bookworm-security main
SOURCES
cp /etc/resolv.conf "$root/etc/resolv.conf"
chroot "$root" /usr/bin/env DEBIAN_FRONTEND=noninteractive apt-get update
timeout 600 chroot "$root" /usr/bin/env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  systemd-sysv udev openssh-server sudo chromium chromium-sandbox fonts-dejavu-core curl ca-certificates \
  python3 python3-websocket iproute2 procps dbus-user-session
rm "$root/usr/sbin/policy-rc.d"
chroot "$root" useradd --create-home --shell /bin/bash bot
# Password login is disabled; unlock account for public-key SSH only.
chroot "$root" usermod --password '*' bot
printf 'bot ALL=(ALL) NOPASSWD: ALL\n' > "$root/etc/sudoers.d/bot"
chmod 440 "$root/etc/sudoers.d/bot"
install -d -o 1000 -g 1000 -m 700 "$root/home/bot/.ssh"
install -o 1000 -g 1000 -m 600 /etc/bot-computer/operator.pub "$root/home/bot/.ssh/authorized_keys"
install -d -o 1000 -g 1000 "$root/workspace" "$root/home/bot/.config/chromium-bot"
cat > "$root/etc/ssh/sshd_config.d/90-bot.conf" <<'SSH'
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
AllowUsers bot
AllowAgentForwarding no
AllowTcpForwarding local
PermitOpen 127.0.0.1:9222 127.0.0.1:8000
SSH
printf 'bot-computer\n' > "$root/etc/hostname"
printf '127.0.0.1 localhost\n127.0.1.1 bot-computer\n' > "$root/etc/hosts"
printf '/dev/vda / ext4 defaults 0 1\n' > "$root/etc/fstab"
printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > "$root/etc/resolv.conf"
mkdir -p "$root/etc/systemd/network"
cat > "$root/etc/systemd/network/10-eth0.network" <<'NETWORK'
[Match]
Name=eth0
[Network]
Address=172.30.0.2/30
Gateway=172.30.0.1
IPv6AcceptRA=no
LinkLocalAddressing=no
NETWORK
install -m 755 /usr/local/lib/bot-computer/browser-close.py "$root/usr/local/bin/browser-close.py"
cat > "$root/etc/systemd/system/bot-browser.service" <<'SERVICE'
[Unit]
Description=Persistent headless Chromium
After=network.target
[Service]
Type=simple
User=bot
Environment=HOME=/home/bot
ExecStart=/usr/bin/chromium --headless --no-first-run --no-default-browser-check --user-data-dir=/home/bot/.config/chromium-bot --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 about:blank
ExecStop=/usr/bin/python3 /usr/local/bin/browser-close.py $MAINPID
ExecStopPost=/usr/bin/sync
TimeoutStopSec=25
Restart=no
[Install]
WantedBy=multi-user.target
SERVICE
# No fixture page, cookie, marker, or tool-persistence proof is baked in.
chroot "$root" systemctl enable ssh systemd-networkd bot-browser
chroot "$root" ssh-keygen -A
chroot "$root" apt-get clean
chroot "$root" dpkg-query -W chromium chromium-sandbox > "$store/guest-packages.txt"
sync
umount "$root/dev" "$root/proc" "$root/sys"
umount "$root"
mv "$image" "$store/rootfs.ext4"
printf 'debian-bookworm-firecracker-1.17.0-kernel-6.1.188\n' > "$store/READY"
sync
echo BOT_COMPUTER_IMAGE_READY
