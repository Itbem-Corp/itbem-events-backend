import importlib.util
import json
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("go_evidence", Path(__file__).resolve().parents[1] / "verify_go_test_evidence.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class GoEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.rows = [
            {"Action": "run", "Package": "fixture/transport", "Test": "TestTransport"},
            {"Action": "pass", "Package": "fixture/transport", "Test": "TestTransport"},
            {"Action": "pass", "Package": "fixture/transport"},
        ]

    def check(self, rows):
        module.verify("\n".join(json.dumps(row) for row in rows), ["TestTransport"])

    def test_valid_execution(self):
        self.check(self.rows)

    def test_rejects_empty_missing_run_skip_duplicate_and_truncated(self):
        for rows in [[], self.rows[1:], self.rows[:-1], self.rows + [self.rows[1]],
                     [self.rows[0], dict(self.rows[1], Action="skip"), self.rows[2]],
                     [self.rows[0], dict(self.rows[1], Package="other"), self.rows[2]]]:
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                self.check(rows)

    def test_rejects_other_test_failure(self):
        with self.assertRaises(ValueError):
            self.check(self.rows + [{"Action": "fail", "Package": "fixture/transport", "Test": "TestOther"}])

    def test_rejects_malformed_and_nonobject_events(self):
        for stream in ["not-json", "[]", "null"]:
            with self.subTest(stream=stream), self.assertRaises(ValueError):
                module.verify(stream, ["TestTransport"])


if __name__ == "__main__":
    unittest.main()
