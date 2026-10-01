"""Offline quality gate for two scored, frozen-corpus evaluation reports.

No requests, credentials, admissions or policy writes occur in this tool.
Passing this synthetic gate is evidence for review, never automatic promotion.
"""
import argparse
import json
import math
from pathlib import Path


def compare(baseline, proposed):
    if not baseline.get('complete') or not proposed.get('complete'):
        raise ValueError('Both screenings must be complete.')
    old_hash = baseline['batch'].get('corpus_hash')
    if not old_hash or old_hash != proposed['batch'].get('corpus_hash'):
        raise ValueError('Comparisons require the same frozen corpus hash.')
    if baseline['results'].keys() != proposed['results'].keys():
        raise ValueError('Candidate groups must match.')
    result = {}
    for name, old in baseline['results'].items():
        new = proposed['results'][name]
        reasons = []
        old_cases = {row['case_id']: row for row in old['cases']}
        new_cases = {row['case_id']: row for row in new['cases']}
        if (old['denominator'] != 20 or new['denominator'] != 20
                or len(old_cases) != 20 or len(new_cases) != 20
                or old_cases.keys() != new_cases.keys()):
            raise ValueError('Retain the same twenty distinct case outcomes.')
        lost = [key for key in old_cases if old_cases[key]['success'] and not new_cases[key]['success']]
        if lost:
            reasons.append('previously successful cases regressed')
        for metric in ('clean_false_positives', 'clean_missing_or_invalid',
                       'errors_or_incomplete', 'truncations', 'unexpected_routes_or_fallbacks'):
            if new[metric] > old[metric]:
                reasons.append(metric + ' increased')
        # A broken baseline does not qualify a broken cheaper replacement.
        if new['successes'] != 20 or new['valid_json_count'] != 20:
            reasons.append('proposed screening must pass all twenty cases')
        old_cost = old.get('api_equivalent_cost_usd')
        new_cost = new.get('api_equivalent_cost_usd')
        for value in (old_cost, new_cost):
            if value is not None and (type(value) not in (int, float) or not math.isfinite(value) or value < 0):
                raise ValueError('Invalid comparison cost.')
        if old_cost is None or new_cost is None or old.get('unknown_cost_count') or new.get('unknown_cost_count'):
            reasons.append('cost coverage incomplete')
        elif new_cost >= old_cost:
            reasons.append('no measured cost reduction')
        result[name] = {
            'eligible_for_review': not reasons, 'reasons': reasons,
            'regressed_case_ids': lost,
            'baseline_cost_usd': old_cost, 'proposed_cost_usd': new_cost,
            'cost_reduction_percent': (100 * (old_cost-new_cost) / old_cost
                                       if old_cost and new_cost is not None else None),
            'baseline_latency_p95_ms': old.get('gateway_latency_p95_ms'),
            'proposed_latency_p95_ms': new.get('gateway_latency_p95_ms'),
            'baseline_output_tokens': old.get('total_tokens', {}).get('output_tokens'),
            'proposed_output_tokens': new.get('total_tokens', {}).get('output_tokens'),
        }
    return {'corpus_hash': old_hash, 'automatic_promotion': False,
            'screening_only': True, 'results': result}


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--baseline', type=Path, required=True)
    parser.add_argument('--proposed', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    result = compare(json.loads(args.baseline.read_text()), json.loads(args.proposed.read_text()))
    args.output.write_text(json.dumps(result, indent=2, allow_nan=False))
