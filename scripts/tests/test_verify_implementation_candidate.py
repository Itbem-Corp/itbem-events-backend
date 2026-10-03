import copy
import json
from pathlib import Path
import tempfile
import unittest

from prepare_implementation_candidate import package
from verify_implementation_candidate import verify


class CandidateBindingTests(unittest.TestCase):
    def setUp(self):
        self.response = json.dumps({'changes': [
            {'path': name, 'content': 'package pagination\n'}
            for name in ('page.go', 'store.go')]}).encode()
        self.prepared = package(self.response)
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        for name, content in self.prepared['package_files'].items():
            (self.root / name).write_bytes(content.encode())
            (self.root / name).chmod(0o644)

    def check(self, prepared=None, response=None):
        return verify(self.response if response is None else response,
                      json.dumps(self.prepared if prepared is None else prepared).encode(), self.root)

    def test_complete_binding_and_explicit_limits(self):
        result = self.check()
        self.assertTrue(result['candidate_package_binding_verified'])
        self.assertEqual(result['worktree_digest'], self.prepared['expected_worktree_digest'])
        self.assertEqual(result['file_count'], 4)
        self.assertFalse(result['candidate_executed'])
        self.assertFalse(result['provider_provenance_authenticated'])
        self.assertIsNone(result['implementation_correctness'])

    def test_rebound_manifest_and_false_execution_claims_rejected(self):
        for field, value in [('expected_worktree_digest', 'sha256:' + 'a' * 64),
                             ('candidate_executed', True), ('candidate_package_prepared', 1)]:
            changed = copy.deepcopy(self.prepared)
            changed[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.check(changed)
        changed = copy.deepcopy(self.prepared)
        changed['package_files']['page_test.go'] += '\n'
        with self.assertRaises(ValueError):
            self.check(changed)
        with self.assertRaises(ValueError):
            self.check(response=b'{}')

    def test_worktree_mutations_rejected(self):
        for name in ('page.go', 'page_test.go'):
            original = (self.root / name).read_bytes()
            (self.root / name).write_bytes(original + b'\n')
            with self.subTest(name=name), self.assertRaises(ValueError):
                self.check()
            (self.root / name).write_bytes(original)
        (self.root / 'page.go').chmod(0o755)
        with self.assertRaises(ValueError):
            self.check()
        (self.root / 'page.go').chmod(0o644)
        (self.root / '.git').mkdir()
        with self.assertRaises(ValueError):
            self.check()
