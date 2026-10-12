import unittest
from pathlib import Path


PROJECT_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = PROJECT_ROOT / ".github" / "workflows" / "deploy-backend.yml"
AWS_QUALIFIER = PROJECT_ROOT / "scripts" / "qualify-agent-platform-live-aws.sh"


class DeployWorkflowContractTests(unittest.TestCase):
    def test_ssm_payload_is_non_empty_json_and_revision_gated(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("remote_payload=", workflow)
        self.assertIn("jq -cn --arg command", workflow)
        self.assertIn("'{commands: [$command]}'", workflow)
        self.assertIn('grep -Fqx "deployed_revision=$REVISION"', workflow)
        self.assertIn("deploy_dir='/var/lib/eventiapp-deploy/${GITHUB_RUN_ID}'", workflow)
        self.assertNotIn("$HOME/.eventiapp-deploy", workflow)
        self.assertNotIn("sed 's|^|echo |", workflow)

    def test_event_client_access_flag_reaches_runtime_env(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        render_step = workflow.split("- name: Render runtime environment without shell interpolation", 1)[1]
        render_step = render_step.split("\n      - name: ", 1)[0]
        self.assertIn("EVENT_CLIENT_ACCESS: ${{ vars.EVENT_CLIENT_ACCESS }}", render_step)
        self.assertIn("--optional EVENT_CLIENT_ACCESS \\", render_step)
        self.assertNotIn("--required EVENT_CLIENT_ACCESS", workflow)

    def test_aws_qualifier_allows_slow_emulator_startup(self):
        qualifier = AWS_QUALIFIER.read_text(encoding="utf-8")
        self.assertIn('while [ "$attempt" -lt 60 ]; do', qualifier)
        self.assertIn("sleep 1", qualifier)


if __name__ == "__main__":
    unittest.main()
