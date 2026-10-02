"""Require actual, unskipped execution of named Go qualification tests."""
import json
import sys
from pathlib import Path


def verify(stream: str, required: list[str]) -> None:
    if not required or len(set(required)) != len(required):
        raise ValueError("required test names must be nonempty and unique")
    rows = [json.loads(line) for line in stream.splitlines() if line.strip()]
    if not rows or any(not isinstance(row, dict) for row in rows):
        raise ValueError("missing or invalid Go test events")
    if any(row.get("Action") == "fail" for row in rows):
        raise ValueError("Go test stream contains a failure")
    packages = set()
    for name in required:
        events = [row for row in rows if row.get("Test") == name]
        runs = [row for row in events if row.get("Action") == "run"]
        terminals = [row for row in events if row.get("Action") in {"pass", "fail", "skip"}]
        if len(runs) != 1 or len(terminals) != 1 or terminals[0]["Action"] != "pass":
            raise ValueError(f"missing unskipped execution of {name}")
        package = runs[0].get("Package")
        if not isinstance(package, str) or not package or terminals[0].get("Package") != package:
            raise ValueError(f"inconsistent package identity for {name}")
        packages.add(package)
    for package in packages:
        terminals = [row for row in rows if row.get("Package") == package
                     and not row.get("Test") and row.get("Action") in {"pass", "fail", "skip"}]
        if len(terminals) != 1 or terminals[0]["Action"] != "pass":
            raise ValueError(f"missing successful package completion for {package}")


if __name__ == "__main__":
    try:
        verify(Path(sys.argv[1]).read_text(encoding="utf-8"), sys.argv[2:])
    except (ValueError, OSError, IndexError) as error:
        print(f"Qualification evidence rejected: {error}", file=sys.stderr)
        sys.exit(1)
    print("Required Go qualification tests executed and passed without skips.")
