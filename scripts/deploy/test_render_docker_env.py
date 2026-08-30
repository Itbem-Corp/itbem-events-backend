from __future__ import annotations

import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("render_docker_env.py")
WORKFLOW = Path(__file__).parents[2] / ".github" / "workflows" / "deploy-backend.yml"


class RenderDockerEnvTests(unittest.TestCase):
    def run_renderer(
        self, value: str | None, *, required: bool = True
    ) -> tuple[subprocess.CompletedProcess[str], Path, tempfile.TemporaryDirectory[str]]:
        temporary_directory = tempfile.TemporaryDirectory()
        output = Path(temporary_directory.name) / "runtime.env"
        environment = os.environ.copy()
        environment.pop("TEST_SECRET", None)
        if value is not None:
            environment["TEST_SECRET"] = value
        argument = "--required" if required else "--optional"
        result = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--output",
                str(output),
                argument,
                "TEST_SECRET",
            ],
            env=environment,
            capture_output=True,
            text=True,
            check=False,
        )
        return result, output, temporary_directory

    def test_shell_metacharacters_are_written_literally(self) -> None:
        value = "literal $(touch /tmp/pwned) `id`; # ' \" = ENVVARS"
        result, output, temporary_directory = self.run_renderer(value)
        self.addCleanup(temporary_directory.cleanup)

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(output.read_text(encoding="utf-8"), f"TEST_SECRET={value}\n")
        if os.name != "nt":
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)

    def test_newlines_fail_closed_without_logging_the_value(self) -> None:
        value = "first-line\nENVVARS\necho compromised"
        result, output, temporary_directory = self.run_renderer(value)
        self.addCleanup(temporary_directory.cleanup)

        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(output.exists())
        self.assertNotIn(value, result.stderr)

    def test_missing_required_value_fails_closed(self) -> None:
        result, output, temporary_directory = self.run_renderer(None)
        self.addCleanup(temporary_directory.cleanup)

        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(output.exists())
        self.assertIn("TEST_SECRET", result.stderr)

    def test_workflow_never_interpolates_secret_expressions_in_run_source(self) -> None:
        lines = WORKFLOW.read_text(encoding="utf-8").splitlines()
        violations: list[str] = []
        block_indent: int | None = None

        for line_number, line in enumerate(lines, start=1):
            stripped = line.lstrip()
            indent = len(line) - len(stripped)
            if block_indent is not None and stripped and indent <= block_indent:
                block_indent = None
            if re.match(r"^run:\s*[|>]", stripped):
                block_indent = indent
            if "${{ secrets." in line and (
                block_indent is not None or stripped.startswith("run:")
            ):
                violations.append(f"{line_number}: {stripped}")

        self.assertEqual(violations, [], "secret expression found in shell source")

    def test_redis_password_allows_the_current_no_auth_cluster(self) -> None:
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("--optional REDIS_PASSWORD", workflow)
        self.assertNotIn("--required REDIS_PASSWORD", workflow)

    def test_runtime_environment_is_validated_before_database_snapshot(self) -> None:
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertLess(
            workflow.index("- name: Render runtime environment"),
            workflow.index("- name: Snapshot database"),
        )

    def test_production_requires_github_review_and_automation_secrets(self) -> None:
        workflow = WORKFLOW.read_text(encoding="utf-8")
        for name in (
            "SQS_AUTOMATION_QUEUE_URL",
            "AUTOMATION_INPUT_BUCKET",
            "AUTOMATION_OUTPUT_BUCKET",
            "AUTOMATION_CALLBACK_SECRET",
            "ITBEM_GITHUB_APP_ID",
            "ITBEM_GITHUB_INSTALLATION_IDS",
            "ITBEM_GITHUB_APP_PRIVATE_KEY",
            "GITHUB_REVIEW_WEBHOOK_SECRET",
            "GITHUB_REVIEW_REPOSITORIES",
        ):
            self.assertIn(f"--required {name}", workflow)
            self.assertIn(f"{name}: ${{{{ secrets.{name} }}}}", workflow)

    def test_automatic_promotion_keeps_rollback_snapshots_bounded(self) -> None:
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("github.event_name == 'push'", workflow)
        self.assertIn("date='7 days ago'", workflow)
        self.assertIn("rds delete-db-snapshot", workflow)


if __name__ == "__main__":
    unittest.main()
