#!/usr/bin/env python3
"""Read-only preflight for the Firecracker production jailer boundary.

The command never creates cgroups, changes ACLs, starts a VM, or modifies a
workspace. It reports exactly which host capability is missing so the worker
can fail closed instead of silently treating a local proof as production
isolation.
"""

from __future__ import annotations

import json
import os
import pathlib
import subprocess
import sys


ROOT = pathlib.Path(os.environ.get("ITBEM_FIRECRACKER_ROOT", "/tmp/itbem-firecracker"))
RELEASE = ROOT / "release-v1.7.0-x86_64"
FIRECRACKER = RELEASE / "firecracker-v1.7.0-x86_64"
JAILER = RELEASE / "jailer-v1.7.0-x86_64"
CGROUP_ROOT = pathlib.Path(os.environ.get("ITBEM_FIRECRACKER_CGROUP_ROOT", "/sys/fs/cgroup"))


def check_file(path: pathlib.Path) -> dict:
    return {"path": str(path), "exists": path.is_file(), "executable": os.access(path, os.X_OK)}


def version(path: pathlib.Path) -> str:
    try:
        result = subprocess.run([str(path), "--version"], capture_output=True, text=True, timeout=3, check=False)
        return (result.stdout or result.stderr).strip().splitlines()[0] if result.returncode == 0 else ""
    except (OSError, subprocess.SubprocessError):
        return ""


def current_cgroup_path() -> pathlib.Path:
    """Resolve this process' cgroup without trusting an env-supplied path."""
    try:
        for line in pathlib.Path("/proc/self/cgroup").read_text(encoding="utf-8").splitlines():
            hierarchy, _controllers, relative = line.split(":", 2)
            if hierarchy == "0":
                relative = relative.strip().lstrip("/")
                return CGROUP_ROOT / relative if relative else CGROUP_ROOT
    except (OSError, ValueError):
        pass
    return CGROUP_ROOT


def can_create_child_cgroup(path: pathlib.Path) -> bool:
    """Open-only delegation probe; never writes controller state or creates a child."""
    try:
        descriptor = os.open(path / "cgroup.subtree_control", os.O_WRONLY)
    except OSError:
        return False
    os.close(descriptor)
    return True


def controller_status() -> dict:
    controllers = set()
    controllers_file = CGROUP_ROOT / "cgroup.controllers"
    if controllers_file.is_file():
        controllers = set(controllers_file.read_text(encoding="utf-8", errors="replace").split())
    delegated_path = current_cgroup_path()
    delegated_controllers = set()
    delegated_file = delegated_path / "cgroup.controllers"
    if delegated_file.is_file():
        delegated_controllers = set(delegated_file.read_text(encoding="utf-8", errors="replace").split())
    return {
        "mount_exists": CGROUP_ROOT.is_dir(),
        "filesystem": subprocess.run(["stat", "-fc", "%T", str(CGROUP_ROOT)], capture_output=True, text=True, check=False).stdout.strip(),
        "controllers": sorted(controllers),
        "required_controllers": {name: name in controllers and name in delegated_controllers for name in ("cpu", "memory", "pids")},
        "delegated_path": str(delegated_path),
        "delegated_controllers": sorted(delegated_controllers),
        "current_user_can_create_cgroup": can_create_child_cgroup(delegated_path),
    }


def main() -> int:
    firecracker = check_file(FIRECRACKER)
    jailer = check_file(JAILER)
    kvm = {
        "path": "/dev/kvm",
        "exists": pathlib.Path("/dev/kvm").exists(),
        "read_write": os.access("/dev/kvm", os.R_OK | os.W_OK),
    }
    cgroups = controller_status()
    checks = {
        "firecracker": firecracker,
        "jailer": jailer,
        "kvm": kvm,
        "cgroups": cgroups,
    }
    blockers = []
    if not firecracker["exists"] or not firecracker["executable"]:
        blockers.append("firecracker_binary_unavailable")
    if not jailer["exists"] or not jailer["executable"]:
        blockers.append("jailer_binary_unavailable")
    if not kvm["exists"] or not kvm["read_write"]:
        blockers.append("kvm_not_read_write")
    if not cgroups["mount_exists"] or cgroups["filesystem"] != "cgroup2fs":
        blockers.append("cgroup_v2_unavailable")
    for name, present in cgroups["required_controllers"].items():
        if not present:
            blockers.append(f"cgroup_controller_missing:{name}")
    if not cgroups["current_user_can_create_cgroup"]:
        blockers.append("cgroup_parent_not_delegated_for_current_user")
    result = {
        "schema_version": 1,
        "status": "ready" if not blockers else "blocked",
        "checks": checks,
        "versions": {"firecracker": version(FIRECRACKER), "jailer": version(JAILER)},
        "blockers": blockers,
        "next_action": (
            "Run the worker inside a delegated cgroup parent (for example a systemd user scope), register the reviewed Jailer profile, then rerun this preflight."
            if blockers else "Run the production supervisor with the reviewed jailer and cgroup parent."
        ),
    }
    print(json.dumps(result, sort_keys=True))
    return 0 if not blockers else 1


if __name__ == "__main__":
    raise SystemExit(main())
