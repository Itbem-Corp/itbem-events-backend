from pathlib import Path
import unittest


class AgentReleaseWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        root = Path(__file__).resolve().parents[2]
        cls.workflow = (root / ".github" / "workflows" / "deploy-backend.yml").read_text(encoding="utf-8")
        cls.runbook = (root / "deploy" / "systemd" / "README.md").read_text(encoding="utf-8")

    def test_validated_artifact_is_bound_to_source_revision_and_digest(self) -> None:
        self.assertIn("id: agent_release", self.workflow)
        self.assertIn("GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=true", self.workflow)
        self.assertIn('release_dir="${RUNNER_TEMP}/itbem-ai-agent-release"', self.workflow)
        self.assertIn('echo "directory=$release_dir" >> "$GITHUB_OUTPUT"', self.workflow)
        self.assertIn('[[ "$embedded_revision" == "$REVISION" ]]', self.workflow)
        self.assertIn('"source_revision": "%s"', self.workflow)
        self.assertIn('"sha256": "%s"', self.workflow)
        self.assertIn('"target": "linux/amd64"', self.workflow)
        self.assertIn("agent_release_sha256: ${{ steps.agent_release.outputs.sha256 }}", self.workflow)

    def test_artifact_publication_has_only_required_scope_and_guardrails(self) -> None:
        self.assertIn("permissions:\n  artifact-metadata: write\n  contents: read", self.workflow)
        self.assertIn("uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02", self.workflow)
        self.assertIn("name: itbem-ai-agent-release-${{ steps.revision.outputs.sha }}", self.workflow)
        self.assertIn("path: ${{ steps.agent_release.outputs.directory }}", self.workflow)
        self.assertIn("if-no-files-found: error", self.workflow)
        self.assertIn("retention-days: 30", self.workflow)
        self.assertIn('[[ "$AGENT_RELEASE_SHA256" =~ ^[a-f0-9]{64}$ ]]', self.workflow)

    def test_operator_runbook_requires_manifest_verification(self) -> None:
        self.assertIn("release-manifest.json", self.runbook)
        self.assertIn("Publish validated Linux agent release artifact", self.runbook)
        self.assertIn("source_revision", self.runbook)
        self.assertIn("itbem-ai-agent.sha256", self.runbook)
        self.assertIn("never calculate the expected digest", self.runbook)
        self.assertIn("untrusted local copy", self.runbook)


if __name__ == "__main__":
    unittest.main()
