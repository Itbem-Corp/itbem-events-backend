from datetime import datetime, timedelta, timezone
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("verify_rds_recovery", Path(__file__).resolve().parents[1] / "deploy" / "verify_rds_recovery.py")
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)


class RecoveryTests(unittest.TestCase):
    def setUp(self):
        self.now = datetime(2026, 10, 4, tzinfo=timezone.utc)
        self.instance = {"DBInstanceIdentifier": "production", "DBInstanceStatus": "available",
                         "BackupRetentionPeriod": 7,
                         "EarliestRestorableTime": (self.now - timedelta(days=6)).isoformat(),
                         "LatestRestorableTime": (self.now - timedelta(minutes=5)).isoformat()}

    def test_current_automated_backup_is_sufficient(self):
        self.assertIn("no deploy snapshot created", recovery.verify(self.instance, "production", self.now))

    def test_unrecoverable_or_wrong_database_is_rejected(self):
        changes = [{"BackupRetentionPeriod": 0}, {"BackupRetentionPeriod": 6},
                   {"PendingModifiedValues": {"BackupRetentionPeriod": 0}},
                   {"LatestRestorableTime": None}, {"EarliestRestorableTime": ""},
                   {"LatestRestorableTime": (self.now - timedelta(minutes=31)).isoformat()},
                   {"LatestRestorableTime": (self.now + timedelta(minutes=6)).isoformat()},
                   {"EarliestRestorableTime": self.now.isoformat()},
                   {"DBInstanceIdentifier": "other"}, {"DBInstanceStatus": "modifying"}]
        for change in changes:
            with self.subTest(change=change), self.assertRaises((ValueError, AttributeError)):
                recovery.verify(self.instance | change, "production", self.now)


if __name__ == "__main__":
    unittest.main()
