import json
from pathlib import Path
import unittest

from prepare_implementation_evaluation import prepare


class ImplementationPilotCorpusTests(unittest.TestCase):
    def test_server_pilot_matches_prepared_model_input_without_private_oracle(self):
        root = Path(__file__).resolve().parents[2]
        corpus = json.loads((root / 'internal/modelevaluation/implementation_pilot_corpus.json').read_text(encoding='utf-8'))
        self.assertEqual(corpus['corpus_version'], 'synthetic-implementation-pagination-2026-10-03-v1')
        self.assertEqual(len(corpus['cases']), 1)
        self.assertEqual(corpus['cases'][0]['id'], 'pagination-v1')
        prompt = corpus['system'].strip() + '\n\n' + corpus['cases'][0]['prompt'].strip()
        self.assertEqual(prompt, prepare()['prompt'])
        self.assertNotIn('TestPaginationContract', prompt)
        self.assertNotIn('maxInt :=', prompt)
