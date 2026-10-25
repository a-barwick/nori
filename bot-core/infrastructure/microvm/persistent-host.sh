#!/bin/bash
set -Eeuo pipefail
exec > >(tee -a /var/log/bot-computer-setup.log /dev/ttyS0) 2>&1
trap 'status=$?; echo "BOT_COMPUTER_FAIL status=$status line=$LINENO"; exit "$status"' ERR
meta() { curl --fail --silent --show-error -H 'Metadata-Flavor: Google' "http://metadata.google.internal/computeMetadata/v1/instance/attributes/$1"; }
export DEBIAN_FRONTEND=noninteractive
timeout 180 apt-get update
timeout 240 apt-get -o DPkg::Lock::Timeout=120 install -y --no-install-recommends \
  debootstrap debian-archive-keyring ca-certificates curl e2fsprogs iptables openssh-server sudo
install -d /etc/bot-computer /usr/local/lib/bot-computer /opt/bot-firecracker
meta bot-public-key > /etc/bot-computer/operator.pub
meta bot-build-guest > /usr/local/lib/bot-computer/build-guest.sh
meta bot-vm-control > /usr/local/sbin/bot-vm
meta bot-browser-close > /usr/local/lib/bot-computer/browser-close.py
chmod 755 /usr/local/sbin/bot-vm
id hostbot >/dev/null 2>&1 || useradd --create-home --shell /bin/bash hostbot
usermod --password '*' hostbot
install -d -o hostbot -g hostbot -m 700 /home/hostbot/.ssh
install -o hostbot -g hostbot -m 600 /etc/bot-computer/operator.pub /home/hostbot/.ssh/authorized_keys
printf 'hostbot ALL=(root) NOPASSWD: /usr/local/sbin/bot-vm\n' > /etc/sudoers.d/bot-vm
chmod 440 /etc/sudoers.d/bot-vm
cat > /etc/ssh/sshd_config.d/90-bot.conf <<'SSH'
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
AllowUsers hostbot
AllowAgentForwarding no
AllowTcpForwarding local
PermitOpen 172.30.0.2:22
SSH
systemctl reload ssh
modprobe kvm_intel
test -c /dev/kvm
test -r /dev/kvm
test -w /dev/kvm
cd /opt/bot-firecracker
curl -fL --connect-timeout 10 --max-time 180 -o firecracker.tgz https://github.com/firecracker-microvm/firecracker/releases/download/v1.17.0/firecracker-v1.17.0-x86_64.tgz
printf '%s  %s\n' 06094a1108ae9e82aa4c23a775aa92758f53f1175d422270d9d6162cb9ade558 firecracker.tgz | sha256sum --check
curl -fL --connect-timeout 10 --max-time 180 -o vmlinux https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20261023-dd7c24b0d165-0/x86_64/vmlinux-6.1.188
printf '%s  %s\n' fc7a33d23a629d64db61ddf12789c737c12edb1702a5ce9d5546b95ca21dc6ca vmlinux | sha256sum --check
tar -xzf firecracker.tgz release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64
install -m 755 release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64 /usr/local/bin/firecracker

device=/dev/disk/by-id/google-bot-guest-data
test -b "$device"
mkdir -p /srv/bot-guest
fresh=$(meta bot-fresh-disk)
# Query errors are fatal. Blank is accepted only with the client-created flag.
signatures=$(wipefs --no-act --noheadings --output TYPE "$device")
initialize=false
if [ -z "$signatures" ]; then
  [ "$fresh" = true ] || { echo 'Reused blank disk: refusing format' >&2; exit 1; }
  mkfs.ext4 -L BOT_GUEST_DATA -F "$device"
  initialize=true
else
  type=$(blkid -s TYPE -o value "$device")
  label=$(blkid -s LABEL -o value "$device")
  [ "$type" = ext4 ]
  [ "$label" = BOT_GUEST_DATA ]
fi
mount "$device" /srv/bot-guest
if "$initialize"; then
  /bin/bash /usr/local/lib/bot-computer/build-guest.sh
else
  # Also covers a manual host reboot with its original fresh=true metadata.
  # Any missing/partial image is preserved, never rebuilt automatically.
  test -f /srv/bot-guest/READY
  test -s /srv/bot-guest/rootfs.ext4
  grep -qx debian-bookworm-firecracker-1.17.0-kernel-6.1.188 /srv/bot-guest/READY
fi

# Refresh only the operator public key before the retained guest is booted.
# Never execute guest programs as host root or follow guest-controlled symlinks.
(
  if pgrep -x firecracker >/dev/null; then echo 'Refusing key update with a running VMM' >&2; exit 1; fi
  image=/srv/bot-guest/rootfs.ext4
  test ! -L "$image"
  loops=$(losetup --associated "$image" --noheadings --output NAME)
  test -z "$loops"
  [ "$(blkid -p -s TYPE -o value "$image")" = ext4 ]
  ssh-keygen -l -f /etc/bot-computer/operator.pub >/dev/null
  guest_root=$(mktemp -d /mnt/bot-key.XXXXXX)
  cleanup_key_mount() {
    status=$?
    trap - EXIT
    if mountpoint -q "$guest_root"; then umount "$guest_root" || status=1; fi
    rmdir "$guest_root" || status=1
    exit "$status"
  }
  trap cleanup_key_mount EXIT
  mount -t ext4 -o loop,nodev,nosuid,noexec "$image" "$guest_root"
  for directory in etc home home/bot; do
    test ! -L "$guest_root/$directory"
    test -d "$guest_root/$directory"
  done
  test ! -L "$guest_root/etc/passwd"
  test -f "$guest_root/etc/passwd"
  awk -F: '$1 == "bot" { if ($3 != 1000 || $4 != 1000 || $6 != "/home/bot") exit 1; found++ } END { if (found != 1) exit 1 }' "$guest_root/etc/passwd"
  test ! -L "$guest_root/home/bot/.ssh"
  install -d -o 1000 -g 1000 -m 700 "$guest_root/home/bot/.ssh"
  authorized_keys="$guest_root/home/bot/.ssh/authorized_keys"
  test ! -L "$authorized_keys"
  if [ -e "$authorized_keys" ]; then test -f "$authorized_keys"; fi
  key_file=$(mktemp "$guest_root/home/bot/.ssh/.authorized_keys.XXXXXX")
  install -o 1000 -g 1000 -m 600 /etc/bot-computer/operator.pub "$key_file"
  mv -fT "$key_file" "$authorized_keys"
  sync -f "$authorized_keys"
)

# Guest egress is public IPv4 only. It cannot initiate connections to the host,
# metadata, private, link-local, shared-address, loopback, or reserved networks.
ip tuntap add tap-bot mode tap
ip addr add 172.30.0.1/30 dev tap-bot
ip link set tap-bot up
sysctl -w net.ipv4.ip_forward=1
sysctl -w net.ipv6.conf.tap-bot.disable_ipv6=1
external=$(ip -4 route show default | awk 'NR==1 {print $5}')
test -n "$external"
iptables -N BOT_GUEST_EGRESS
for range in 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 192.0.0.0/24 198.18.0.0/15 224.0.0.0/4 240.0.0.0/4; do
  iptables -A BOT_GUEST_EGRESS -d "$range" -j REJECT
done
iptables -A BOT_GUEST_EGRESS -o "$external" -j ACCEPT
iptables -A BOT_GUEST_EGRESS -j REJECT
iptables -I FORWARD 1 -i tap-bot -j BOT_GUEST_EGRESS
iptables -I FORWARD 1 -o tap-bot -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -I INPUT 1 -i tap-bot -j REJECT
iptables -I INPUT 1 -i tap-bot -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -I INPUT 1 -p tcp --dport 22 ! -s 35.235.240.0/20 -j REJECT
iptables -t nat -A POSTROUTING -s 172.30.0.0/30 -o "$external" -j MASQUERADE
cat > /etc/bot-computer/vm.json <<'CONFIG'
{
  "boot-source":{"kernel_image_path":"/opt/bot-firecracker/vmlinux","boot_args":"console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw rootfstype=ext4 ipv6.disable=1 net.ifnames=0"},
  "drives":[{"drive_id":"rootfs","path_on_host":"/srv/bot-guest/rootfs.ext4","is_root_device":true,"is_read_only":false,"cache_type":"Writeback"}],
  "network-interfaces":[{"iface_id":"eth0","guest_mac":"06:00:AC:10:00:02","host_dev_name":"tap-bot"}],
  "machine-config":{"vcpu_count":2,"mem_size_mib":2048}
}
CONFIG
cat > /usr/local/lib/bot-computer/run-vm.sh <<'RUN'
#!/bin/bash
set -euo pipefail
mkdir -p /run/bot-vm
rm -f /run/bot-vm/api.socket
[ -p /run/bot-vm/console-input ] || mkfifo /run/bot-vm/console-input
exec 3<>/run/bot-vm/console-input
exec /usr/local/bin/firecracker --api-sock /run/bot-vm/api.socket --config-file /etc/bot-computer/vm.json <&3
RUN
chmod 755 /usr/local/lib/bot-computer/run-vm.sh
cat > /etc/systemd/system/bot-firecracker.service <<'SERVICE'
[Unit]
Description=Persistent Bot Firecracker guest
After=network.target
[Service]
Type=simple
ExecStart=/usr/local/lib/bot-computer/run-vm.sh
ExecStop=/usr/local/sbin/bot-vm request-stop
TimeoutStopSec=55
StandardOutput=append:/var/log/bot-vm-console.log
StandardError=inherit
Restart=no
[Install]
WantedBy=multi-user.target
SERVICE
systemctl daemon-reload
/usr/local/sbin/bot-vm start
echo BOT_COMPUTER_HOST_READY
