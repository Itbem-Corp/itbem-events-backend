"""Synthetic scorer fixtures: these are not paid evaluation results."""
import copy
import hashlib
import json
from pathlib import Path
import unittest

from score_model_evaluation import CANDIDATES, SCREENING_VERSION, score

ROOT = Path(__file__).resolve().parents[2]
CORPUS = json.loads((ROOT / 'scripts/model-evaluation-screening-corpus.json').read_text(encoding='utf-8-sig'))


def fixture(corpus=CORPUS):
    calls = []
    for candidate, (provider, model) in CANDIDATES.items():
        for case in corpus['cases']:
            prompt = corpus['system'].strip() + '\n\n' + case['prompt'].strip()
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
                'pricing_basis': 'conservative_api_equivalent_not_invoice',
                'input_tokens': 2, 'output_tokens': 3, 'cached_input_tokens': 0,
                'cache_write_tokens': 0, 'reasoning_tokens': 0,
            })
    return {'batch': {'status': 'completed', 'budget_microusd': 1000000,
                      'reservation_microusd': 600, 'corpus_version': corpus.get('corpus_version', SCREENING_VERSION)}, 'calls': calls}


class ScoringTests(unittest.TestCase):
    def test_completed_batch_cannot_hide_unfinished_outcomes(self):
        for status, receipt in (('running', 'accepted'), ('failed', 'accepted'),
                                ('completed', 'ambiguous')):
            with self.subTest(status=status, receipt=receipt):
                evidence = fixture()
                evidence['calls'][0].update(status=status, receipt_status=receipt)
                result = score(CORPUS, evidence)
                self.assertFalse(result['complete'])
                self.assertTrue(result['reported_complete'])
                self.assertEqual(result['incomplete_outcome_count'], 1)
                self.assertEqual(result['results']['minimax-m3']['denominator'], 20)

    def test_failure_diagnostics_distinguish_answer_and_execution(self):
        evidence = fixture()
        evidence['calls'][0]['final_answer'] = 'not-json'
        evidence['calls'][1]['final_answer'] = '{"wrong":true}'
        evidence['calls'][2].update(status='failed', receipt_status='ambiguous',
                                  actual_model='unexpected', finish_reason='length',
                                  result_error='synthetic failure', final_answer='not-json')
        row = score(CORPUS, evidence)['results']['minimax-m3']
        self.assertEqual(row['successes'], 17)
        self.assertEqual(row['denominator'], 20)
        self.assertEqual(row['cases'][0]['failure_reasons'], ['invalid_json'])
        self.assertEqual(row['cases'][1]['failure_reasons'], ['answer_mismatch'])
        self.assertEqual(set(row['cases'][2]['failure_reasons']), {
            'task_not_completed', 'receipt_not_accepted', 'unexpected_route',
            'truncated', 'result_error', 'invalid_json'})
        self.assertEqual(row['failure_reason_counts']['invalid_json'], 2)
        self.assertEqual(row['failure_reason_counts']['answer_mismatch'], 1)
        self.assertTrue(all(not case['failure_reasons'] for case in row['cases'][3:]))

    def test_completion_is_independent_of_answer_quality_and_not_inferred(self):
        evidence = fixture()
        evidence['calls'][0]['final_answer'] = '{"wrong":true}'
        result = score(CORPUS, evidence)
        self.assertTrue(result['complete'])
        self.assertEqual(result['incomplete_outcome_count'], 0)
        self.assertEqual(result['results']['minimax-m3']['successes'], 19)
        evidence['batch']['status'] = 'active'
        result = score(CORPUS, evidence)
        self.assertFalse(result['complete'])
        self.assertFalse(result['reported_complete'])
        self.assertEqual(result['incomplete_outcome_count'], 0)

    def test_reused_receipt_or_run_cannot_count_as_new_outcome(self):
        for field in ('receipt_id', 'run_id'):
            with self.subTest(field=field):
                evidence = fixture()
                evidence['calls'][1][field] = evidence['calls'][0][field]
                with self.assertRaises(ValueError):
                    score(CORPUS, evidence)

    def test_accepted_identities_must_be_nonblank_strings(self):
        for field in ('run_id', 'receipt_id'):
            for value in (True, 123, ['identity'], ' ', ' padded '):
                with self.subTest(field=field, value=value):
                    evidence = fixture()
                    evidence['calls'][0][field] = value
                    with self.assertRaises(ValueError):
                        score(CORPUS, evidence)

    def test_modified_corpus_cannot_redefine_success(self):
        for mutation in ('expected', 'duplicate', 'type', 'version'):
            with self.subTest(mutation=mutation):
                corpus = copy.deepcopy(CORPUS)
                if mutation == 'expected':
                    corpus['cases'][0]['expected'] = {'bug': False, 'code': 'none'}
                elif mutation == 'duplicate':
                    corpus['cases'].append(copy.deepcopy(corpus['cases'][0]))
                elif mutation == 'type':
                    corpus['cases'][0]['expected']['bug'] = 1
                else:
                    corpus['corpus_version'] = 'unknown'
                with self.assertRaises(ValueError):
                    score(corpus, fixture(CORPUS if mutation == 'duplicate' else corpus))

    def test_screening_batch_requires_matching_version(self):
        for version in (None, '', 'unknown', 'synthetic-prefix-cache-20-2026-10-01-v1'):
            with self.subTest(version=version):
                evidence = fixture()
                evidence['batch']['corpus_version'] = version
                with self.assertRaises(ValueError):
                    score(CORPUS, evidence)

    def test_complete_fixture(self):
        result = score(CORPUS, fixture())
        self.assertTrue(result['screening_only'])
        for row in result['results'].values():
            self.assertEqual(row['successes'], 20)
            self.assertEqual(row['unknown_cost_count'], 0)

    def test_failed_outcomes_keep_missing_identities_but_not_reused_receipts(self):
        evidence = fixture()
        evidence['calls'][0].update(receipt_status='ambiguous', status='failed', run_id='', receipt_id=None)
        row = score(CORPUS, evidence)['results']['minimax-m3']
        self.assertEqual(row['successes'], 19)
        self.assertEqual(row['denominator'], 20)
        evidence['calls'][0]['receipt_id'] = evidence['calls'][1]['receipt_id']
        with self.assertRaisesRegex(ValueError, 'Reused evaluation outcome identity'):
            score(CORPUS, evidence)

    def test_ambiguous_sealed_route_is_rejected(self):
        evidence = fixture()
        original = evidence['calls'][0]['sealed_routes_json']
        evidence['calls'][0]['sealed_routes_json'] = '[{"provider":"unexpected",' + original[2:]
        with self.assertRaisesRegex(ValueError, 'Duplicate evaluation JSON field'):
            score(CORPUS, evidence)

    def test_ambiguous_model_json_is_not_successful(self):
        for answer in ('{"bug":false,"bug":true,"code":"missing_organization_authorization"}',
                       '{"bug":false,"\\u0062ug":true,"code":"missing_organization_authorization"}',
                       '{"metadata":{"value":1,"value":2}}',
                       '{"value":NaN}', '{"value":Infinity}', '{"value":-Infinity}'):
            with self.subTest(answer=answer):
                evidence = fixture()
                evidence['calls'][0]['final_answer'] = answer
                row = score(CORPUS, evidence)['results']['minimax-m3']
                self.assertEqual(row['valid_json_count'], 19)
                self.assertEqual(row['successes'], 19)
                self.assertEqual(row['denominator'], 20)
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
        self.assertEqual(row['tokens_per_case']['input_tokens'], 2)
        self.assertIsNone(row['tokens_per_case']['reasoning_tokens'])

    def test_missing_cache_does_not_erase_known_input_and_output(self):
        evidence = fixture()
        evidence['calls'][0]['cached_input_tokens'] = None
        row = score(CORPUS, evidence)['results']['minimax-m3']
        self.assertEqual(row['total_tokens']['input_tokens'], 40)
        self.assertEqual(row['total_tokens']['output_tokens'], 60)
        self.assertIsNone(row['total_tokens']['cached_input_tokens'])
        self.assertEqual(row['unknown_token_counts']['cached_input_tokens'], 1)
        self.assertEqual(row['unknown_token_counts']['input_tokens'], 0)
        self.assertEqual(row['tokens_per_case']['output_tokens'], 3)
        self.assertEqual(row['tokens_per_success']['input_tokens'], 2)

    def test_invalid_known_counter_is_rejected_despite_missing_cache(self):
        for value in (True, -1, 1.5, '10', float('nan')):
            with self.subTest(value=value):
                evidence = fixture()
                evidence['calls'][0].update(cached_input_tokens=None, input_tokens=value)
                with self.assertRaises(ValueError):
                    score(CORPUS, evidence)

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

    def test_unpriced_cost(self):
        for basis in (None, '', ' ', 'unpriced'):
            with self.subTest(basis=basis):
                evidence = fixture()
                evidence['calls'][0]['pricing_basis'] = basis
                row = score(CORPUS, evidence)['results']['minimax-m3']
                self.assertEqual(row['unknown_cost_count'], 1)
                self.assertIsNone(row['api_equivalent_cost_usd'])

    def test_invalid_latency(self):
        for latency in (True, float('nan'), float('inf'), -1, '10'):
            with self.subTest(latency=latency):
                evidence = fixture()
                evidence['calls'][0]['gateway_latency_ms'] = latency
                with self.assertRaises(ValueError):
                    score(CORPUS, evidence)

    def test_integer_reservation(self):
        for reservation in (True, 600.0, '600'):
            with self.subTest(reservation=reservation):
                evidence = fixture()
                evidence['batch']['reservation_microusd'] = reservation
                with self.assertRaises(ValueError):
                    score(CORPUS, evidence)

    def test_exported_null_accounting(self):
        evidence = fixture()
        call = evidence['calls'][0]
        call.update(receipt_status='ambiguous', status='failed')
        for field in ('total_cost_microusd', 'input_tokens', 'output_tokens', 'cached_input_tokens', 'cache_write_tokens', 'reasoning_tokens'):
            call[field] = None
        row = score(CORPUS, evidence)['results']['minimax-m3']
        self.assertEqual(row['unknown_cost_count'], 1)
        self.assertEqual(row['unknown_usage_count'], 1)
        self.assertEqual(row['successes'], 19)

    def test_server_prompts(self):
        server = json.loads((ROOT / 'internal/modelevaluation/corpus.json').read_text())
        self.assertEqual(CORPUS['system'], server['system'])
        self.assertEqual({c['id']: c['prompt'] for c in CORPUS['cases']},
                         {c['id']: c['prompt'] for c in server['cases']})


if __name__ == '__main__':
    unittest.main()
