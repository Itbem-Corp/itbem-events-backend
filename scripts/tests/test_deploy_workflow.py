import unittest
from pathlib import Path


PROJECT_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = PROJECT_ROOT / ".github" / "workflows" / "deploy-backend.yml"


class DeployWorkflowContractTests(unittest.TestCase):
    def test_ssm_payload_is_non_empty_json_and_revision_gated(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("remote_payload=", workflow)
        self.assertIn("jq -cn --arg command", workflow)
        self.assertIn("'{commands: [$command]}'", workflow)
        self.assertIn('grep -Fqx "deployed_revision=$REVISION"', workflow)
        self.assertNotIn("sed 's|^|echo |", workflow)


if __name__ == "__main__":
    unittest.main()
