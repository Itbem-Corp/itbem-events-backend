from __future__ import annotations

import base64
import json
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path

from scripts.deploy.verify_github_app_webhook import (
    GitHubWebhookVerifier,
    encode_app_jwt,
    github_requester,
    load_private_key,
)


def decode_segment(value: str) -> dict[str, object]:
    padded = value + "=" * (-len(value) % 4)
    return json.loads(base64.urlsafe_b64decode(padded))


class GitHubAppWebhookTests(unittest.TestCase):
    def test_jwt_is_short_lived_and_signed_without_exposing_key_material(self) -> None:
        token = encode_app_jwt(4739036, 1_800_000_000, lambda value: b"signature")
        header, payload, signature = token.split(".")
        self.assertEqual(decode_segment(header), {"alg": "RS256", "typ": "JWT"})
        self.assertEqual(
            decode_segment(payload),
            {"exp": 1_800_000_540, "iat": 1_799_999_940, "iss": "4739036"},
        )
        self.assertEqual(signature, "c2lnbmF0dXJl")

    def test_redelivers_latest_ping_and_requires_http_200(self) -> None:
        calls: list[tuple[str, str]] = []
        responses = iter(
            [
                [{"id": 10, "event": "ping"}],
                [],
                [
                    {
                        "id": 11,
                        "event": "ping",
                        "redelivery": True,
                        "delivered_at": "2027-01-15T12:00:01Z",
                        "status_code": 200,
                    }
                ],
            ]
        )

        def request(method: str, path: str, body: dict[str, object] | None):
            calls.append((method, path))
            if method == "POST":
                return None
            return next(responses)

        verifier = GitHubWebhookVerifier(
            request,
            clock=lambda: datetime(2027, 1, 15, 12, 0, tzinfo=timezone.utc),
            sleep=lambda _: None,
        )
        self.assertEqual(
            verifier.verify(attempts=2, delay_seconds=0),
            {"delivery_id": 11, "status_code": 200},
        )
        self.assertEqual(calls[1], ("POST", "/app/hook/deliveries/10/attempts"))

    def test_non_200_ping_fails_closed_without_response_payload(self) -> None:
        responses = iter(
            [
                [{"id": 10, "event": "ping"}],
                [
                    {
                        "id": 12,
                        "event": "ping",
                        "redelivery": True,
                        "delivered_at": "2027-01-15T12:00:01Z",
                        "status_code": 401,
                    }
                ],
            ]
        )

        def request(method: str, path: str, body: dict[str, object] | None):
            return None if method == "POST" else next(responses)

        verifier = GitHubWebhookVerifier(
            request,
            clock=lambda: datetime(2027, 1, 15, 12, 0, tzinfo=timezone.utc),
            sleep=lambda _: None,
        )
        with self.assertRaisesRegex(RuntimeError, "HTTP 401"):
            verifier.verify(attempts=1, delay_seconds=0)

    def test_missing_ping_and_untrusted_api_endpoint_fail_closed(self) -> None:
        verifier = GitHubWebhookVerifier(lambda method, path, body: [])
        with self.assertRaisesRegex(RuntimeError, "no ping"):
            verifier.verify(attempts=1, delay_seconds=0)
        with self.assertRaisesRegex(ValueError, "api.github.com"):
            github_requester("token", "https://example.invalid")

    def test_private_key_accepts_one_inline_or_absolute_file_source(self) -> None:
        self.assertEqual(load_private_key(" inline-key ", ""), "inline-key")
        escaped = (
            "-----BEGIN PRIVATE KEY-----\\n"
            "encoded-body\\n"
            "-----END PRIVATE KEY-----"
        )
        self.assertEqual(
            load_private_key(escaped, ""),
            "-----BEGIN PRIVATE KEY-----\nencoded-body\n-----END PRIVATE KEY-----",
        )
        with tempfile.TemporaryDirectory() as directory:
            key_path = Path(directory).resolve() / "app.pem"
            key_path.write_text("file-key\n", encoding="utf-8")
            self.assertEqual(load_private_key("", str(key_path)), "file-key")
            with self.assertRaisesRegex(ValueError, "exactly one"):
                load_private_key("inline-key", str(key_path))
        with self.assertRaisesRegex(ValueError, "exactly one"):
            load_private_key("", "")
        with self.assertRaisesRegex(ValueError, "absolute"):
            load_private_key("", "relative.pem")


if __name__ == "__main__":
    unittest.main()
