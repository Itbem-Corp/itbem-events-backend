import hashlib
import json
import unittest

from prepare_implementation_candidate import CASE, package
from prepare_implementation_evaluation import prepare


class CandidatePackageTests(unittest.TestCase):
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
            self.assertFalse(result['assessment']['response_contract_valid'])
