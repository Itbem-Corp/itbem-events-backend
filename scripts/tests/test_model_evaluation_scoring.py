"""Synthetic scorer fixtures: these are not paid evaluation results."""
import copy
import hashlib
import json
from pathlib import Path
import unittest

from score_model_evaluation import CANDIDATES, score

ROOT = Path(__file__).resolve().parents[2]
CORPUS = json.loads((ROOT / 'scripts/model-evaluation-screening-corpus.json').read_text(encoding='utf-8-sig'))


def fixture():
    calls = []
    for candidate, (provider, model) in CANDIDATES.items():
        for case in CORPUS['cases']:
            prompt = CORPUS['system'].strip() + '\n\n' + case['prompt'].strip()
            calls.append({
                'case_id': case['id'], 'candidate': candidate,
                'task_id': 'synthetic-' + candidate + '-' + case['id'],
                'run_id': 'synthetic-run-' + candidate + '-' + case['id'],
                'receipt_id': 'synthetic-receipt-' + candidate + '-' + case['id'],
                'prompt_sha256': hashlib.sha256(prompt.encode()).hexdigest(),
                'sealed_routes_json': json.dumps([{'provider': provider, 'model': model,
                    'reasoning_enabled': True, 'reasoning_effort': '' if candidate == 'minimax-m3' else 'high'}]),
                'actual_provider': provider, 'actual_model': model,
                'receipt_status': 'accepted', 'status': 'completed',
                'final_answer': json.dumps(case['expected']), 'finish_reason': 'stop',
                'total_cost_microusd': 10, 'gateway_latency_ms': 100,
                'input_tokens': 2, 'output_tokens': 3, 'cached_input_tokens': 0,
                'cache_write_tokens': 0, 'reasoning_tokens': 0,
            })
    return {'batch': {'status': 'completed', 'budget_microusd': 1000000,
                      'reservation_microusd': 600}, 'calls': calls}


class ScoringTests(unittest.TestCase):
    def test_complete_fixture(self):
        result = score(CORPUS, fixture())
        self.assertTrue(result['screening_only'])
        for row in result['results'].values():
            self.assertEqual(row['successes'], 20)
            self.assertEqual(row['unknown_cost_count'], 0)

    def test_ambiguous_accounting(self):
        evidence = fixture()
        evidence['calls'][0].update(receipt_status='ambiguous', status='failed', total_cost_microusd=0)
        row = score(CORPUS, evidence)['results']['minimax-m3']
        self.assertEqual(row['denominator'], 20)
        self.assertEqual(row['successes'], 19)
        self.assertEqual(row['unknown_cost_count'], 1)
        self.assertIsNone(row['api_equivalent_cost_usd'])
        self.assertIsNone(row['api_equivalent_cost_per_success_usd'])
        self.assertEqual(row['unknown_usage_count'], 1)

    def test_missing_accounting(self):
        evidence = fixture()
        del evidence['calls'][0]['total_cost_microusd']
        del evidence['calls'][0]['reasoning_tokens']
        row = score(CORPUS, evidence)['results']['minimax-m3']
        self.assertIsNone(row['api_equivalent_cost_usd'])
        self.assertIsNone(row['tokens_per_case']['input_tokens'])

    def test_invalid_bindings(self):
        for mutation in ('duplicate', 'missing', 'hash', 'effort', 'identity'):
            with self.subTest(mutation=mutation):
                evidence = fixture()
                if mutation == 'duplicate':
                    evidence['calls'][1] = copy.deepcopy(evidence['calls'][0])
                elif mutation == 'missing':
                    evidence['calls'].pop()
                elif mutation == 'hash':
                    evidence['calls'][0]['prompt_sha256'] = 'wrong'
                elif mutation == 'effort':
                    evidence['calls'][0]['sealed_routes_json'] = '[]'
                else:
                    del evidence['calls'][0]['receipt_id']
                with self.assertRaises(ValueError):
                    score(CORPUS, evidence)

    def test_fallback_and_truncation(self):
        evidence = fixture()
        evidence['calls'][0]['actual_model'] = 'unexpected'
        evidence['calls'][1]['finish_reason'] = 'length'
        row = score(CORPUS, evidence)['results']['minimax-m3']
        self.assertEqual(row['successes'], 18)
        self.assertEqual(row['unexpected_routes_or_fallbacks'], 1)
        self.assertEqual(row['truncations'], 1)

    def test_missing_latency(self):
        evidence = fixture()
        for call in evidence['calls'][:20]:
            call['gateway_latency_ms'] = 0
        evidence['calls'][0]['gateway_latency_ms'] = 25
        row = score(CORPUS, evidence)['results']['minimax-m3']
        self.assertEqual(row['gateway_latency_samples'], 1)
        self.assertEqual(row['gateway_latency_p95_ms'], 25)

    def test_server_prompts(self):
        server = json.loads((ROOT / 'internal/modelevaluation/corpus.json').read_text())
        self.assertEqual(CORPUS['system'], server['system'])
        self.assertEqual({c['id']: c['prompt'] for c in CORPUS['cases']},
                         {c['id']: c['prompt'] for c in server['cases']})


if __name__ == '__main__':
    unittest.main()
