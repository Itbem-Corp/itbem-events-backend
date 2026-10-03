"""Offline publication controls; synthetic inputs are not model results."""
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from evaluation_report import publish_report
from test_model_evaluation_scoring import CORPUS, ROOT, fixture
from test_cache_evaluation import CORPUS as CACHE_CORPUS, cache_fixture


class ReportPublicationTests(unittest.TestCase):
    def test_existing_evidence_and_hardlink_are_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = Path(directory) / 'evidence.json'
            original = b'{"private":"original evidence"}'
            evidence.write_bytes(original)
            alias = evidence.with_name('alias.json')
            alias.hardlink_to(evidence)
            for path in (evidence, alias):
                with self.assertRaises(FileExistsError):
                    publish_report(path, {'replacement': True})
                self.assertEqual(path.read_bytes(), original)
            self.assertEqual(sorted(p.name for p in Path(directory).iterdir()), ['alias.json', 'evidence.json'])

    def test_concurrent_publication_has_one_complete_winner(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'report.json'
            def attempt(index):
                try:
                    publish_report(output, {'index': index, 'data': 'x' * 10000})
                    return index
                except FileExistsError:
                    return None
            with ThreadPoolExecutor(max_workers=8) as pool:
                winners = [index for index in pool.map(attempt, range(16)) if index is not None]
            self.assertEqual(len(winners), 1)
            self.assertEqual(json.loads(output.read_bytes()), {'index': winners[0], 'data': 'x' * 10000})
            self.assertEqual(list(Path(directory).iterdir()), [output])

    def test_cli_binds_original_bytes_and_refuses_input_destination(self):
        for script, corpus, evidence in (
                ('score_model_evaluation.py', CORPUS, fixture()),
                ('analyze_cache_evaluation.py', CACHE_CORPUS, cache_fixture())):
            with self.subTest(script=script):
                self.check_cli(script, corpus, evidence)

    def check_cli(self, script, corpus_value, evidence_value):
        with tempfile.TemporaryDirectory() as directory:
            corpus = Path(directory) / 'corpus.json'
            evidence = Path(directory) / 'evidence.json'
            output = Path(directory) / 'report.json'
            corpus.write_bytes(json.dumps(corpus_value).encode('utf-8'))
            evidence.write_bytes(json.dumps(evidence_value).encode('utf-8'))
            original = evidence.read_bytes()
            command = [sys.executable, str(ROOT / 'scripts' / script),
                       '--corpus', str(corpus), '--evidence', str(evidence), '--output']
            result = subprocess.run(command + [str(output)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            report = json.loads(output.read_bytes())
            self.assertEqual(report['input_sha256'], {
                'corpus': hashlib.sha256(corpus.read_bytes()).hexdigest(),
                'evidence': hashlib.sha256(original).hexdigest()})
            result = subprocess.run(command + [str(evidence)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('FileExistsError', result.stderr)
            self.assertEqual(evidence.read_bytes(), original)

    def test_serialization_failure_does_not_publish_partial_report(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'report.json'
            with self.assertRaises(ValueError):
                publish_report(output, {'invalid': float('nan')})
            self.assertEqual(list(Path(directory).iterdir()), [])
