#!/usr/bin/env python3
"""Externally validate a TLS certificate chain/hostname and expiry window."""

from __future__ import annotations

import argparse
import json
import math
import os
import socket
import ssl
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable


FetchExpiry = Callable[[str, int, float], datetime]


def validate_hostname(hostname: str) -> None:
    if not hostname or len(hostname) > 253 or ".." in hostname:
        raise ValueError("invalid TLS hostname")
    labels = hostname.split(".")
    if any(
        not label
        or len(label) > 63
        or label.startswith("-")
        or label.endswith("-")
        or not all(character.isascii() and (character.isalnum() or character == "-") for character in label)
        for label in labels
    ):
        raise ValueError("invalid TLS hostname")


def build_ssl_context() -> ssl.SSLContext:
    context = ssl.create_default_context()
    context.check_hostname = True
    context.verify_mode = ssl.CERT_REQUIRED
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    return context


def fetch_certificate_expiry(hostname: str, port: int, timeout: float) -> datetime:
    context = build_ssl_context()
    with socket.create_connection((hostname, port), timeout=timeout) as connection:
        with context.wrap_socket(connection, server_hostname=hostname) as tls_connection:
            certificate = tls_connection.getpeercert()
    not_after = certificate.get("notAfter")
    if not isinstance(not_after, str) or not not_after:
        raise ssl.SSLError("peer certificate has no notAfter value")
    return datetime.fromtimestamp(ssl.cert_time_to_seconds(not_after), timezone.utc)


def evaluate_tls(
    hostname: str,
    warning_days: int,
    *,
    port: int = 443,
    timeout: float = 10.0,
    fetch_expiry: FetchExpiry = fetch_certificate_expiry,
    now: datetime | None = None,
) -> dict[str, object]:
    validate_hostname(hostname)
    checked_at = now or datetime.now(timezone.utc)
    if checked_at.tzinfo is None:
        raise ValueError("current time must be timezone-aware")
    result: dict[str, object] = {
        "host": hostname,
        "port": port,
        "checked_at": checked_at.astimezone(timezone.utc).isoformat(),
        "warning_days": warning_days,
    }
    try:
        expires_at = fetch_expiry(hostname, port, timeout).astimezone(timezone.utc)
        seconds_remaining = (expires_at - checked_at).total_seconds()
        days_remaining = math.floor(seconds_remaining / 86400)
        result.update(
            {
                "expires_at": expires_at.isoformat(),
                "days_remaining": days_remaining,
            }
        )
        if seconds_remaining <= warning_days * 86400:
            result.update(
                status="alert",
                reason="certificate is expired or inside the renewal warning window",
            )
        else:
            result.update(status="ok", reason="certificate chain, hostname, and expiry are valid")
    except Exception as error:
        result.update(
            status="alert",
            reason=f"TLS chain/hostname validation failed: {str(error)[:500]}",
        )
    return result


def write_atomic(path: Path, payload: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary_path: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w",
            encoding="utf-8",
            newline="\n",
            dir=path.parent,
            prefix=f".{path.name}.",
            delete=False,
        ) as temporary:
            temporary_path = Path(temporary.name)
            json.dump(payload, temporary, indent=2, sort_keys=True)
            temporary.write("\n")
            temporary.flush()
            os.fsync(temporary.fileno())
        os.replace(temporary_path, path)
        temporary_path = None
    finally:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", required=True)
    parser.add_argument("--port", type=int, default=443, choices=range(1, 65536))
    parser.add_argument("--warning-days", type=int, default=30, choices=range(1, 366))
    parser.add_argument("--timeout", type=float, default=10.0)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    result = evaluate_tls(
        args.host,
        args.warning_days,
        port=args.port,
        timeout=args.timeout,
    )
    write_atomic(args.output, result)
    print(
        f"TLS monitor status={result['status']} host={args.host} "
        f"days_remaining={result.get('days_remaining', 'unknown')}"
    )
    return 0 if result["status"] == "ok" else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except ValueError as error:
        print(f"TLS monitor configuration failed: {error}", file=sys.stderr)
        raise SystemExit(2) from error
