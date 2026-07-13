import ssl
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

from check_tls_certificate import (
    build_ssl_context,
    evaluate_tls,
    validate_hostname,
    write_atomic,
)


NOW = datetime(2026, 7, 12, 12, 0, tzinfo=timezone.utc)


class TLSCertificateMonitorTests(unittest.TestCase):
    def test_ssl_context_requires_chain_and_hostname_validation(self):
        context = build_ssl_context()
        self.assertTrue(context.check_hostname)
        self.assertEqual(context.verify_mode, ssl.CERT_REQUIRED)
        self.assertGreaterEqual(context.minimum_version, ssl.TLSVersion.TLSv1_2)

    def test_healthy_certificate_outside_warning_window(self):
        result = evaluate_tls(
            "api.eventiapp.com.mx",
            30,
            fetch_expiry=lambda *_args: NOW + timedelta(days=31),
            now=NOW,
        )
        self.assertEqual(result["status"], "ok")
        self.assertEqual(result["days_remaining"], 31)

    def test_certificate_at_warning_boundary_alerts(self):
        result = evaluate_tls(
            "api.eventiapp.com.mx",
            30,
            fetch_expiry=lambda *_args: NOW + timedelta(days=30),
            now=NOW,
        )
        self.assertEqual(result["status"], "alert")

    def test_expired_certificate_alerts(self):
        result = evaluate_tls(
            "api.eventiapp.com.mx",
            30,
            fetch_expiry=lambda *_args: NOW - timedelta(seconds=1),
            now=NOW,
        )
        self.assertEqual(result["status"], "alert")
        self.assertLess(result["days_remaining"], 0)

    def test_chain_or_hostname_verification_failure_alerts(self):
        def fail(*_args):
            raise ssl.SSLCertVerificationError("hostname mismatch")

        result = evaluate_tls(
            "api.eventiapp.com.mx", 30, fetch_expiry=fail, now=NOW
        )
        self.assertEqual(result["status"], "alert")
        self.assertIn("validation failed", result["reason"])

    def test_unexpected_probe_failure_still_produces_an_alert(self):
        def fail(*_args):
            raise RuntimeError("unexpected probe failure")

        result = evaluate_tls(
            "api.eventiapp.com.mx", 30, fetch_expiry=fail, now=NOW
        )
        self.assertEqual(result["status"], "alert")
        self.assertIn("unexpected probe failure", result["reason"])

    def test_invalid_hostname_fails_closed(self):
        for hostname in ("", "api..eventiapp.com.mx", "-api.eventiapp.com.mx", "api_eventiapp.com.mx"):
            with self.subTest(hostname=hostname):
                with self.assertRaises(ValueError):
                    validate_hostname(hostname)

    def test_result_is_written_atomically_as_json(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "result.json"
            write_atomic(output, {"status": "alert", "host": "api.eventiapp.com.mx"})
            self.assertIn('"status": "alert"', output.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
