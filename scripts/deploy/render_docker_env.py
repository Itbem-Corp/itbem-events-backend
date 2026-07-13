#!/usr/bin/env python3
"""Render a Docker --env-file without putting values in shell source or argv."""

from __future__ import annotations

import argparse
import os
import re
import stat
import sys
import tempfile
from pathlib import Path


KEY_PATTERN = re.compile(r"^[A-Z][A-Z0-9_]*$")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--required", action="append", default=[])
    parser.add_argument("--optional", action="append", default=[])
    return parser.parse_args()


def read_values(required: list[str], optional: list[str]) -> list[tuple[str, str]]:
    keys = [*required, *optional]
    if len(keys) != len(set(keys)):
        raise ValueError("environment key list contains duplicates")

    rendered: list[tuple[str, str]] = []
    required_keys = set(required)
    for key in keys:
        if not KEY_PATTERN.fullmatch(key):
            raise ValueError(f"invalid environment key: {key!r}")

        value = os.environ.get(key, "")
        if key in required_keys and value == "":
            raise ValueError(f"required environment value is empty: {key}")
        if any(character in value for character in ("\x00", "\r", "\n")):
            raise ValueError(
                f"environment value cannot be represented by Docker --env-file: {key}"
            )
        rendered.append((key, value))
    return rendered


def write_atomic(output: Path, values: list[tuple[str, str]]) -> None:
    output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    temporary_path: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w",
            encoding="utf-8",
            newline="\n",
            dir=output.parent,
            prefix=f".{output.name}.",
            delete=False,
        ) as temporary:
            temporary_path = Path(temporary.name)
            os.chmod(temporary.name, stat.S_IRUSR | stat.S_IWUSR)
            for key, value in values:
                temporary.write(f"{key}={value}\n")
            temporary.flush()
            os.fsync(temporary.fileno())
        os.replace(temporary_path, output)
        temporary_path = None
    finally:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)


def main() -> int:
    args = parse_args()
    try:
        values = read_values(args.required, args.optional)
        write_atomic(args.output, values)
    except (OSError, ValueError) as error:
        print(f"cannot render Docker environment file: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
