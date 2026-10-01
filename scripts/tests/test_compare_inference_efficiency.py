import copy
import unittest
from compare_inference_efficiency import compare


def report(cost):
    group = {'denominator': 20, 'successes': 20, 'valid_json_count': 20,
             'clean_false_positives': 0, 'clean_missing_or_invalid': 0,
             'errors_or_incomplete': 0, 'truncations': 0,
             'unexpected_routes_or_fallbacks': 0, 'unknown_cost_count': 0,
             'api_equivalent_cost_usd': cost,
             'cases': [{'case_id': str(i), 'success': True} for i in range(20)]}
    return {'complete': True, 'batch': {'corpus_hash': 'frozen'}, 'results': {'fixture': group}}


class EfficiencyGateTests(unittest.TestCase):
    def test_cheaper_complete_quality_is_reviewable(self):
        result = compare(report(.2), report(.1))
        self.assertTrue(result['results']['fixture']['eligible_for_review'])
        self.assertEqual(result['results']['fixture']['cost_reduction_percent'], 50)
        self.assertFalse(result['automatic_promotion'])

    def test_aggregate_accuracy_cannot_hide_a_regressed_case(self):
        proposed = report(.1)
        proposed['results']['fixture']['cases'][0]['success'] = False
        self.assertFalse(compare(report(.2), proposed)['results']['fixture']['eligible_for_review'])

    def test_unknown_cost_and_failed_quality_never_qualify(self):
        for field, value in [('api_equivalent_cost_usd', None), ('unknown_cost_count', 1),
                             ('truncations', 1), ('clean_false_positives', 1), ('successes', 19)]:
            proposed = report(.1)
            proposed['results']['fixture'][field] = value
            self.assertFalse(compare(report(.2), proposed)['results']['fixture']['eligible_for_review'])

    def test_incomplete_or_changed_corpus_and_bad_cost_reject(self):
        for mutate in (lambda p: p.update(complete=False),
                       lambda p: p['batch'].update(corpus_hash='other'),
                       lambda p: p['results']['fixture'].update(api_equivalent_cost_usd=float('nan')),
                       lambda p: p['results']['fixture'].update(cases=[{'case_id':'0','success':True}]*20)):
            proposed = copy.deepcopy(report(.1))
            mutate(proposed)
            with self.assertRaises(ValueError):
                compare(report(.2), proposed)


if __name__ == '__main__':
    unittest.main()
