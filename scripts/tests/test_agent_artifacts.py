import hashlib
from pathlib import Path
import tempfile
import unittest

from verify_agent_artifacts import verify_binaries, workflow_scopes


class ArtifactVerificationTests(unittest.TestCase):
    def test_workflow_scope_ignores_unrelated_shell_lines(self):
        commands = '\n'.join('  go run ./cmd/verify-test-evidence -log ' + name +
                             ' -repetitions 3 -package synthetic/pkg -test Required'
                             for name in ('agent-regression.jsonl', 'localstack-integration.jsonl', 'sandbox-integration.jsonl'))
        scopes = workflow_scopes('go build ./cmd/example \\\n' + commands)
        self.assertEqual(len(scopes), 3)
        self.assertIn('Required', scopes['agent-regression.jsonl'])
        for changed in (commands.splitlines()[0], commands + '\n' + commands.splitlines()[0],
                        commands.replace('sandbox-integration.jsonl', 'unknown.jsonl')):
            with self.assertRaises(ValueError):
                workflow_scopes(changed)

    def test_binary_hashes_and_exact_manifest_names(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for artifact, filename in [('itbem-ai-agent-windows-amd64', 'itbem-ai-agent.exe'),
                                       ('itbem-ai-agent-linux-amd64', 'itbem-ai-agent')]:
                folder = root / artifact
                folder.mkdir()
                raw = artifact.encode()
                (folder / filename).write_bytes(raw)
                (folder / 'SHA256SUMS.txt').write_text(hashlib.sha256(raw).hexdigest() + ' *' + filename + '\n')
            self.assertEqual(len(verify_binaries(root)), 2)
            folder = root / 'itbem-ai-agent-linux-amd64'
            manifest = folder / 'SHA256SUMS.txt'
            original = manifest.read_text()
            for changed in (original.replace('itbem-ai-agent\n', '../itbem-ai-agent\n'),
                            original + original, '0' * 64 + ' *itbem-ai-agent\n'):
                manifest.write_text(changed)
                with self.assertRaises(ValueError):
                    verify_binaries(root)
            manifest.write_text(original)
            (folder / 'itbem-ai-agent').write_bytes(b'changed')
            with self.assertRaises(ValueError):
                verify_binaries(root)
