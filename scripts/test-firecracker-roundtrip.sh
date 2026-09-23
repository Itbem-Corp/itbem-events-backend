#!/usr/bin/env bash
set -euo pipefail

# Operator-invoked local proof only. This script never changes the worker
# registry or labels Docker as a microVM; it proves that the WSL host can boot
# an isolated Firecracker guest with the supplied kernel/rootfs artifacts.
ROOT="${1:-/tmp/itbem-firecracker}"
FIRECRACKER="$ROOT/release-v1.7.0-x86_64/firecracker-v1.7.0-x86_64"
KERNEL="$ROOT/hello-vmlinux.bin"
ROOTFS="$ROOT/hello-rootfs.ext4"
SOCK="$ROOT/itbem-firecracker.sock"
LOG="$ROOT/itbem-firecracker.log"
BOOT_ARGS="${FIRECRACKER_BOOT_ARGS:-console=ttyS0 reboot=k panic=1 pci=off}"

for path in "$FIRECRACKER" "$KERNEL" "$ROOTFS"; do
  test -f "$path" || { echo "missing artifact: $path" >&2; exit 2; }
done
if [[ ! -e /dev/kvm ]]; then
  echo "KVM is unavailable: /dev/kvm does not exist in this WSL instance" >&2
  echo "kvm_dependency=missing_device" >&2
  exit 3
fi
if [[ ! -r /dev/kvm || ! -w /dev/kvm ]]; then
  echo "KVM is present but inaccessible to the current user" >&2
  echo "kvm_dependency=permission_denied" >&2
  stat -c 'kvm_device=%A %U:%G %a %n' /dev/kvm >&2 || true
  echo "current_identity=$(id)" >&2
  echo "required_action=grant the invoking WSL user read/write access to /dev/kvm (usually membership in the kvm group), then restart the shell/WSL session" >&2
  exit 3
fi

rm -f "$SOCK" "$LOG" "$ROOT/itbem-firecracker.stdout" "$ROOT/itbem-firecracker.stderr"
cleanup() {
  if [[ -n "${FC_PID:-}" ]]; then kill "$FC_PID" 2>/dev/null || true; wait "$FC_PID" 2>/dev/null || true; fi
  rm -f "$SOCK"
}
trap cleanup EXIT

"$FIRECRACKER" --api-sock "$SOCK" --no-seccomp --level Warning \
  >"$ROOT/itbem-firecracker.stdout" 2>"$ROOT/itbem-firecracker.stderr" &
FC_PID=$!
for _ in $(seq 1 100); do
  [[ -S "$SOCK" ]] && break
  sleep 0.1
done
test -S "$SOCK" || { echo "Firecracker API socket did not appear" >&2; exit 4; }

put() {
  curl --silent --show-error --fail-with-body --unix-socket "$SOCK" -X PUT \
    -H 'Content-Type: application/json' "http://localhost$1" --data-binary @-
}

put /boot-source <<JSON
{"kernel_image_path":"$KERNEL","boot_args":"$BOOT_ARGS"}
JSON
put /drives/rootfs <<JSON
{"drive_id":"rootfs","path_on_host":"$ROOTFS","is_root_device":true,"is_read_only":true}
JSON
put /machine-config <<JSON
{"vcpu_count":1,"mem_size_mib":128,"smt":false}
JSON
put /actions <<JSON
{"action_type":"InstanceStart"}
JSON

sleep 8
if grep -Eiq 'VMM started|Started micro|Booting Linux|Booting paravirtualized|Guest-boot|Welcome to Alpine|Linux version' "$LOG" "$ROOT/itbem-firecracker.stdout" "$ROOT/itbem-firecracker.stderr" 2>/dev/null; then
  echo "Firecracker round-trip: PASS"
  echo "runtime=firecracker"
  echo "kvm=available"
  echo "guest=started"
else
  echo "Firecracker round-trip: FAIL" >&2
  cat "$LOG" >&2 || true
  cat "$ROOT/itbem-firecracker.stderr" >&2 || true
  exit 5
fi
