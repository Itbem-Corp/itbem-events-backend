#!/usr/bin/env python3
"""Bounded Firecracker supervisor using a guest-agent over virtio-vsock.

This is an operator-owned local proof adapter. It deliberately accepts one
allowlisted guest command and transfers either a task binding manifest or a
bounded regular-file worktree image into a disposable read-only guest drive.
It never provides a general shell. The control-plane receipt and attestation
are task-scoped.
"""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import re
import resource
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import time

ROOT = pathlib.Path("/tmp/itbem-firecracker")
FIRECRACKER = ROOT / "release-v1.7.0-x86_64" / "firecracker-v1.7.0-x86_64"
JAILER = ROOT / "release-v1.7.0-x86_64" / "jailer-v1.7.0-x86_64"
KERNEL = ROOT / "hello-vmlinux.bin"
ROOTFS = ROOT / "hello-rootfs.ext4"
SCRIPT_ROOT = pathlib.Path(__file__).resolve().parent.parent
GUEST_AGENT = pathlib.Path(os.environ.get("ITBEM_FIRECRACKER_GUEST_AGENT", str(SCRIPT_ROOT / ".local" / "firecracker-guest-agent")))
ATTESTATION_ROOT = pathlib.Path("/tmp/itbem-firecracker-attestations")
MAX_OUTPUT = 12000
MAX_WORKTREE_FILES = 4096
MAX_WORKTREE_BYTES = 64 * 1024 * 1024
MAX_WORKTREE_FILE_BYTES = 8 * 1024 * 1024
PROFILE_LOCAL = "local"
PROFILE_PRODUCTION = "production"


def emit_failure(message: str, request: dict | None = None) -> None:
    request = request or {}
    print(json.dumps({
        "protocol_version": 1,
        "operation": request.get("operation", "execute"),
        "lease_id": request.get("lease_id", ""),
        "ok": False,
        "exit_code": 1,
        "error": message,
        "task_id": request.get("task_id", ""),
        "workspace_id": request.get("workspace_id", ""),
        "worktree_digest": request.get("worktree_digest", ""),
        "attestation": {},
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


def debugfs_write(rootfs: pathlib.Path, source: pathlib.Path, target: str) -> None:
    result = subprocess.run([
        "debugfs", "-w", "-R", f"write {source} {target}", str(rootfs),
    ], capture_output=True, text=True, check=False)
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip() or f"debugfs write failed: {target}")


def debugfs(rootfs: pathlib.Path, command: str) -> None:
    result = subprocess.run(["debugfs", "-w", "-R", command, str(rootfs)], capture_output=True, text=True, check=False)
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip() or f"debugfs command failed: {command}")


def stage_worktree(source: pathlib.Path, destination: pathlib.Path) -> tuple[int, int]:
    """Copy a bounded, regular-file-only worktree into a disposable staging tree."""
    source = source.resolve(strict=True)
    if not source.is_dir():
        raise RuntimeError("workspace_path must resolve to a directory")
    files = 0
    total = 0
    destination.mkdir(parents=True, exist_ok=True)
    for root, dirs, names in os.walk(source, topdown=True, followlinks=False):
        root_path = pathlib.Path(root)
        dirs.sort()
        names.sort()
        # A symlinked directory is never traversed; rejecting it keeps the
        # source boundary explicit instead of silently following an escape.
        for name in list(dirs):
            candidate = root_path / name
            if candidate.is_symlink():
                raise RuntimeError(f"workspace contains a symlinked directory: {candidate}")
        for name in names:
            candidate = root_path / name
            relative = candidate.relative_to(source)
            if len(relative.parts) > 32 or any(part in ("", ".", "..") for part in relative.parts):
                raise RuntimeError(f"workspace file path is invalid: {relative}")
            info = candidate.lstat()
            if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
                raise RuntimeError(f"workspace contains a non-regular file: {relative}")
            if info.st_size > MAX_WORKTREE_FILE_BYTES:
                raise RuntimeError(f"workspace file exceeds the bounded transfer limit: {relative}")
            files += 1
            total += info.st_size
            if files > MAX_WORKTREE_FILES or total > MAX_WORKTREE_BYTES:
                raise RuntimeError("workspace exceeds the bounded transfer limit")
            target = destination / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(candidate, target)
            os.chmod(target, info.st_mode & 0o777)
    return files, total


def build_worktree_image(staging: pathlib.Path, image: pathlib.Path, total_bytes: int) -> None:
    """Create a read-only ext4 disk without mounting or executing workspace data."""
    # mke2fs -d populates an image from a directory and does not require a
    # privileged mount. Keep enough slack for ext4 metadata and fail closed at
    # the same bounded transfer limit used by stage_worktree.
    size_mb = max(32, min(96, (total_bytes // (1024 * 1024)) + 16))
    with image.open("wb") as handle:
        handle.truncate(size_mb * 1024 * 1024)
    result = subprocess.run([
        "mke2fs", "-q", "-t", "ext4", "-F", "-d", str(staging), str(image),
    ], capture_output=True, text=True, check=False)
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip() or "mke2fs worktree image failed")


def apply_production_limits() -> None:
    """Bound the supervisor process before it launches Firecracker."""
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_NOFILE, (4096, 4096))
    resource.setrlimit(resource.RLIMIT_FSIZE, (512 * 1024 * 1024, 512 * 1024 * 1024))


def vsock_command(vsock_path: pathlib.Path, command: str, args: list[str]) -> dict:
    client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    client.settimeout(10)
    try:
        client.connect(str(vsock_path))
        client.sendall(b"CONNECT 52\n")
        ack = client.recv(1024)
        if not ack.startswith(b"OK "):
            raise RuntimeError(f"unexpected vsock handshake: {ack!r}")
        client.sendall((json.dumps({"command": command, "args": args}, separators=(",", ":")) + "\n").encode())
        payload = b""
        while not payload.endswith(b"\n"):
            chunk = client.recv(4096)
            if not chunk:
                raise RuntimeError("guest agent closed the vsock connection before a response")
            payload += chunk
            if len(payload) > MAX_OUTPUT:
                raise RuntimeError("guest agent response exceeded the bounded output limit")
        return json.loads(payload.decode())
    finally:
        client.close()


def main() -> None:
    profile = os.environ.get("ITBEM_FIRECRACKER_PROFILE", PROFILE_LOCAL).strip().lower()
    force_jailer = False
    argv = list(sys.argv[1:])
    if argv:
        index = 0
        while index < len(argv):
            if argv[index] == "--profile" and index + 1 < len(argv):
                profile = argv[index + 1].strip().lower()
                index += 2
            elif argv[index] == "--jailer":
                force_jailer = True
                index += 1
            else:
                emit_failure("supervisor accepts --profile local|production and optional --jailer")
    if profile not in (PROFILE_LOCAL, PROFILE_PRODUCTION):
        emit_failure("unsupported Firecracker profile; choose local or production")
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
    # The guest agent has a narrow allow-list. The binding mode is retained for
    # the protocol smoke test; the worktree mode reads an actual staged file
    # from a read-only second guest drive.
    binding_mode = command == "/bin/cat" and args == ["/itbem-fixture/binding.txt"]
    worktree_mode = (
        command == "/bin/cat" and len(args) == 1 and args[0].startswith("/workspace/")
        and ".." not in pathlib.PurePosixPath(args[0]).parts
    )
    if not binding_mode and not worktree_mode:
        emit_failure("virtio-vsock proof only permits /bin/cat of the binding manifest or /workspace/*", request)
    if request.get("input") or request.get("environment"):
        emit_failure("virtio-vsock proof does not forward host input or environment", request)
    for artifact in (FIRECRACKER, KERNEL, ROOTFS, GUEST_AGENT):
        if not artifact.is_file():
            emit_failure(f"missing Firecracker artifact: {artifact}", request)
    use_jailer = profile == PROFILE_PRODUCTION and (force_jailer or os.environ.get("ITBEM_FIRECRACKER_USE_JAILER", "0") == "1")
    if use_jailer:
        if not JAILER.is_file() or not os.access(JAILER, os.X_OK):
            emit_failure(f"production jailer profile requires an executable jailer: {JAILER}", request)
        if os.geteuid() != 0:
            emit_failure("production jailer profile must be launched by the delegated root-owned worker boundary", request)
    if not os.access("/dev/kvm", os.R_OK | os.W_OK):
        emit_failure("current WSL user cannot access /dev/kvm", request)

    workdir = pathlib.Path(tempfile.mkdtemp(prefix="itbem-supervisor-vsock-", dir="/tmp"))
    api_socket = workdir / "firecracker.sock"
    vsock_socket = workdir / "vsock.sock"
    # Unix domain sockets have a small path limit; keep the jail base short and
    # make the lease id the unique component instead of nesting under the
    # long-lived supervisor temp directory.
    jailer_base = pathlib.Path(os.environ.get("ITBEM_FIRECRACKER_JAILER_BASE", "/tmp/itbem-jailer"))
    jailer_id = re.sub(r"[^A-Za-z0-9-]", "-", str(request["lease_id"]))[:48]
    jailer_uid = int(os.environ.get("ITBEM_FIRECRACKER_JAILER_UID", "1000"))
    jailer_gid = int(os.environ.get("ITBEM_FIRECRACKER_JAILER_GID", "1000"))
    jail_root = jailer_base / FIRECRACKER.name / jailer_id / "root"
    if use_jailer:
        api_socket = jail_root / "run" / "firecracker.sock"
        vsock_socket = jail_root / "run" / "vsock.sock"
    rootfs = workdir / "rootfs.ext4"
    worktree_image = workdir / "worktree.ext4"
    staging = workdir / "worktree"
    stdout_path = workdir / "firecracker.stdout"
    stderr_path = workdir / "firecracker.stderr"
    binding = workdir / "binding.txt"
    init_path = workdir / "itbem-init"
    shutil.copy2(ROOTFS, rootfs)
    if use_jailer:
        jailer_base.mkdir(parents=True, exist_ok=True)
    worktree_files = 0
    worktree_bytes = 0
    if worktree_mode:
        worktree_files, worktree_bytes = stage_worktree(workspace_path, staging)
        build_worktree_image(staging, worktree_image, worktree_bytes)
    binding.write_text(
        f"task_id={request['task_id']}\nworkspace_id={request['workspace_id']}\nworktree_digest={request['worktree_digest']}\n",
        encoding="utf-8",
    )
    init_lines = [
        "#!/bin/sh",
        "set -eu",
    ]
    if worktree_mode:
        init_lines.extend([
            "mount -t ext4 -o ro /dev/vdb /workspace",
        ])
    init_lines.extend([
        "/sbin/itbem-guest-agent >/dev/console 2>&1 &",
        "echo ITBEM_VSOCK_AGENT_READY >/dev/console",
        "exec /bin/sh",
        "",
    ])
    init_path.write_text("\n".join(init_lines), encoding="utf-8")
    # The shared hello rootfs may already contain files from the serial proof.
    # Remove those entries first; debugfs `write` does not replace an existing
    # inode and silently leaving the old init would boot the wrong protocol.
    subprocess.run(["debugfs", "-w", "-R", "rm /sbin/itbem-init", str(rootfs)], capture_output=True, check=False)
    subprocess.run(["debugfs", "-w", "-R", "rm /sbin/itbem-guest-agent", str(rootfs)], capture_output=True, check=False)
    subprocess.run(["debugfs", "-w", "-R", "rm /itbem-fixture/binding.txt", str(rootfs)], capture_output=True, check=False)
    subprocess.run(["debugfs", "-w", "-R", "rm -r /workspace", str(rootfs)], capture_output=True, check=False)
    subprocess.run(["debugfs", "-w", "-R", "mkdir /itbem-fixture", str(rootfs)], capture_output=True, check=False)
    if worktree_mode:
        subprocess.run(["debugfs", "-w", "-R", "mkdir /workspace", str(rootfs)], capture_output=True, check=False)
    debugfs_write(rootfs, binding, "/itbem-fixture/binding.txt")
    debugfs_write(rootfs, GUEST_AGENT, "/sbin/itbem-guest-agent")
    debugfs_write(rootfs, init_path, "/sbin/itbem-init")
    debugfs(rootfs, "set_inode_field /sbin/itbem-guest-agent mode 0100755")
    debugfs(rootfs, "set_inode_field /sbin/itbem-init mode 0100755")

    lifecycle = {"created": False, "worktree_bound": True, "guest_command_executed": False, "destroyed": False, "attestation_persisted": False}
    process = None
    response = {"ok": False, "stdout": "", "stderr": "", "error": ""}
    guest_exit = 1
    captured = ""
    try:
        firecracker_command = [str(FIRECRACKER), "--api-sock", str(api_socket), "--level", "Warning"]
        if profile == PROFILE_LOCAL:
            # The local fixture is intentionally fast and uses no seccomp only
            # for the operator-owned proof. It is never a production profile.
            firecracker_command.append("--no-seccomp")
        else:
            # Omitting --no-seccomp enables Firecracker's built-in seccomp
            # policy. A custom filter is intentionally not accepted here: the
            # release JSON is version-sensitive and must be reviewed against
            # the exact binary before a future profile can opt into it.
            pass
        if use_jailer:
            # Jailer creates the chroot and execs Firecracker as uid/gid 1000.
            # Resources are copied into that root before the first API call;
            # Firecracker never receives a host path outside the jail.
            firecracker_command = [
                str(JAILER), "--id", jailer_id, "--exec-file", str(FIRECRACKER),
                "--uid", str(jailer_uid),
                "--gid", str(jailer_gid),
                "--chroot-base-dir", str(jailer_base), "--new-pid-ns",
                "--cgroup-version", "2",
                "--cgroup", "memory.max=268435456",
                "--cgroup", "pids.max=128",
                "--resource-limit", "no-file=4096",
                "--resource-limit", "fsize=536870912",
                "--", "--api-sock", "/run/firecracker.sock", "--level", "Warning",
            ]
        process = subprocess.Popen(
            firecracker_command,
            stdout=stdout_path.open("w"),
            stderr=stderr_path.open("w"),
            text=True,
            preexec_fn=apply_production_limits if profile == PROFILE_PRODUCTION and not use_jailer else None,
        )
        if use_jailer:
            for _ in range(100):
                if jail_root.is_dir():
                    break
                time.sleep(0.1)
            if not jail_root.is_dir():
                raise RuntimeError("Jailer chroot did not appear")
            (jail_root / "run").mkdir(parents=True, exist_ok=True)
            os.chown(jail_root / "run", jailer_uid, jailer_gid)
            os.chmod(jail_root / "run", 0o755)
            shutil.copy2(KERNEL, jail_root / "vmlinux")
            shutil.copy2(rootfs, jail_root / "rootfs.ext4")
            if worktree_mode:
                shutil.copy2(worktree_image, jail_root / "worktree.ext4")
            for resource_path in (jail_root / "vmlinux", jail_root / "rootfs.ext4", jail_root / "worktree.ext4"):
                if resource_path.exists():
                    os.chmod(resource_path, 0o644)
        for _ in range(100):
            if api_socket.exists():
                break
            time.sleep(0.1)
        if not api_socket.exists():
            raise RuntimeError("Firecracker API socket did not appear")
        lifecycle["created"] = True
        kernel_path = "/vmlinux" if use_jailer else str(KERNEL)
        rootfs_path = "/rootfs.ext4" if use_jailer else str(rootfs)
        worktree_path = "/worktree.ext4" if use_jailer else str(worktree_image)
        api_put(api_socket, "/boot-source", {"kernel_image_path": kernel_path, "boot_args": "console=ttyS0 reboot=k panic=1 pci=off init=/sbin/itbem-init"})
        api_put(api_socket, "/drives/rootfs", {"drive_id": "rootfs", "path_on_host": rootfs_path, "is_root_device": True, "is_read_only": True})
        if worktree_mode:
            api_put(api_socket, "/drives/worktree", {"drive_id": "worktree", "path_on_host": worktree_path, "is_root_device": False, "is_read_only": True})
        api_put(api_socket, "/machine-config", {"vcpu_count": 1, "mem_size_mib": 128, "smt": False})
        vsock_api_path = "/run/vsock.sock" if use_jailer else str(vsock_socket)
        api_put(api_socket, "/vsock", {"guest_cid": 3, "uds_path": vsock_api_path})
        api_put(api_socket, "/actions", {"action_type": "InstanceStart"})
        deadline = time.time() + 12
        while time.time() < deadline and not vsock_socket.exists():
            time.sleep(0.1)
        if not vsock_socket.exists():
            raise RuntimeError("Firecracker vsock socket did not appear")
        while time.time() < deadline:
            if stdout_path.exists():
                captured = stdout_path.read_text(errors="replace")
            if "ITBEM_VSOCK_AGENT_READY" in captured or "ITBEM_VSOCK_LISTENING" in captured:
                break
            time.sleep(0.1)
        response = vsock_command(vsock_socket, command, args)
        guest_exit = 0 if response.get("ok") else 1
        if binding_mode:
            lifecycle["guest_command_executed"] = bool(response.get("ok")) and request["worktree_digest"] in str(response.get("stdout", ""))
        else:
            lifecycle["guest_command_executed"] = bool(response.get("ok")) and str(response.get("stdout", "")).strip() != ""
    except Exception as exc:
        response["error"] = str(exc)
        if stdout_path.exists():
            response["error"] += " | firecracker stdout: " + stdout_path.read_text(errors="replace")[-3000:]
        if stderr_path.exists():
            response["error"] += " | firecracker stderr: " + stderr_path.read_text(errors="replace")[-3000:]
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
        if use_jailer:
            shutil.rmtree(jail_root.parent, ignore_errors=True)

    output = str(response.get("stdout", ""))[-MAX_OUTPUT:]
    attestation = {
        "runtime": "firecracker",
        "runtime_version": "1.7.0",
        "transport": "virtio_vsock",
        "evidence_scope": "task_guest_command_worktree" if worktree_mode else "task_guest_command",
        "guest_command_verified": bool(lifecycle["guest_command_executed"]),
        "evidence_digest": "sha256:" + hashlib.sha256((output + captured).encode()).hexdigest(),
    }
    ATTESTATION_ROOT.mkdir(parents=True, exist_ok=True)
    persisted = ATTESTATION_ROOT / (re.sub(r"[^A-Za-z0-9_.-]", "_", str(request["lease_id"])) + ".json")
    transfer = {
        "mode": "read_only_ext4_worktree" if worktree_mode else "binding_manifest",
        "file_count": worktree_files,
        "byte_count": worktree_bytes,
    }
    # Mark the intended receipt before writing so the durable JSON records the
    # same lifecycle that the response will return. If the write does not
    # produce a non-empty file, the in-memory receipt is revoked below.
    lifecycle["attestation_persisted"] = True
    persisted.write_text(json.dumps({"profile": profile, "request": {"task_id": request["task_id"], "workspace_id": request["workspace_id"], "worktree_digest": request["worktree_digest"]}, "transfer": transfer, "attestation": attestation, "lifecycle": lifecycle}, sort_keys=True) + "\n", encoding="utf-8")
    lifecycle["attestation_persisted"] = persisted.is_file() and persisted.stat().st_size > 0
    shutil.rmtree(workdir, ignore_errors=True)
    ok = guest_exit == 0 and all(lifecycle.values())
    print(json.dumps({"protocol_version": 1, "operation": "execute", "lease_id": request["lease_id"], "ok": ok, "exit_code": guest_exit, "stdout": output, "stderr": str(response.get("stderr", "")), "error": str(response.get("error", "")), "profile": profile, "task_id": request["task_id"], "workspace_id": request["workspace_id"], "worktree_digest": request["worktree_digest"], "worktree_transfer": transfer, "attestation": attestation, "lifecycle": lifecycle}, separators=(",", ":")))
    raise SystemExit(0 if ok else 1)


if __name__ == "__main__":
    main()
