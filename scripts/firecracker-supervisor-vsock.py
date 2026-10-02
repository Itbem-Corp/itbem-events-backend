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
import select
import signal
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from sandbox_worktree import snapshot_worktree

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
    # debugfs can report success even when a path traverses an unsupported
    # image symlink. Verify the inserted bytes before ever starting a VM.
    inserted = subprocess.run(["debugfs", "-R", f"cat {target}", str(rootfs)], capture_output=True, check=False)
    if inserted.returncode or inserted.stdout != source.read_bytes():
        raise RuntimeError(f"debugfs insertion verification failed: {target}")


def debugfs(rootfs: pathlib.Path, command: str) -> None:
    result = subprocess.run(["debugfs", "-w", "-R", command, str(rootfs)], capture_output=True, text=True, check=False)
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip() or f"debugfs command failed: {command}")


def stage_worktree(source: pathlib.Path, destination: pathlib.Path) -> tuple[int, int]:
    """Compatibility wrapper over the credential-free content-bound snapshot."""
    _, files, total = snapshot_worktree(source, destination)
    return files, total


def verified_guest_read(response: dict, expected: bytes) -> bool:
    output = response.get("stdout")
    try:
        observed = output.encode("utf-8") if isinstance(output, str) else None
    except UnicodeError:
        return False
    return (response.get("ok") is True and isinstance(output, str)
            and not response.get("error") and len(expected) <= MAX_OUTPUT
            and observed == expected)


def open_jailed_process(pid_file: pathlib.Path, executable: pathlib.Path, require_namespace: bool = True) -> int:
    """Pin the exact Jailer child, never signal a reusable numeric PID."""
    if not hasattr(os, "pidfd_open") or not hasattr(signal, "pidfd_send_signal"):
        raise RuntimeError("Jailer teardown requires Linux pidfd support")
    text = pid_file.read_text().strip()
    if not text.isdecimal() or int(text) <= 1:
        raise RuntimeError("invalid Jailer child PID")
    descriptor = os.pidfd_open(int(text))
    try:
        try:
            same_executable = os.path.samefile(f"/proc/{int(text)}/exe", executable)
        except OSError as exc:
            raise RuntimeError("Jailer child executable cannot be verified") from exc
        if not same_executable:
            raise RuntimeError("Jailer child executable does not match this lease")
        if require_namespace:
            status = pathlib.Path(f"/proc/{int(text)}/status").read_text()
            namespaces = next((line.split()[1:] for line in status.splitlines() if line.startswith("NSpid:")), [])
            if len(namespaces) < 2 or namespaces[-1] != "1":
                raise RuntimeError("Jailer child is not init of its PID namespace")
    except Exception:
        os.close(descriptor)
        raise
    return descriptor


def stop_jailed_process(descriptor: int) -> bool:
    poller = select.poll()
    poller.register(descriptor, select.POLLIN)
    for sig in (signal.SIGTERM, signal.SIGKILL):
        try:
            signal.pidfd_send_signal(descriptor, sig)
        except ProcessLookupError:
            pass
        if poller.poll(3000):
            return True
    return False


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


def verified_guest_execution(response: dict) -> bool:
    if not isinstance(response, dict) or response.get("executed") is not True:
        return False
    code = response.get("exit_code")
    if type(code) is not int or code < 0 or code > 255:
        return False
    if response.get("ok") is not (code == 0):
        return False
    if response.get("error", "") != ("" if code == 0 else "guest command failed"):
        return False
    for field in ("stdout", "stderr"):
        value = response.get(field, "")
        if not isinstance(value, str):
            return False
        try:
            if len(value.encode("utf-8")) > MAX_OUTPUT:
                return False
        except UnicodeError:
            return False
    if code == 0:
        tests = {}
        packages = set()
        try:
            for line in response.get("stdout", "").splitlines():
                event = json.loads(line)
                if not isinstance(event, dict):
                    return False
                action, test, package = event.get("Action"), event.get("Test"), event.get("Package")
                if action in ("skip", "fail"):
                    return False
                if test:
                    key = (package, test)
                    if action == "run":
                        tests[key] = False
                    elif action == "pass":
                        if key not in tests:
                            return False
                        tests[key] = True
                elif action == "pass":
                    packages.add(package)
        except (ValueError, TypeError):
            return False
        if not tests or not all(tests.values()) or any(package not in packages for package, _ in tests):
            return False
    return True


def delegated_jailer_parent(relative: str) -> pathlib.Path:
    path = pathlib.PurePosixPath(relative)
    if path.is_absolute() or len(path.parts) < 2 or ".." in path.parts or str(path) != relative:
        raise RuntimeError("jailer cgroup parent must be a normalized delegated relative path")
    current = next(line.split(":", 2)[2] for line in pathlib.Path("/proc/self/cgroup").read_text().splitlines() if line.startswith("0::"))
    current_path = pathlib.PurePosixPath(current.lstrip("/"))
    if path not in (current_path, current_path.parent):
        raise RuntimeError("jailer cgroup parent is outside the supervisor delegation")
    parent = pathlib.Path("/sys/fs/cgroup") / path
    enabled = set((parent / "cgroup.subtree_control").read_text().split())
    if not {"cpu", "memory", "pids"}.issubset(enabled):
        raise RuntimeError("delegated parent must already enable cpu, memory and pids")
    return parent


def verify_jailer_constraints(pid_file: pathlib.Path, cgroup: pathlib.Path, uid: int, gid: int) -> None:
    pid = int(pid_file.read_text().strip())
    expected = "0::/" + str(cgroup.relative_to("/sys/fs/cgroup"))
    if pathlib.Path(f"/proc/{pid}/cgroup").read_text().strip() != expected:
        raise RuntimeError("Jailer child is outside its lease cgroup")
    for filename, value in (("memory.max", "268435456"), ("pids.max", "128"), ("cpu.max", "100000 100000")):
        if " ".join((cgroup / filename).read_text().split()) != value:
            raise RuntimeError("Jailer resource limit was not enforced: " + filename)
    status = pathlib.Path(f"/proc/{pid}/status").read_text().splitlines()
    for name, value in (("Uid:", uid), ("Gid:", gid)):
        row = next(line.split()[1:] for line in status if line.startswith(name))
        if row != [str(value)] * 4 or value == 0:
            raise RuntimeError("Jailer child did not drop its privileges")


def apply_production_limits() -> None:
    """Bound the supervisor process before it launches Firecracker."""
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_NOFILE, (4096, 4096))
    resource.setrlimit(resource.RLIMIT_FSIZE, (512 * 1024 * 1024, 512 * 1024 * 1024))


def vsock_command(vsock_path: pathlib.Path, command: str, args: list[str], timeout: int = 10) -> dict:
    client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    client.settimeout(timeout)
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
            if len(payload) > 2 * MAX_OUTPUT + 4096:
                raise RuntimeError("guest agent response exceeded the bounded output limit")
        return json.loads(payload.decode())
    finally:
        client.close()


def main() -> None:
    profile = os.environ.get("ITBEM_FIRECRACKER_PROFILE", PROFILE_LOCAL).strip().lower()
    force_jailer = False
    cgroup_parent = None
    sdk_path = None
    sdk_sha256 = None
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
            elif argv[index] == "--cgroup-parent" and index + 1 < len(argv):
                cgroup_parent = argv[index + 1]
                index += 2
            elif argv[index] in ("--go-sdk-image", "--go-sdk-sha256") and index + 1 < len(argv):
                if argv[index] == "--go-sdk-image":
                    sdk_path = pathlib.Path(argv[index + 1])
                else:
                    sdk_sha256 = argv[index + 1]
                index += 2
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
    try:
        digest, _, _ = snapshot_worktree(workspace_path)
    except (OSError, RuntimeError, UnicodeError) as exc:
        emit_failure(f"worktree content binding failed: {exc}", request)
    if request["worktree_digest"] != digest:
        emit_failure("worktree digest does not match the bound source content", request)
    command = str(request.get("command", ""))
    args = request.get("args") or []
    go_mode = command in ("go", "/sdk/bin/go") and args == ["test", "-json", "-count=1", "-timeout=30s", "./..."]
    if go_mode:
        command = "/sdk/bin/go"
        if sdk_path is None or not sdk_path.is_absolute() or sdk_path.is_symlink() or not sdk_path.is_file() or sdk_path.stat().st_size > 384 * 1024 * 1024:
            emit_failure("Go execution requires an operator-configured bounded SDK image", request)
        if not isinstance(sdk_sha256, str) or not re.fullmatch(r"[a-f0-9]{64}", sdk_sha256):
            emit_failure("Go SDK image requires a pinned SHA256", request)
        if hashlib.sha256(sdk_path.read_bytes()).hexdigest() != sdk_sha256:
            emit_failure("Go SDK image checksum mismatch", request)
        if request.get("timeout_ms", 0) < 90000:
            emit_failure("Go execution requires a 90-second outer lifecycle budget", request)
    # The guest agent has a narrow allow-list. The binding mode is retained for
    # the protocol smoke test; the worktree mode reads an actual staged file
    # from a read-only second guest drive.
    binding_mode = command == "/bin/cat" and args == ["/itbem-fixture/binding.txt"]
    worktree_mode = (
        command == "/bin/cat" and len(args) == 1 and args[0].startswith("/workspace/")
        and ".." not in pathlib.PurePosixPath(args[0]).parts
    )
    worktree_mode = worktree_mode or go_mode
    if not binding_mode and not worktree_mode:
        emit_failure("virtio-vsock proof only permits /bin/cat of the binding manifest or /workspace/*", request)
    if request.get("input") or request.get("environment"):
        emit_failure("virtio-vsock proof does not forward host input or environment", request)
    for artifact in (FIRECRACKER, KERNEL, ROOTFS, GUEST_AGENT):
        if not artifact.is_file():
            emit_failure(f"missing Firecracker artifact: {artifact}", request)
    use_jailer = profile == PROFILE_PRODUCTION and (force_jailer or os.environ.get("ITBEM_FIRECRACKER_USE_JAILER", "0") == "1")
    if go_mode and use_jailer:
        emit_failure("Go toolchain requires a qualified jailer memory and scratch profile before production admission", request)
    if use_jailer:
        try:
            delegated_parent = delegated_jailer_parent(cgroup_parent or "")
        except (OSError, RuntimeError, StopIteration) as exc:
            emit_failure(str(exc), request)
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
    if use_jailer and (jailer_uid <= 0 or jailer_gid <= 0):
        shutil.rmtree(workdir)
        emit_failure("jailer child requires non-root UID and GID", request)
    jail_root = jailer_base / FIRECRACKER.name / jailer_id / "root"
    jail_cgroup = delegated_parent / jailer_id if use_jailer else None
    if use_jailer and (jail_cgroup.exists() or jail_root.parent.exists()):
        shutil.rmtree(workdir)
        emit_failure("refusing to reuse an existing jail or cgroup lease", request)
    if use_jailer:
        api_socket = jail_root / "run" / "api.sock"
        vsock_socket = jail_root / "run" / "v.sock"
        if max(len(os.fsencode(api_socket)), len(os.fsencode(vsock_socket))) > 107:
            shutil.rmtree(workdir)
            emit_failure("jailer base exceeds the Unix socket path limit", request)
    rootfs = workdir / "rootfs.ext4"
    worktree_image = workdir / "worktree.ext4"
    staging = workdir / "worktree"
    stdout_path = workdir / "firecracker.stdout"
    stderr_path = workdir / "firecracker.stderr"
    binding = workdir / "binding.txt"
    init_path = workdir / "itbem-init"
    shutil.copy2(ROOTFS, rootfs)
    sdk_image = workdir / "sdk.ext4"
    if go_mode:
        shutil.copyfile(sdk_path, sdk_image)
        if hashlib.sha256(sdk_image.read_bytes()).hexdigest() != sdk_sha256:
            shutil.rmtree(workdir)
            emit_failure("Go SDK changed during staging", request)
    if use_jailer:
        jailer_base.mkdir(parents=True, exist_ok=True)
    worktree_files = 0
    worktree_bytes = 0
    if worktree_mode:
        try:
            staged_digest, worktree_files, worktree_bytes = snapshot_worktree(workspace_path, staging)
        except (OSError, RuntimeError, UnicodeError) as exc:
            shutil.rmtree(workdir)
            emit_failure(f"worktree staging failed: {exc}", request)
        if staged_digest != request["worktree_digest"]:
            shutil.rmtree(workdir)
            emit_failure("worktree source changed before guest staging", request)
        build_worktree_image(staging, worktree_image, worktree_bytes)
    binding.write_text(
        f"task_id={request['task_id']}\nworkspace_id={request['workspace_id']}\nworktree_digest={request['worktree_digest']}\n",
        encoding="utf-8",
    )
    expected_file = binding if binding_mode or go_mode else staging.joinpath(*pathlib.PurePosixPath(args[0]).parts[2:])
    if not expected_file.is_file() or expected_file.stat().st_size > MAX_OUTPUT:
        shutil.rmtree(workdir)
        emit_failure("guest read proof requires a bounded regular text file", request)
    expected_output = expected_file.read_bytes()
    try:
        expected_output.decode("utf-8")
    except UnicodeError:
        shutil.rmtree(workdir)
        emit_failure("guest read proof requires UTF-8 content", request)
    init_lines = [
        "#!/bin/sh",
        "set -eu",
    ]
    if worktree_mode:
        init_lines.extend([
            "mount -t ext4 -o ro /dev/vdb /workspace",
        ])
    if go_mode:
        init_lines.extend([
            "mount -t proc proc /proc",
            "mount -t tmpfs -o size=512m,nosuid,nodev tmpfs /tmp",
            "mount -t ext4 -o ro /dev/vdc /sdk",
        ])
    init_lines.extend([
        "/itbem-guest-agent >/dev/console 2>&1 &",
        "echo ITBEM_VSOCK_AGENT_READY >/dev/console",
        "exec /bin/sh",
        "",
    ])
    init_path.write_text("\n".join(init_lines), encoding="utf-8")
    # The shared hello rootfs may already contain files from the serial proof.
    # Remove those entries first; debugfs `write` does not replace an existing
    # inode and silently leaving the old init would boot the wrong protocol.
    subprocess.run(["debugfs", "-w", "-R", "rm /itbem-init", str(rootfs)], capture_output=True, check=False)
    subprocess.run(["debugfs", "-w", "-R", "rm /itbem-guest-agent", str(rootfs)], capture_output=True, check=False)
    subprocess.run(["debugfs", "-w", "-R", "rm /itbem-fixture/binding.txt", str(rootfs)], capture_output=True, check=False)
    subprocess.run(["debugfs", "-w", "-R", "rm -r /workspace", str(rootfs)], capture_output=True, check=False)
    subprocess.run(["debugfs", "-w", "-R", "mkdir /itbem-fixture", str(rootfs)], capture_output=True, check=False)
    if worktree_mode:
        subprocess.run(["debugfs", "-w", "-R", "mkdir /workspace", str(rootfs)], capture_output=True, check=False)
    if go_mode:
        debugfs(rootfs, "mkdir /sdk")
    debugfs_write(rootfs, binding, "/itbem-fixture/binding.txt")
    debugfs_write(rootfs, GUEST_AGENT, "/itbem-guest-agent")
    debugfs_write(rootfs, init_path, "/itbem-init")
    debugfs(rootfs, "set_inode_field /itbem-guest-agent mode 0100755")
    debugfs(rootfs, "set_inode_field /itbem-init mode 0100755")

    lifecycle = {"created": False, "worktree_bound": True, "guest_command_executed": False, "destroyed": False, "attestation_persisted": False}
    process = None
    jailed_process = None
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
                "--parent-cgroup", cgroup_parent,
                "--cgroup", "cpu.max=100000 100000",
                "--cgroup", "memory.max=268435456",
                "--cgroup", "pids.max=128",
                "--resource-limit", "no-file=4096",
                "--resource-limit", "fsize=536870912",
                "--", "--api-sock", "/run/api.sock", "--level", "Warning",
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
        if use_jailer:
            jailed_process = open_jailed_process(jail_root / (FIRECRACKER.name + ".pid"), jail_root / FIRECRACKER.name)
            verify_jailer_constraints(jail_root / (FIRECRACKER.name + ".pid"), jail_cgroup, jailer_uid, jailer_gid)
        lifecycle["created"] = True
        kernel_path = "/vmlinux" if use_jailer else str(KERNEL)
        rootfs_path = "/rootfs.ext4" if use_jailer else str(rootfs)
        worktree_path = "/worktree.ext4" if use_jailer else str(worktree_image)
        api_put(api_socket, "/boot-source", {"kernel_image_path": kernel_path, "boot_args": "console=ttyS0 reboot=k panic=1 pci=off init=/itbem-init"})
        api_put(api_socket, "/drives/rootfs", {"drive_id": "rootfs", "path_on_host": rootfs_path, "is_root_device": True, "is_read_only": True})
        if worktree_mode:
            api_put(api_socket, "/drives/worktree", {"drive_id": "worktree", "path_on_host": worktree_path, "is_root_device": False, "is_read_only": True})
        if go_mode:
            api_put(api_socket, "/drives/sdk", {"drive_id": "sdk", "path_on_host": str(sdk_image), "is_root_device": False, "is_read_only": True})
        api_put(api_socket, "/machine-config", {"vcpu_count": 1, "mem_size_mib": 768 if go_mode else 128, "smt": False})
        vsock_api_path = "/run/v.sock" if use_jailer else str(vsock_socket)
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
        response = vsock_command(vsock_socket, command, args, 65 if go_mode else 10)
        lifecycle["guest_command_executed"] = verified_guest_execution(response) if go_mode else verified_guest_read(response, expected_output)
        guest_exit = response["exit_code"] if go_mode and lifecycle["guest_command_executed"] else (0 if lifecycle["guest_command_executed"] else 1)
        if not lifecycle["guest_command_executed"]:
            response["error"] = "guest output does not match the content-bound file"
    except Exception as exc:
        response["error"] = str(exc)
        if stdout_path.exists():
            response["error"] += " | firecracker stdout: " + stdout_path.read_text(errors="replace")[-3000:]
        if stderr_path.exists():
            response["error"] += " | firecracker stderr: " + stderr_path.read_text(errors="replace")[-3000:]
    finally:
        if use_jailer:
            try:
                if jailed_process is None:
                    jailed_process = open_jailed_process(jail_root / (FIRECRACKER.name + ".pid"), jail_root / FIRECRACKER.name)
                lifecycle["destroyed"] = stop_jailed_process(jailed_process)
                if lifecycle["destroyed"]:
                    jail_cgroup.rmdir()
            except (OSError, RuntimeError) as exc:
                lifecycle["destroyed"] = False
                response["error"] = "Jailer child teardown could not be verified: " + str(exc)
            finally:
                if jailed_process is not None:
                    os.close(jailed_process)
        if process is not None:
            try:
                process.terminate()
                process.wait(timeout=3)
            except Exception:
                process.kill()
                process.wait(timeout=3)
            if not use_jailer:
                lifecycle["destroyed"] = process.poll() is not None
        if stdout_path.exists():
            captured = stdout_path.read_text(errors="replace")
        if stderr_path.exists():
            captured += "\n" + stderr_path.read_text(errors="replace")
        if use_jailer and lifecycle["destroyed"]:
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
    if go_mode:
        attestation["toolchain_image_sha256"] = sdk_sha256
        attestation["registered_command"] = "go.test.json.offline"
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
    protocol_ok = all(lifecycle.values())
    ok = guest_exit == 0 and protocol_ok
    print(json.dumps({"protocol_version": 1, "operation": "execute", "lease_id": request["lease_id"], "ok": ok, "exit_code": guest_exit, "stdout": output, "stderr": str(response.get("stderr", "")), "error": str(response.get("error", "")), "profile": profile, "task_id": request["task_id"], "workspace_id": request["workspace_id"], "worktree_digest": request["worktree_digest"], "worktree_transfer": transfer, "attestation": attestation, "lifecycle": lifecycle}, separators=(",", ":")))
    raise SystemExit(0 if protocol_ok else 1)


if __name__ == "__main__":
    main()
