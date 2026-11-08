#!/bin/bash
# Retained computer host: no guest programs are executed by host root.
set -Eeuo pipefail
exec > >(tee -a /var/log/nori-computer-setup.log /dev/ttyS0) 2>&1
trap 'echo "NORI_COMPUTER_FAIL line=$LINENO status=$?"' ERR
meta() { curl -fsS --connect-timeout 5 -H 'Metadata-Flavor: Google' "http://metadata.google.internal/computeMetadata/v1/$1"; }
attribute() { meta "instance/attributes/$1"; }
if [ "$(attribute computer-paused)" = true ]; then echo NORI_COMPUTER_PAUSED; exit 0; fi
export DEBIAN_FRONTEND=noninteractive
if ! command -v iptables >/dev/null || ! command -v curl >/dev/null; then
  timeout 180 apt-get update
  timeout 240 apt-get -o DPkg::Lock::Timeout=120 install -y --no-install-recommends ca-certificates curl e2fsprogs iptables
fi
install -d /etc/nori-computer /usr/local/lib/nori-computer /opt/nori-firecracker /srv/nori-guest
python3 - <<'TELEMETRY'
import json,re,urllib.request
from pathlib import Path
request=urllib.request.Request('http://metadata.google.internal/computeMetadata/v1/instance/attributes/computer-telemetry',headers={'Metadata-Flavor':'Google'})
try:
    with urllib.request.urlopen(request,timeout=5) as response: values=json.load(response)
except Exception: values={}
patterns={'SENTRY_DSN':r'https://[a-f0-9]+@[a-z0-9.-]+/[0-9]+','SENTRY_ENVIRONMENT':r'[a-z0-9_-]{1,64}','SENTRY_RELEASE':r'[a-zA-Z0-9_.-]{1,100}','SENTRY_TRACES_SAMPLE_RATE':r'(?:0(?:\.[0-9]+)?|1(?:\.0+)?)'}
lines=[key+'='+value for key,value in values.items() if key in patterns and isinstance(value,str) and re.fullmatch(patterns[key],value)]
Path('/etc/nori-computer/telemetry.env').write_text('\n'.join(lines)+'\n')
Path('/etc/nori-computer/telemetry.env').chmod(0o644)
TELEMETRY
attribute nori-vm-control > /usr/local/sbin/nori-vm
chmod 755 /usr/local/sbin/nori-vm
modprobe kvm_intel
test -c /dev/kvm
cd /opt/nori-firecracker
if ! [ -x /usr/local/bin/firecracker ]; then
  curl -fL --connect-timeout 10 --max-time 180 -o firecracker.tgz https://github.com/firecracker-microvm/firecracker/releases/download/v1.17.0/firecracker-v1.17.0-x86_64.tgz
  printf '%s  %s\n' 06094a1108ae9e82aa4c23a775aa92758f53f1175d422270d9d6162cb9ade558 firecracker.tgz | sha256sum --check
  tar -xzf firecracker.tgz release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64
  install -m 755 release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64 /usr/local/bin/firecracker
fi
if ! [ -f vmlinux ]; then
  curl -fL --connect-timeout 10 --max-time 180 -o vmlinux https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20261023-dd7c24b0d165-0/x86_64/vmlinux-6.1.188
fi
printf '%s  %s\n' fc7a33d23a629d64db61ddf12789c737c12edb1702a5ce9d5546b95ca21dc6ca vmlinux | sha256sum --check
release=$(attribute computer-release-object)
checksum=$(attribute computer-release-sha256)
[[ "$release" =~ ^[a-z0-9._-]+/releases/computer-agent-[a-f0-9]{64}$ ]]
[[ "$checksum" =~ ^[a-f0-9]{64}$ ]]
[ "${release##*-}" = "$checksum" ]
# The host uses ADC for the download. Only the verified binary enters the guest.
token=$(meta instance/service-accounts/default/token | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')
curl -fLsS --max-time 120 -H "Authorization: Bearer $token" "https://storage.googleapis.com/$release" -o /usr/local/lib/nori-computer/computer-agent.download
unset token
printf '%s  %s\n' "$checksum" /usr/local/lib/nori-computer/computer-agent.download | sha256sum --check
install -m 755 /usr/local/lib/nori-computer/computer-agent.download /usr/local/bin/computer-agent
rm /usr/local/lib/nori-computer/computer-agent.download
# Preserve the deployed attachment name and filesystem identity.
device=/dev/disk/by-id/google-bot-guest-data
test -b "$device"
[ "$(blkid -s TYPE -o value "$device")" = ext4 ]
[ "$(blkid -s LABEL -o value "$device")" = BOT_GUEST_DATA ]
mountpoint -q /srv/nori-guest || mount "$device" /srv/nori-guest
grep -qx debian-bookworm-firecracker-1.17.0-kernel-6.1.188 /srv/nori-guest/READY
(
  if pgrep -x firecracker >/dev/null; then echo 'Refusing binary install with running VMM' >&2; exit 1; fi
  image=/srv/nori-guest/rootfs.ext4
  test ! -L "$image" && test -s "$image"
  [ -z "$(losetup --associated "$image" --noheadings --output NAME)" ]
  [ "$(blkid -p -s TYPE -o value "$image")" = ext4 ]
  guest_root=$(mktemp -d /mnt/nori-agent.XXXXXX)
  cleanup() { status=$?; trap - EXIT; if mountpoint -q "$guest_root"; then umount "$guest_root" || status=1; fi; rmdir "$guest_root" || status=1; exit "$status"; }
  trap cleanup EXIT
  mount -t ext4 -o loop,nodev,nosuid,noexec "$image" "$guest_root"
  for directory in etc etc/systemd etc/systemd/system etc/systemd/system/multi-user.target.wants usr usr/local usr/local/bin workspace; do
    test ! -L "$guest_root/$directory" && test -d "$guest_root/$directory"
  done
  test ! -L "$guest_root/etc/passwd" && test -f "$guest_root/etc/passwd"
  awk -F: '$1=="bot" { if($3!=1000 || $4!=1000 || $6!="/home/bot") exit 1; found++ } END { if(found!=1) exit 1 }' "$guest_root/etc/passwd"
  # Reuse the existing guest service so upgrades never enable a second RPC listener.
  for file in usr/local/bin/computer-agent etc/systemd/system/bot-computer-agent.service; do
    test ! -L "$guest_root/$file"
    if [ -e "$guest_root/$file" ]; then test -f "$guest_root/$file"; fi
  done
  test ! -L "$guest_root/workspace/tasks"
  install -d -o 1000 -g 1000 -m 700 "$guest_root/workspace/tasks"
  install -m 755 /usr/local/bin/computer-agent "$guest_root/usr/local/bin/computer-agent"
  # Tasks reach for `python`, and Debian ships only python3. A file the guest already put there stays.
  python_link="$guest_root/usr/local/bin/python"
  if [ ! -e "$python_link" ] && [ ! -L "$python_link" ]; then ln -s /usr/bin/python3 "$python_link"; fi
  test ! -L "$guest_root/etc/nori-telemetry.env"
  install -m 644 /etc/nori-computer/telemetry.env "$guest_root/etc/nori-telemetry.env"
  cat > "$guest_root/etc/systemd/system/bot-computer-agent.service" <<'SERVICE'
[Unit]
Description=Nori guest computer RPC
After=network-online.target bot-browser.service
Wants=network-online.target
[Service]
User=bot
Group=bot
Environment=HOME=/home/bot
EnvironmentFile=-/etc/nori-telemetry.env
ExecStart=/usr/local/bin/computer-agent --listen 172.30.0.2:8090
NoNewPrivileges=yes
Delegate=yes
KillMode=control-group
TimeoutStopSec=5
Restart=no
[Install]
WantedBy=multi-user.target
SERVICE
  # Older images lack pdftotext, which tasks use to read attached PDFs. This runs inside the guest on its next boot, so
  # host root still runs no guest program. It runs beside the agent, because the host gives up on a guest that takes
  # too long to answer. An install cut off by a stop is finished on the next boot, and a failure only leaves it missing.
  test ! -L "$guest_root/etc/systemd/system/nori-guest-packages.service"
  cat > "$guest_root/etc/systemd/system/nori-guest-packages.service" <<'SERVICE'
[Unit]
Description=Nori guest packages
After=network-online.target
Wants=network-online.target
[Service]
Type=oneshot
Environment=DEBIAN_FRONTEND=noninteractive
ExecStart=/bin/sh -c 'command -v pdftotext >/dev/null || { dpkg --configure -a; apt-get update && apt-get install -y --no-install-recommends poppler-utils; }'
TimeoutStartSec=600
[Install]
WantedBy=multi-user.target
SERVICE
  packages_link="$guest_root/etc/systemd/system/multi-user.target.wants/nori-guest-packages.service"
  if [ ! -e "$packages_link" ] && [ ! -L "$packages_link" ]; then ln -s /etc/systemd/system/nori-guest-packages.service "$packages_link"; fi
  link="$guest_root/etc/systemd/system/multi-user.target.wants/bot-computer-agent.service"
  if [ -e "$link" ] || [ -L "$link" ]; then
    test -L "$link"
    [ "$(readlink "$link")" = /etc/systemd/system/bot-computer-agent.service ]
  else
    ln -s /etc/systemd/system/bot-computer-agent.service "$link"
  fi
  sync -f "$guest_root/usr/local/bin/computer-agent"
  sync -f "$guest_root/etc/systemd/system/bot-computer-agent.service"
)
# Guest cannot initiate connections to the host, metadata, or private networks.
ip link show tap-nori >/dev/null 2>&1 || ip tuntap add tap-nori mode tap
ip addr replace 172.30.0.1/30 dev tap-nori
ip link set tap-nori up
sysctl -w net.ipv4.ip_forward=1
sysctl -w net.ipv6.conf.tap-nori.disable_ipv6=1
external=$(ip -4 route show default | awk 'NR==1 {print $5}')
test -n "$external"
iptables -N NORI_GUEST_EGRESS 2>/dev/null || iptables -F NORI_GUEST_EGRESS
for range in 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 192.0.0.0/24 198.18.0.0/15 224.0.0.0/4 240.0.0.0/4; do iptables -A NORI_GUEST_EGRESS -d "$range" -j REJECT; done
iptables -A NORI_GUEST_EGRESS -o "$external" -j ACCEPT
iptables -A NORI_GUEST_EGRESS -j REJECT
iptables -C FORWARD -i tap-nori -j NORI_GUEST_EGRESS 2>/dev/null || iptables -I FORWARD 1 -i tap-nori -j NORI_GUEST_EGRESS
iptables -C FORWARD -o tap-nori -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || iptables -I FORWARD 1 -o tap-nori -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -C INPUT -i tap-nori -j REJECT 2>/dev/null || iptables -I INPUT 1 -i tap-nori -j REJECT
iptables -C INPUT -i tap-nori -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || iptables -I INPUT 1 -i tap-nori -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -t nat -C POSTROUTING -s 172.30.0.0/30 -o "$external" -j MASQUERADE 2>/dev/null || iptables -t nat -A POSTROUTING -s 172.30.0.0/30 -o "$external" -j MASQUERADE
cat > /etc/nori-computer/vm.json <<'CONFIG'
{"boot-source":{"kernel_image_path":"/opt/nori-firecracker/vmlinux","boot_args":"console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw rootfstype=ext4 ipv6.disable=1 net.ifnames=0"},"drives":[{"drive_id":"rootfs","path_on_host":"/srv/nori-guest/rootfs.ext4","is_root_device":true,"is_read_only":false,"cache_type":"Writeback"}],"network-interfaces":[{"iface_id":"eth0","guest_mac":"06:00:AC:10:00:02","host_dev_name":"tap-nori"}],"machine-config":{"vcpu_count":2,"mem_size_mib":2048}}
CONFIG
cat > /usr/local/lib/nori-computer/run-vm.sh <<'RUN'
#!/bin/bash
set -euo pipefail
mkdir -p /run/nori-vm
rm -f /run/nori-vm/api.socket
[ -p /run/nori-vm/console-input ] || mkfifo /run/nori-vm/console-input
exec 3<>/run/nori-vm/console-input
exec /usr/local/bin/firecracker --api-sock /run/nori-vm/api.socket --config-file /etc/nori-computer/vm.json <&3
RUN
chmod 755 /usr/local/lib/nori-computer/run-vm.sh
cat > /etc/systemd/system/nori-firecracker.service <<'SERVICE'
[Unit]
Description=Persistent Nori Firecracker guest
After=network.target
[Service]
ExecStart=/usr/local/lib/nori-computer/run-vm.sh
ExecStop=/usr/local/sbin/nori-vm request-stop
TimeoutStopSec=55
StandardOutput=append:/var/log/nori-vm-console.log
StandardError=inherit
Restart=no
SERVICE
# This helper is root-owned; the unprivileged proxy can only request its own
# idle exit. Ordinary errors or operator signals never trigger host shutdown.
cat > /usr/local/sbin/nori-computer-idle-stop <<'STOP'
#!/bin/bash
set -euo pipefail
[ "${EXIT_CODE:-}" = exited ] && [ "${EXIT_STATUS:-}" = 75 ] || exit 0
if ! /usr/local/sbin/nori-vm stop; then
  echo 'Idle stop incomplete: graceful guest shutdown/flush was not verified; host remains on. Reconcile the guest/filesystem, then pause explicitly. No forced poweroff was attempted.' >&2
  exit 1
fi
systemctl poweroff
STOP
chmod 755 /usr/local/sbin/nori-computer-idle-stop
primary=$(meta instance/network-interfaces/0/ip)
cat > /etc/systemd/system/nori-computer-proxy.service <<SERVICE
[Unit]
Description=IAP computer RPC proxy
After=nori-firecracker.service
[Service]
User=nobody
EnvironmentFile=-/etc/nori-computer/telemetry.env
ExecStart=/usr/local/bin/computer-agent --proxy --listen $primary:8090 --idle-timeout 15m
SuccessExitStatus=75
ExecStopPost=+/usr/local/sbin/nori-computer-idle-stop
TimeoutStopSec=75
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
Restart=on-failure
RestartSec=5
RestartPreventExitStatus=75
SERVICE
systemctl daemon-reload
/usr/local/sbin/nori-vm start
systemctl start nori-computer-proxy
for attempt in $(seq 1 60); do
  if curl -fsS --max-time 2 http://172.30.0.2:8090/healthz >/dev/null; then echo NORI_COMPUTER_READY; exit 0; fi
  sleep 2
done
exit 1
