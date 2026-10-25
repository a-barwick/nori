#!/bin/bash
# Disposable trusted-code boot/persistence experiment; no guest network or jailer.
set -Eeuo pipefail
exec > >(tee -a /var/log/nori-firecracker-smoke.log /dev/ttyS0) 2>&1
trap 'status=$?; echo "NORI_FC_FAIL status=$status line=$LINENO"; exit "$status"' ERR
mkdir /var/lib/nori-firecracker-smoke-started # Refuse a second host-startup run.
echo NORI_FC_BEGIN
test "$(uname -m)" = x86_64
modprobe kvm_intel
test -c /dev/kvm
test -r /dev/kvm
test -w /dev/kvm
ls -l /dev/kvm
echo NORI_FC_HOST_KVM_OK
export DEBIAN_FRONTEND=noninteractive
timeout 180 apt-get update
timeout 240 apt-get -o DPkg::Lock::Timeout=120 install -y --no-install-recommends busybox-static ca-certificates curl e2fsprogs file
mkdir -p /opt/nori-firecracker
cd /opt/nori-firecracker

# Official tagged release + dated official CI kernel from the tagged guide.
# Release checksum is published upstream; kernel checksum was calculated from
# the pinned upstream HTTPS artifact during preparation, not supplied upstream.
curl -fL --connect-timeout 10 --max-time 180 -o firecracker.tgz \
  https://github.com/firecracker-microvm/firecracker/releases/download/v1.17.0/firecracker-v1.17.0-x86_64.tgz
printf '%s  %s\n' 06094a1108ae9e82aa4c23a775aa92758f53f1175d422270d9d6162cb9ade558 firecracker.tgz | sha256sum --check
curl -fL --connect-timeout 10 --max-time 180 -o vmlinux \
  https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20261023-dd7c24b0d165-0/x86_64/vmlinux-6.1.188
printf '%s  %s\n' fc7a33d23a629d64db61ddf12789c737c12edb1702a5ce9d5546b95ca21dc6ca vmlinux | sha256sum --check
tar -xzf firecracker.tgz release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64
cp release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64 ./firecracker
./firecracker --version
dpkg-query -W busybox-static
file /bin/busybox | grep -E 'statically linked|static-pie linked'
sha256sum /bin/busybox

# Tiny distro-supplied static userspace, rather than a prebuilt third-party OS.
mkdir -p rootfs/{bin,dev,proc,sys,tmp}
cp /bin/busybox rootfs/bin/busybox
ln -s busybox rootfs/bin/sh
mknod -m 600 rootfs/dev/console c 5 1
mknod -m 666 rootfs/dev/null c 1 3
cat > rootfs/init <<'GUEST'
#!/bin/sh
set -eu
# The pinned kernel has CONFIG_DEVTMPFS_MOUNT=y and mounts /dev itself.
/bin/busybox mount -t proc proc /proc
/bin/busybox mount -t sysfs sysfs /sys
echo NORI_FC_GUEST_UNAME
/bin/busybox uname -a
if [ -f /persistent-marker ]; then
  [ "$(/bin/busybox cat /persistent-marker)" = nori-firecracker-persistence-v1 ]
  echo NORI_FC_GUEST_RETAINED
else
  echo nori-firecracker-persistence-v1 > /persistent-marker
  echo NORI_FC_GUEST_WROTE
fi
/bin/busybox sync
/bin/busybox mount -o remount,ro /
echo NORI_FC_GUEST_SHUTDOWN
exec /bin/busybox reboot -f
GUEST
chmod 755 rootfs/init
truncate -s 64M rootfs.ext4
mkfs.ext4 -F -d rootfs rootfs.ext4
cat > vm.json <<'CONFIG'
{
  "boot-source": {
    "kernel_image_path": "/opt/nori-firecracker/vmlinux",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw rootfstype=ext4 init=/init"
  },
  "drives": [{"drive_id": "rootfs", "path_on_host": "/opt/nori-firecracker/rootfs.ext4", "is_root_device": true, "is_read_only": false}],
  "machine-config": {"vcpu_count": 1, "mem_size_mib": 128}
}
CONFIG
# A FIFO keeps stdin pollable for the UART; /dev/null cannot be epoll-monitored.
mkfifo guest-input
exec 3<>guest-input
for boot in 1 2; do
  echo "NORI_FC_HOST_BOOT_$boot"
  timeout 60 ./firecracker --no-api --config-file vm.json <&3 2>&1 | tr -d '\r' | tee "guest-$boot.log"
  grep -qx NORI_FC_GUEST_SHUTDOWN "guest-$boot.log"
  if [ "$boot" = 1 ]; then
    grep -qx NORI_FC_GUEST_WROTE guest-1.log
  else
    grep -qx NORI_FC_GUEST_RETAINED guest-2.log
  fi
  echo "NORI_FC_HOST_STOPPED_$boot"
done
echo NORI_FC_SUCCESS
