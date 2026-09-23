#!/usr/bin/env bash
set -euo pipefail

# This is an operator-invoked proof of guest command execution. It prepares a
# disposable copy of the Alpine hello rootfs, injects a read-only repository
# fixture and runs its verifier as PID 1. Nothing from a product checkout is
# mounted or copied into the guest.
BASE="${1:-/tmp/itbem-firecracker}"
WORK="${2:-/tmp/itbem-firecracker-repository-command}"
PREP="$WORK/mnt"
ROOTFS="$WORK/hello-rootfs.ext4"
RUNNER="$(cd "$(dirname "$0")" && pwd)/test-firecracker-roundtrip.sh"

[[ "$(id -u)" == "0" ]] || { echo "run this preparation step as root" >&2; exit 2; }
test -f "$BASE/hello-vmlinux.bin" || { echo "missing kernel artifact" >&2; exit 2; }
test -f "$BASE/hello-rootfs.ext4" || { echo "missing rootfs artifact" >&2; exit 2; }
rm -rf "$WORK"
mkdir -p "$PREP"
cp "$BASE/hello-rootfs.ext4" "$ROOTFS"
mount -o loop,rw "$ROOTFS" "$PREP"
cleanup_mount() { mountpoint -q "$PREP" && umount "$PREP" || true; }
trap cleanup_mount EXIT

mkdir -p "$PREP/itbem-fixture"
printf '%s\n' 'bounded repository fixture: no host path is mounted' > "$PREP/itbem-fixture/repository.txt"
expected="$(sha256sum "$PREP/itbem-fixture/repository.txt" | awk '{print $1}')"
cat > "$PREP/sbin/itbem-init" <<EOF
#!/bin/sh
set -eu
actual=\$(sha256sum /itbem-fixture/repository.txt | awk '{print \$1}')
if [ "\$actual" = "$expected" ]; then
  echo "ITBEM_REPOSITORY_COMMAND_PASS" > /dev/console
else
  echo "ITBEM_REPOSITORY_COMMAND_FAIL" > /dev/console
fi
exec /bin/sh
EOF
chmod 0755 "$PREP/sbin/itbem-init"
umount "$PREP"
trap - EXIT

cp "$BASE/hello-vmlinux.bin" "$WORK/hello-vmlinux.bin"
cp "$ROOTFS" "$BASE/hello-rootfs.ext4"
chmod 0644 "$BASE/hello-rootfs.ext4" "$BASE/hello-vmlinux.bin"

setpriv --reuid=1000 --regid=1000 --groups=1000,993 \
  env FIRECRACKER_BOOT_ARGS='console=ttyS0 reboot=k panic=1 pci=off init=/sbin/itbem-init' \
  bash "$RUNNER" "$BASE"

if grep -q 'ITBEM_REPOSITORY_COMMAND_PASS' "$BASE/itbem-firecracker.stdout"; then
  echo "Firecracker repository command: PASS"
  echo "guest_command=pass"
else
  echo "Firecracker repository command: FAIL" >&2
  exit 5
fi
