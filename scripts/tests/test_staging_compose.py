import re
import unittest
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]
COMPOSE_FILE = REPOSITORY_ROOT / "deploy" / "staging" / "control-plane.compose.yml"


class StagingComposeContractTests(unittest.TestCase):
    def test_valkey_runs_unprivileged_without_restoring_linux_capabilities(self) -> None:
        compose = COMPOSE_FILE.read_text(encoding="utf-8")
        match = re.search(r"(?ms)^  valkey:\n(?P<body>.*?)(?=^  [a-z][a-z0-9-]*:\n|\Z)", compose)
        self.assertIsNotNone(match, "the disposable staging fixture must define Valkey")
        body = match.group("body")

        self.assertRegex(body, r"(?m)^    user: valkey$")
        self.assertRegex(body, r"(?m)^    read_only: true$")
        self.assertRegex(body, r"(?ms)^    cap_drop:\n      - ALL$")
        self.assertIn("no-new-privileges:true", body)


if __name__ == "__main__":
    unittest.main()
