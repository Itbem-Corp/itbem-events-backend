import hashlib
import json
import unittest
import tempfile
from pathlib import Path
from unittest.mock import patch

from prepare_implementation_evaluation import CASE, EDITABLE, prepare


class ImplementationPromptTests(unittest.TestCase):
    def test_checkout_line_endings_do_not_change_model_prompt(self):
        reports = []
        with tempfile.TemporaryDirectory() as directory:
            for ending in ('lf', 'crlf'):
                case = Path(directory) / ending
                (case / 'fixture').mkdir(parents=True)
                (case / 'oracle').mkdir()
                for name in ['go.mod'] + EDITABLE:
                    raw = (CASE / 'fixture' / name).read_bytes().replace(b'\r\n', b'\n')
                    if ending == 'crlf':
                        raw = raw.replace(b'\n', b'\r\n')
                    (case / 'fixture' / name).write_bytes(raw)
                (case / 'oracle/page_test.go').write_bytes(b'private oracle')
                with patch('prepare_implementation_evaluation.CASE', case):
                    reports.append(prepare())
        self.assertEqual(reports[0]['prompt'], reports[1]['prompt'])
        self.assertEqual(reports[0]['prompt_sha256'], reports[1]['prompt_sha256'])
        self.assertEqual(reports[0]['fixture_sha256'], reports[1]['fixture_sha256'])
        self.assertNotEqual(reports[0]['fixture_raw_sha256'], reports[1]['fixture_raw_sha256'])

    def test_prompt_contains_only_supplied_fixture(self):
        report = prepare()
        supplied = json.loads(report['prompt'].split('Supplied source (JSON):\n', 1)[1])
        self.assertEqual(set(supplied), {'go.mod', *EDITABLE})
        for name, content in supplied.items():
            raw = (CASE / 'fixture' / name).read_bytes()
            self.assertEqual(content.encode(), raw.replace(b'\r\n', b'\n'))
            self.assertEqual(hashlib.sha256(raw).hexdigest(), report['fixture_raw_sha256'][name])
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
