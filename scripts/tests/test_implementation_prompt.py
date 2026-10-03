import hashlib
import json
import unittest

from prepare_implementation_evaluation import CASE, EDITABLE, prepare


class ImplementationPromptTests(unittest.TestCase):
    def test_prompt_contains_only_supplied_fixture(self):
        report = prepare()
        supplied = json.loads(report['prompt'].split('Supplied source (JSON):\n', 1)[1])
        self.assertEqual(set(supplied), {'go.mod', *EDITABLE})
        for name, content in supplied.items():
            self.assertEqual(content.encode(), (CASE / 'fixture' / name).read_bytes())
            self.assertEqual(hashlib.sha256(content.encode()).hexdigest(), report['fixture_sha256'][name])
        self.assertNotIn('TestPaginationContract', report['prompt'])
        self.assertNotIn('maxInt :=', report['prompt'])
        self.assertNotIn('if end > len(items)', report['prompt'])

    def test_preparation_is_deterministic_and_not_execution_evidence(self):
        report = prepare()
        self.assertEqual(report, prepare())
        self.assertEqual(report['status'], 'prepared_not_executed')
        self.assertFalse(report['model_quality_measured'])
        self.assertEqual(hashlib.sha256(report['prompt'].encode()).hexdigest(), report['prompt_sha256'])
        self.assertEqual(hashlib.sha256((CASE / 'oracle/page_test.go').read_bytes()).hexdigest(), report['private_oracle_sha256'])
