#!/usr/bin/env python3
"""Operator-owned, bounded local Firecracker task supervisor.

The local WSL kernel fixture does not expose a virtio-vsock transport, so this
proof uses the guest's serial console for a one-shot command receipt. The
control-plane attestation explicitly names that transport; production VM
profiles should use the same lifecycle contract with a reviewed vsock guest
agent before enabling hostile repository execution.
"""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
import time

ROOT = pathlib.Path("/tmp/itbem-firecracker")
FIRECRACKER = ROOT / "release-v1.7.0-x86_64" / "firecracker-v1.7.0-x86_64"
KERNEL = ROOT / "hello-vmlinux.bin"
ROOTFS = ROOT / "hello-rootfs.ext4"
ATTESTATION_ROOT = pathlib.Path("/tmp/itbem-firecracker-attestations")
MAX_OUTPUT = 12000


def emit_failure(message: str, request: dict | None = None) -> None:
    request = request or {}
    print(json.dumps({
        "protocol_version": 1, "operation": request.get("operation", "execute"),
        "lease_id": request.get("lease_id", ""), "ok": False, "exit_code": 1,
        "error": message, "task_id": request.get("task_id", ""),
        "workspace_id": request.get("workspace_id", ""),
        "worktree_digest": request.get("worktree_digest", ""), "attestation": {},
        "lifecycle": {"created": False, "worktree_bound": False,
                       "guest_command_executed": False, "destroyed": True,
                       "attestation_persisted": False},
    }, separators=(",", ":")))
    raise SystemExit(1)


def api_put(api_socket: pathlib.Path, endpoint: str, payload: dict) -> None:
    result = subprocess.run([
        "curl", "--silent", "--show-error", "--fail-with-body", "--unix-socket",
        str(api_socket), "-X", "PUT", "-H", "Content-Type: application/json",
        f"http://localhost{endpoint}", "--data-binary",
        json.dumps(payload, separators=(",", ":")),
    ], capture_output=True, text=True, check=False)
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip() or "Firecracker API request failed")


def quote(value: str) -> str:
    return "'" + value.replace("'", "'\\''") + "'"


def main() -> None:
    try:
        request = json.load(sys.stdin)
    except Exception as exc:
        emit_failure(f"invalid supervisor request: {exc}")
    if not isinstance(request, dict):
        emit_failure("supervisor request must be an object")
    if request.get("protocol_version") != 1 or request.get("operation") != "execute":
        emit_failure("unsupported supervisor protocol or operation", request)
    required = ("lease_id", "task_id", "workspace_id", "workspace_path", "worktree_digest")
    if any(not str(request.get(field, "")).strip() for field in required):
        emit_failure("supervisor request is missing task-scoped binding", request)
    workspace_path = pathlib.Path(str(request["workspace_path"])).absolute()
    digest = "sha256:" + hashlib.sha256(str(workspace_path).strip().encode()).hexdigest()
    if request["worktree_digest"] != digest:
        emit_failure("worktree digest does not match the bound workspace path", request)

    command = str(request.get("command", ""))
    args = request.get("args") or []
    # The fixture contains BusyBox echo. This narrow allow-list is deliberate:
    # it proves a real guest command without turning this local adapter into a
    # general host-controlled shell.
    if command not in ("echo", "/bin/echo") or not isinstance(args, list) or len(args) > 8:
        emit_failure("local Firecracker proof only permits echo with at most eight arguments", request)
    if any(not isinstance(arg, str) or len(arg) > 256 or "\x00" in arg or "\n" in arg or "\r" in arg for arg in args):
        emit_failure("guest argument is invalid or exceeds the local proof bound", request)
    for artifact in (FIRECRACKER, KERNEL, ROOTFS):
        if not artifact.is_file():
            emit_failure(f"missing Firecracker artifact: {artifact}", request)
    if not os.access("/dev/kvm", os.R_OK | os.W_OK):
        emit_failure("current WSL user cannot access /dev/kvm", request)

    workdir = pathlib.Path(tempfile.mkdtemp(prefix="itbem-supervisor-", dir="/tmp"))
    api_socket = workdir / "firecracker.sock"
    rootfs = workdir / "rootfs.ext4"
    stdout_path = workdir / "firecracker.stdout"
    stderr_path = workdir / "firecracker.stderr"
    shutil.copy2(ROOTFS, rootfs)
    command_line = " ".join(quote(arg) for arg in args)
    init = """#!/bin/sh
set -eu
printf 'ITBEM_GUEST_COMMAND_BEGIN\\n' > /dev/console
status=0
/bin/echo {args} > /tmp/itbem-command-output 2>&1 || status=$?
cat /tmp/itbem-command-output > /dev/console
printf 'ITBEM_GUEST_EXIT:%s\\n' "$status" > /dev/console
if [ "$status" -eq 0 ]; then printf 'ITBEM_GUEST_COMMAND_OK\\n' > /dev/console; else printf 'ITBEM_GUEST_COMMAND_FAIL\\n' > /dev/console; fi
sync
/sbin/poweroff -f || true
while :; do sleep 1; done
""".format(args=command_line)
    init_path = workdir / "itbem-task-init"
    init_path.write_text(init, encoding="utf-8")
    subprocess.run(["debugfs", "-w", "-R", f"write {init_path} /sbin/itbem-task-init", str(rootfs)], capture_output=True, check=True)
    subprocess.run(["debugfs", "-w", "-R", "set_inode_field /sbin/itbem-task-init mode 0100755", str(rootfs)], capture_output=True, check=True)

    lifecycle = {"created": False, "worktree_bound": True, "guest_command_executed": False, "destroyed": False, "attestation_persisted": False}
    process = None
    captured = ""
    guest_exit = 1
    try:
        process = subprocess.Popen([str(FIRECRACKER), "--api-sock", str(api_socket), "--no-seccomp", "--level", "Warning"], stdout=stdout_path.open("w"), stderr=stderr_path.open("w"), text=True)
        for _ in range(100):
            if api_socket.exists():
                break
            time.sleep(0.1)
        if not api_socket.exists():
            raise RuntimeError("Firecracker API socket did not appear")
        lifecycle["created"] = True
        api_put(api_socket, "/boot-source", {"kernel_image_path": str(KERNEL), "boot_args": "console=ttyS0 reboot=k panic=1 pci=off init=/sbin/itbem-task-init"})
        api_put(api_socket, "/drives/rootfs", {"drive_id": "rootfs", "path_on_host": str(rootfs), "is_root_device": True, "is_read_only": False})
        api_put(api_socket, "/machine-config", {"vcpu_count": 1, "mem_size_mib": 128, "smt": False})
        api_put(api_socket, "/actions", {"action_type": "InstanceStart"})
        deadline = time.time() + 12
        while time.time() < deadline:
            if stdout_path.exists():
                captured = stdout_path.read_text(errors="replace")
            if "ITBEM_GUEST_COMMAND_OK" in captured or "ITBEM_GUEST_COMMAND_FAIL" in captured:
                break
            time.sleep(0.1)
        lifecycle["guest_command_executed"] = "ITBEM_GUEST_COMMAND_BEGIN" in captured and "ITBEM_GUEST_EXIT:" in captured
        match = re.search(r"ITBEM_GUEST_EXIT:(\d+)", captured)
        guest_exit = int(match.group(1)) if match else 1
    except Exception as exc:
        captured = (captured + "\n" + str(exc)).strip()
    finally:
        if process is not None:
            try:
                process.terminate()
                process.wait(timeout=3)
            except Exception:
                process.kill()
                process.wait(timeout=3)
            lifecycle["destroyed"] = process.poll() is not None
        if stdout_path.exists():
            captured = stdout_path.read_text(errors="replace")
        if stderr_path.exists():
            captured += "\n" + stderr_path.read_text(errors="replace")
        shutil.rmtree(workdir, ignore_errors=True)

    output = captured[-MAX_OUTPUT:]
    attestation = {"runtime": "firecracker", "runtime_version": "1.7.0", "transport": "serial_console", "evidence_scope": "local_task_guest_command", "guest_command_verified": bool(lifecycle["guest_command_executed"]), "evidence_digest": "sha256:" + hashlib.sha256(output.encode()).hexdigest()}
    ATTESTATION_ROOT.mkdir(parents=True, exist_ok=True)
    persisted = ATTESTATION_ROOT / (re.sub(r"[^A-Za-z0-9_.-]", "_", str(request["lease_id"])) + ".json")
    lifecycle["attestation_persisted"] = True
    persisted.write_text(json.dumps({"request": {"task_id": request["task_id"], "workspace_id": request["workspace_id"], "worktree_digest": request["worktree_digest"]}, "attestation": attestation, "lifecycle": lifecycle}, sort_keys=True) + "\n", encoding="utf-8")
    lifecycle["attestation_persisted"] = persisted.is_file() and persisted.stat().st_size > 0
    ok = guest_exit == 0 and all(lifecycle.values())
    print(json.dumps({"protocol_version": 1, "operation": "execute", "lease_id": request["lease_id"], "ok": ok, "exit_code": guest_exit, "stdout": output, "task_id": request["task_id"], "workspace_id": request["workspace_id"], "worktree_digest": request["worktree_digest"], "attestation": attestation, "lifecycle": lifecycle}, separators=(",", ":")))
    raise SystemExit(0 if ok else 1)


if __name__ == "__main__":
    main()
