#!/usr/bin/env bash
set -euo pipefail

# End-to-end local proof: a host-side controller sends one allowlisted command
# through Firecracker virtio-vsock and receives a JSON response from a guest
# agent. The image and fixture are disposable; no product checkout is mounted.
BASE="${1:-/tmp/itbem-firecracker}"
AGENT="${2:-$(cd "$(dirname "$0")/../.local" && pwd)/firecracker-guest-agent}"
WORK="${3:-/tmp/itbem-firecracker-vsock}"
MNT="$WORK/mnt"
ROOTFS="$WORK/rootfs.ext4"
API="$WORK/api.sock"
VSOCK="$WORK/vsock.sock"
OUT="$WORK/firecracker.stdout"
ERR="$WORK/firecracker.stderr"

[[ "$(id -u)" == "0" ]] || { echo "prepare/run as root so the disposable rootfs can be mounted" >&2; exit 2; }
test -f "$BASE/hello-vmlinux.bin" || { echo "missing kernel artifact" >&2; exit 2; }
test -f "$BASE/hello-rootfs.ext4" || { echo "missing rootfs artifact" >&2; exit 2; }
test -f "$AGENT" || { echo "missing guest agent: $AGENT" >&2; exit 2; }
rm -rf "$WORK"
mkdir -p "$MNT"
chmod 0777 "$WORK"
cp "$BASE/hello-rootfs.ext4" "$ROOTFS"
mount -o loop,rw "$ROOTFS" "$MNT"
cleanup_mount() { mountpoint -q "$MNT" && umount "$MNT" || true; }
trap cleanup_mount EXIT

mkdir -p "$MNT/itbem-fixture"
printf '%s\n' 'vsock fixture: bounded guest command' > "$MNT/itbem-fixture/repository.txt"
install -m 0755 "$AGENT" "$MNT/sbin/itbem-guest-agent"
cat > "$MNT/sbin/itbem-init" <<'EOF'
#!/bin/sh
set -eu
/sbin/itbem-guest-agent >/dev/console 2>&1 &
echo ITBEM_VSOCK_AGENT_READY >/dev/console
exec /bin/sh
EOF
chmod 0755 "$MNT/sbin/itbem-init"
umount "$MNT"
trap - EXIT

rm -f "$API" "$VSOCK" "$OUT" "$ERR"
setpriv --reuid=1000 --regid=1000 --groups=1000,993 \
  "$BASE/release-v1.7.0-x86_64/firecracker-v1.7.0-x86_64" \
  --api-sock "$API" --no-seccomp --level Warning >"$OUT" 2>"$ERR" &
FC_PID=$!
cleanup() { kill "$FC_PID" 2>/dev/null || true; wait "$FC_PID" 2>/dev/null || true; rm -f "$API" "$VSOCK"; }
trap cleanup EXIT
for _ in $(seq 1 100); do [[ -S "$API" ]] && break; sleep 0.1; done
test -S "$API" || { echo "Firecracker API socket did not appear" >&2; exit 3; }

put() {
  curl --silent --show-error --fail-with-body --unix-socket "$API" -X PUT \
    -H 'Content-Type: application/json' "http://localhost$1" --data-binary @-
}
put /boot-source <<JSON
{"kernel_image_path":"$BASE/hello-vmlinux.bin","boot_args":"console=ttyS0 reboot=k panic=1 pci=off init=/sbin/itbem-init"}
JSON
put /drives/rootfs <<JSON
{"drive_id":"rootfs","path_on_host":"$ROOTFS","is_root_device":true,"is_read_only":true}
JSON
put /machine-config <<JSON
{"vcpu_count":1,"mem_size_mib":128,"smt":false}
JSON
put /vsock <<JSON
{"guest_cid":3,"uds_path":"$VSOCK"}
JSON
put /actions <<JSON
{"action_type":"InstanceStart"}
JSON

for _ in $(seq 1 120); do [[ -S "$VSOCK" ]] && break; sleep 0.1; done
test -S "$VSOCK" || { echo "Firecracker vsock socket did not appear" >&2; cat "$OUT" >&2; exit 4; }
for _ in $(seq 1 120); do
  grep -q 'ITBEM_VSOCK_LISTENING' "$OUT" 2>/dev/null && break
  sleep 0.1
done
grep -q 'ITBEM_VSOCK_LISTENING' "$OUT" || { echo "guest vsock agent did not become ready" >&2; cat "$OUT" >&2; exit 4; }

VSOCK_PATH="$VSOCK" python3 - <<'PY'
import json, os, socket, time
path = os.environ["VSOCK_PATH"]
sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
sock.settimeout(10)
sock.connect(path)
sock.sendall(b"CONNECT 52\n")
ack = sock.recv(1024)
print("vsock_ack=" + repr(ack))
if not ack.startswith(b"OK "):
    raise SystemExit(f"unexpected vsock handshake: {ack!r}")
sock.sendall(b'{"command":"/bin/cat","args":["/itbem-fixture/repository.txt"]}\n')
payload = b""
while not payload.endswith(b"\n"):
    payload += sock.recv(4096)
result = json.loads(payload)
if not result.get("ok") or "vsock fixture" not in result.get("stdout", ""):
    raise SystemExit(f"guest command failed: {result!r}")
print("guest_vsock_command=pass")
print("guest_response=" + json.dumps(result, sort_keys=True))
PY
if [[ -n "${ITBEM_ATTESTATION_OUT:-}" ]]; then
  digest="$(printf '%s' "$WORK/firecracker.stdout" | sha256sum | awk '{print $1}')"
  printf '%s\n' "{\"runtime\":\"firecracker\",\"runtime_version\":\"1.7.0\",\"transport\":\"virtio_vsock\",\"evidence_scope\":\"synthetic_guest_fixture\",\"guest_command_verified\":true,\"evidence_digest\":\"sha256:$digest\"}" > "$ITBEM_ATTESTATION_OUT"
  chmod 0644 "$ITBEM_ATTESTATION_OUT"
fi
echo "Firecracker virtio-vsock round-trip: PASS"
