"""Fake receipt fixtures only: no paid provider calls or network."""
import copy
import json
import unittest

from analyze_cache_evaluation import analyze
from build_cache_evaluation_corpus import ROOT, build
from test_model_evaluation_scoring import fixture

SERVER, CORPUS = build()

def cache_fixture():
    evidence = fixture(CORPUS)
    cases = {case['id']: case for case in CORPUS['cases']}
    for call in evidence['calls']:
        shared = cases[call['case_id']]['cache_condition'] == 'shared'
        call.update(input_tokens=2000, output_tokens=10,
                    cached_input_tokens=1500 if shared else 0,
                    total_cost_microusd=850 if shared else 2200,
                    pricing_snapshot_json=json.dumps({'rates_microusd_per_million': {
                        'input_microusd_per_million': 1000000,
                        'cached_microusd_per_million': 100000,
                        'cache_write_microusd_per_million': 1250000,
                        'output_microusd_per_million': 20000000}}))
    return evidence

class CacheTests(unittest.TestCase):
    def test_checked_in_generation_and_answer_separation(self):
        self.assertEqual(SERVER, json.loads((ROOT / 'internal/modelevaluation/cache_corpus.json').read_text()))
        self.assertEqual(CORPUS, json.loads((ROOT / 'scripts/model-evaluation-cache-corpus.json').read_text()))
        self.assertEqual(set(SERVER), {'system', 'cases'})
        for case in SERVER['cases']:
            self.assertEqual(set(case), {'id', 'prompt'})
        self.assertEqual(len(SERVER['cases']), 20)

    def test_paired_accounting_and_correct_answers(self):
        result = analyze(CORPUS, cache_fixture())
        for row in result['results'].values():
            self.assertEqual(row['input_savings_microusd'], 13500)
            self.assertEqual(row['cache_read_gain_tokens'], 15000)
            self.assertTrue(row['qualified_with_complete_accounting'])
            self.assertEqual(len(row['pairs']), 10)
            self.assertEqual(row['conditions']['shared']['output_cost_microusd'], 2000)

    def test_unknown_cache_is_not_zero(self):
        evidence = cache_fixture()
        evidence['calls'][1]['cached_input_tokens'] = None
        # Ledger conservatively bills unknown reads as ordinary input.
        evidence['calls'][1]['total_cost_microusd'] = 2200
        row = analyze(CORPUS, evidence)['results']['minimax-m3']
        self.assertIsNone(row['cache_read_gain_tokens'])
        self.assertFalse(row['qualified_with_complete_accounting'])
        self.assertEqual(row['conditions']['shared']['cached_input_tokens']['unknown_count'], 1)

    def test_unknown_write_preserves_observed_reuse(self):
        evidence = cache_fixture()
        evidence['calls'][0]['cache_write_tokens'] = None
        row = analyze(CORPUS, evidence)['results']['minimax-m3']
        self.assertTrue(row['observed_shared_cache_read'])
        self.assertFalse(row['qualified_with_complete_accounting'])

    def test_quality_regression_prevents_qualification(self):
        evidence = cache_fixture()
        evidence['calls'][1]['final_answer'] = '{}'
        row = analyze(CORPUS, evidence)['results']['minimax-m3']
        self.assertFalse(row['qualified_with_complete_accounting'])
        self.assertTrue(row['pairs'][0]['shared_quality_regression'])

    def test_output_growth_can_erase_input_savings(self):
        evidence = cache_fixture()
        for call in evidence['calls']:
            if call['case_id'].endswith('-shared'):
                call['output_tokens'] = 200
                call['total_cost_microusd'] = 4650
        row = analyze(CORPUS, evidence)['results']['minimax-m3']
        self.assertGreater(row['input_savings_microusd'], 0)
        self.assertLess(row['total_savings_microusd'], 0)
        self.assertFalse(row['qualified_with_complete_accounting'])

    def test_rate_change_is_not_claimed_as_cache_savings(self):
        evidence = cache_fixture()
        call = evidence['calls'][0]
        snapshot = json.loads(call['pricing_snapshot_json'])
        snapshot['rates_microusd_per_million']['input_microusd_per_million'] = 2000000
        call['pricing_snapshot_json'] = json.dumps(snapshot)
        call['total_cost_microusd'] = 4200
        with self.assertRaises(ValueError): analyze(CORPUS, evidence)

    def test_unreconciled_or_impossible_usage_is_rejected(self):
        for mutation in ('cost', 'tokens', 'corpus', 'version'):
            with self.subTest(mutation=mutation):
                evidence = cache_fixture()
                corpus = copy.deepcopy(CORPUS)
                if mutation == 'cost': evidence['calls'][0]['total_cost_microusd'] += 1
                elif mutation == 'tokens': evidence['calls'][0]['cached_input_tokens'] = 2001
                elif mutation == 'corpus': corpus['cases'][0]['cache_condition'] = 'shared'
                else: evidence['batch']['corpus_version'] = 'unknown'
                with self.assertRaises(ValueError): analyze(corpus, evidence)

    def test_ambiguous_failure_keeps_denominator_and_unknown_cost(self):
        evidence = cache_fixture()
        evidence['calls'][1].update(receipt_status='ambiguous', status='failed')
        row = analyze(CORPUS, evidence)['results']['minimax-m3']
        self.assertEqual(row['conditions']['shared']['calls'], 10)
        self.assertIsNone(row['input_savings_microusd'])
        self.assertFalse(row['qualified_with_complete_accounting'])

if __name__ == '__main__':
    unittest.main()
