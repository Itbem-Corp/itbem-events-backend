"""Exercise the qualifier against real Go test event streams."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


class RequiredGoRunnerTest(unittest.TestCase):
    def test_actual_pass_missing_and_skipped_tests(self):
        self.assertIsNotNone(shutil.which("go"), "Go is required for this qualification test")
        runner = Path(__file__).resolve().parents[1] / "run_required_go_tests.py"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "go.mod").write_text("module qualification.invalid/fixture\n\ngo 1.20\n")
            (root / "proof_test.go").write_text(
                'package fixture\nimport "testing"\n'
                'func TestPassed(t *testing.T) {}\n'
                'func TestSkipped(t *testing.T) { t.Skip("intentional fixture") }\n'
            )
            env = dict(os.environ, GOWORK="off", GOPROXY="off", GOTOOLCHAIN="local")
            for pattern, succeeds in (("TestPassed$", True), ("TestMissing$", False), ("TestSkipped$", False), ("Test(Passed|Missing)$", False), ("Test.*", False)):
                with self.subTest(pattern=pattern):
                    result = subprocess.run([sys.executable, str(runner), "go", "test", "./...", "-run", pattern, "-count=1"], cwd=root, env=env, capture_output=True, text=True)
                    self.assertEqual(result.returncode == 0, succeeds, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
