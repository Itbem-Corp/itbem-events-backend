#!/usr/bin/env python3
"""Verify a GitHub App webhook by redelivering its latest ping."""

from __future__ import annotations

import base64
import json
import os
import stat
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Callable
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any


GITHUB_API = "https://api.github.com"
API_VERSION = "2022-11-28"


def _base64url(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii")


def encode_app_jwt(app_id: int, now: int, signer: Callable[[bytes], bytes]) -> str:
    header = _base64url(b'{"alg":"RS256","typ":"JWT"}')
    claims = json.dumps(
        {"iat": now - 60, "exp": now + 540, "iss": str(app_id)},
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    payload = _base64url(claims)
    unsigned = f"{header}.{payload}".encode("ascii")
    return f"{unsigned.decode('ascii')}.{_base64url(signer(unsigned))}"


def app_jwt(app_id: int, private_key: str, now: int | None = None) -> str:
    if app_id < 1 or "PRIVATE KEY" not in private_key:
        raise ValueError("GitHub App identity is invalid")
    with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", delete=False) as key:
        key.write(private_key)
        key_path = Path(key.name)
    try:
        os.chmod(key_path, stat.S_IRUSR | stat.S_IWUSR)

        def sign(value: bytes) -> bytes:
            result = subprocess.run(
                ["openssl", "dgst", "-sha256", "-sign", str(key_path)],
                input=value,
                capture_output=True,
                check=False,
            )
            if result.returncode != 0 or not result.stdout:
                raise RuntimeError("GitHub App JWT signing failed")
            return result.stdout

        return encode_app_jwt(app_id, now or int(time.time()), sign)
    finally:
        key_path.unlink(missing_ok=True)


def load_private_key(value: str, file_name: str) -> str:
    value = value.strip()
    file_name = file_name.strip()
    if bool(value) == bool(file_name):
        raise ValueError(
            "configure exactly one GitHub App private key value or root-managed file"
        )
    if value:
        # Docker --env-file cannot represent literal newlines, so the
        # production secret uses the same explicit \n form accepted by the
        # application configuration. Restore PEM framing only in that
        # unambiguous single-line representation.
        if "\n" not in value and "\\n" in value:
            value = value.replace("\\n", "\n")
        return value
    key_path = Path(file_name)
    if not key_path.is_absolute():
        raise ValueError("GitHub App private key file must be absolute")
    return key_path.read_text(encoding="utf-8").strip()


JSONRequest = Callable[[str, str, dict[str, Any] | None], Any]


class GitHubWebhookVerifier:
    def __init__(
        self,
        request_json: JSONRequest,
        *,
        clock: Callable[[], datetime] | None = None,
        sleep: Callable[[float], None] = time.sleep,
    ) -> None:
        self.request_json = request_json
        self.clock = clock or (lambda: datetime.now(timezone.utc))
        self.sleep = sleep

    def verify(self, attempts: int = 12, delay_seconds: float = 2.5) -> dict[str, Any]:
        deliveries = self.request_json("GET", "/app/hook/deliveries?per_page=100", None)
        if not isinstance(deliveries, list):
            raise RuntimeError("GitHub App deliveries response is invalid")
        source = next(
            (
                item
                for item in deliveries
                if isinstance(item, dict)
                and item.get("event") == "ping"
                and isinstance(item.get("id"), int)
            ),
            None,
        )
        if source is None:
            raise RuntimeError("GitHub App has no ping delivery to redeliver")

        cutoff = self.clock() - timedelta(seconds=2)
        self.request_json("POST", f"/app/hook/deliveries/{source['id']}/attempts", None)
        for _ in range(attempts):
            current = self.request_json("GET", "/app/hook/deliveries?per_page=20", None)
            candidate = self._new_ping(current, source["id"], cutoff)
            if candidate is not None and candidate.get("status_code") is not None:
                status_code = candidate.get("status_code")
                if status_code != 200:
                    raise RuntimeError(
                        f"GitHub App ping redelivery returned HTTP {status_code}"
                    )
                return {"delivery_id": candidate["id"], "status_code": status_code}
            self.sleep(delay_seconds)
        raise RuntimeError("GitHub App ping redelivery did not complete in time")

    @staticmethod
    def _new_ping(
        deliveries: Any, source_id: int, cutoff: datetime
    ) -> dict[str, Any] | None:
        if not isinstance(deliveries, list):
            raise RuntimeError("GitHub App deliveries response is invalid")
        for item in deliveries:
            if (
                not isinstance(item, dict)
                or item.get("event") != "ping"
                or item.get("id") == source_id
                or item.get("redelivery") is not True
            ):
                continue
            delivered_at = item.get("delivered_at")
            if not isinstance(delivered_at, str):
                continue
            try:
                timestamp = datetime.fromisoformat(delivered_at.replace("Z", "+00:00"))
            except ValueError:
                continue
            if timestamp >= cutoff:
                return item
        return None


def github_requester(token: str, api_url: str = GITHUB_API) -> JSONRequest:
    parsed = urllib.parse.urlparse(api_url)
    if parsed.scheme != "https" or parsed.hostname != "api.github.com":
        raise ValueError("GitHub API endpoint must be https://api.github.com")

    def request(method: str, path: str, body: dict[str, Any] | None) -> Any:
        data = None if body is None else json.dumps(body).encode("utf-8")
        call = urllib.request.Request(
            api_url + path,
            method=method,
            data=data,
            headers={
                "Accept": "application/vnd.github+json",
                "Authorization": f"Bearer {token}",
                "X-GitHub-Api-Version": API_VERSION,
                "User-Agent": "itbem-backend-release-gate",
            },
        )
        try:
            with urllib.request.urlopen(call, timeout=20) as response:
                payload = response.read()
        except urllib.error.HTTPError as error:
            raise RuntimeError(
                f"GitHub App API request failed with HTTP {error.code}"
            ) from error
        if not payload:
            return None
        return json.loads(payload)

    return request


def main() -> int:
    try:
        app_id = int(os.environ.get("ITBEM_GITHUB_APP_ID", ""))
        private_key = load_private_key(
            os.environ.get("ITBEM_GITHUB_APP_PRIVATE_KEY", ""),
            os.environ.get("ITBEM_GITHUB_APP_PRIVATE_KEY_FILE", ""),
        )
        token = app_jwt(app_id, private_key)
        evidence = GitHubWebhookVerifier(github_requester(token)).verify()
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as error:
        print(f"GitHub review webhook verification failed: {error}", file=os.sys.stderr)
        return 1
    print(
        "github_review_webhook=ready "
        f"status_code={evidence['status_code']} delivery_id={evidence['delivery_id']}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
