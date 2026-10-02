#!/usr/bin/env bash
set -euo pipefail

# This is an operator-invoked proof of guest command execution. It prepares a
# disposable copy of the Alpine hello rootfs, injects a read-only repository
# fixture and runs its verifier as PID 1. Nothing from a product checkout is
# mounted or copied into the guest.
BASE="${1:-/tmp/itbem-firecracker}"
RUNNER="$(cd "$(dirname "$0")" && pwd)/test-firecracker-roundtrip.sh"

[[ "$(id -u)" != "0" ]] || { echo "run this proof as a non-root KVM-enabled user" >&2; exit 2; }
test -f "$BASE/hello-vmlinux.bin" || { echo "missing kernel artifact" >&2; exit 2; }
test -f "$BASE/hello-rootfs.ext4" || { echo "missing rootfs artifact" >&2; exit 2; }
test -f "$BASE/release-v1.7.0-x86_64/firecracker-v1.7.0-x86_64" || { echo "missing Firecracker artifact" >&2; exit 2; }
# Never delete or reuse an operator-supplied directory. Keep this invocation's
# private copy and logs for diagnosis, including after a failed boot.
umask 077
if [[ -n "${2:-}" ]]; then
  WORK="$2"
  [[ "$WORK" =~ ^/[A-Za-z0-9_./-]+$ ]] || { echo "proof directory must be an absolute path without special characters" >&2; exit 2; }
  mkdir -- "$WORK" || { echo "proof directory must be new" >&2; exit 2; }
else
  WORK="$(mktemp -d /tmp/itbem-firecracker-repository-command.XXXXXXXX)"
fi
PREP="$WORK/preparation"
ROOTFS="$WORK/hello-rootfs.ext4"
mkdir "$PREP"
cp "$BASE/hello-rootfs.ext4" "$ROOTFS"

printf '%s\n' 'bounded repository fixture: no host path is mounted' > "$PREP/repository.txt"
expected="$(sha256sum "$PREP/repository.txt" | awk '{print $1}')"
cat > "$PREP/itbem-init" <<EOF
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
# debugfs edits only the disposable image, without mounting a host filesystem.
# Its exit status alone does not reliably signal an invalid command, so verify
# both inserted files byte-for-byte before booting.
debugfs -w -R 'mkdir /itbem-fixture' "$ROOTFS" >/dev/null 2>&1
debugfs -w -R 'rm /itbem-fixture/repository.txt' "$ROOTFS" >/dev/null 2>&1
debugfs -w -R 'rm /sbin/itbem-init' "$ROOTFS" >/dev/null 2>&1
debugfs -w -R "write $PREP/repository.txt /itbem-fixture/repository.txt" "$ROOTFS" >/dev/null 2>&1
debugfs -w -R "write $PREP/itbem-init /sbin/itbem-init" "$ROOTFS" >/dev/null 2>&1
debugfs -w -R 'set_inode_field /sbin/itbem-init mode 0100755' "$ROOTFS" >/dev/null 2>&1
debugfs -R 'cat /itbem-fixture/repository.txt' "$ROOTFS" 2>/dev/null | cmp - "$PREP/repository.txt"
debugfs -R 'cat /sbin/itbem-init' "$ROOTFS" 2>/dev/null | cmp - "$PREP/itbem-init"

cp "$BASE/hello-vmlinux.bin" "$WORK/hello-vmlinux.bin"
mkdir "$WORK/release-v1.7.0-x86_64"
cp "$BASE/release-v1.7.0-x86_64/firecracker-v1.7.0-x86_64" "$WORK/release-v1.7.0-x86_64/"

env FIRECRACKER_BOOT_ARGS='console=ttyS0 reboot=k panic=1 pci=off init=/sbin/itbem-init' \
  bash "$RUNNER" "$WORK"

if grep -qx 'ITBEM_REPOSITORY_COMMAND_PASS' "$WORK/itbem-firecracker.stdout"; then
  echo "Firecracker repository command: PASS"
  echo "guest_command=pass"
else
  echo "Firecracker repository command: FAIL" >&2
  exit 5
fi
