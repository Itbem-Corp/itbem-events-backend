import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from prepare_implementation_candidate import CASE, package
from prepare_implementation_evaluation import prepare
from sandbox_worktree import snapshot_worktree


class CandidatePackageTests(unittest.TestCase):
    def test_prepared_binding_matches_sandbox_and_rejects_source_or_mode_changes(self):
        raw = json.dumps({'changes': [{'path': name, 'content': 'package pagination\n'}
                                     for name in ('page.go', 'store.go')]}).encode()
        result = package(raw)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, content in result['package_files'].items():
                (root / name).write_bytes(content.encode('utf-8'))
                (root / name).chmod(int(result['package_file_modes'][name], 8))
            expected = result['expected_worktree_digest']
            self.assertEqual(snapshot_worktree(root)[0], expected)
            for name in ('page.go', 'page_test.go'):
                original = (root / name).read_bytes()
                (root / name).write_bytes(original + b'\n')
                self.assertNotEqual(snapshot_worktree(root)[0], expected)
                (root / name).write_bytes(original)
            (root / 'page.go').chmod(0o755)
            self.assertNotEqual(snapshot_worktree(root)[0], expected)
            (root / 'page.go').chmod(0o644)
            (root / 'extra.go').write_bytes(b'package pagination\n')
            self.assertNotEqual(snapshot_worktree(root)[0], expected)

    def test_package_keeps_evaluator_oracle_and_module(self):
        raw = json.dumps({'changes': [{'path': name, 'content': 'package pagination\n'}
                                     for name in ('page.go', 'store.go')]}).encode()
        result = package(raw)
        self.assertTrue(result['candidate_package_prepared'])
        self.assertFalse(result['candidate_executed'])
        self.assertIsNone(result['implementation_correctness'])
        self.assertEqual(result['prepared_prompt_sha256'], prepare()['prompt_sha256'])
        self.assertEqual(set(result['package_files']), {'go.mod', 'page.go', 'store.go', 'page_test.go'})
        for name, path in [('go.mod', CASE / 'fixture/go.mod'),
                           ('page_test.go', CASE / 'oracle/page_test.go')]:
            self.assertEqual(result['package_files'][name], path.read_bytes().decode().replace('\r\n', '\n'))
        for name, content in result['package_files'].items():
            self.assertEqual(result['package_file_sha256'][name], hashlib.sha256(content.encode()).hexdigest())

    def test_invalid_response_cannot_prepare_or_replace_oracle(self):
        for raw in (b'{}', json.dumps({'changes': [
                {'path': 'page_test.go', 'content': 'package pagination'},
                {'path': 'store.go', 'content': 'package pagination'}]}).encode()):
            result = package(raw)
            self.assertFalse(result['candidate_package_prepared'])
            self.assertFalse(result['candidate_executed'])
            self.assertEqual(result['package_files'], {})
            self.assertIsNone(result['expected_worktree_digest'])
            self.assertFalse(result['assessment']['response_contract_valid'])
