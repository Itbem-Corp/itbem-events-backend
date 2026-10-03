"""Offline matched-prefix analysis. Reads exported receipts; never calls a model."""
import argparse
import json
from pathlib import Path

from build_cache_evaluation_corpus import VERSION, build
from score_model_evaluation import CANDIDATES, score
from evaluation_report import decode_json, publish_report, read_input

def integer(value):
    return type(value) is int and value >= 0

def components(call):
    basis = call.get('pricing_basis')
    if call.get('receipt_status') not in ('accepted', 'rejected') or not isinstance(basis, str) or not basis.strip() or basis == 'unpriced':
        return None
    if not all(integer(call.get(key)) for key in ('input_tokens', 'output_tokens', 'total_cost_microusd')):
        return None
    try:
        snapshot = decode_json(call['pricing_snapshot_json'])
        rates = snapshot['rates_microusd_per_million']
    except (KeyError, TypeError, ValueError):
        return None
    rate_keys = ('input', 'cached', 'cache_write', 'output')
    if not all(integer(rates.get(key + '_microusd_per_million')) for key in rate_keys):
        raise ValueError('Invalid immutable price snapshot.')
    cached, written = call.get('cached_input_tokens'), call.get('cache_write_tokens')
    if any(value is not None and not integer(value) for value in (cached, written)):
        raise ValueError('Invalid cache usage.')
    uncached = call['input_tokens'] - (cached or 0) - (written or 0)
    if uncached < 0:
        raise ValueError('Cache read/write tokens exceed total input.')
    tokens = (uncached, cached or 0, written or 0, call['output_tokens'])
    costs = {key: (count * rates[key + '_microusd_per_million'] + 500_000) // 1_000_000
             for key, count in zip(rate_keys, tokens)}
    if sum(costs.values()) != call['total_cost_microusd']:
        raise ValueError('Reconstructed cost does not match historical ledger.')
    return {'input_microusd': sum(costs[key] for key in rate_keys[:3]),
            'output_microusd': costs['output'], 'components_microusd': costs,
            'cache_read_unknown': cached is None, 'cache_write_unknown': written is None}

def analyze(corpus, evidence):
    # Bind pair metadata and expected answers to the reviewed generator, not to
    # arbitrary annotations in an uploaded file. Hashes are checked by scorer.
    _, trusted = build()
    if corpus != trusted or evidence['batch'].get('corpus_version') != VERSION:
        raise ValueError('Unrecognized or modified controlled corpus.')
    screening = score(corpus, evidence)
    case_by_id = {case['id']: case for case in corpus['cases']}
    result = {}
    for candidate in CANDIDATES:
        rows = [call for call in evidence['calls'] if call['candidate'] == candidate]
        rate_snapshots = {json.dumps(decode_json(call['pricing_snapshot_json'])['rates_microusd_per_million'], sort_keys=True)
                          for call in rows if components(call) is not None}
        if len(rate_snapshots) > 1:
            raise ValueError('Experimental conditions have different historical rates.')
        scores = {row['case_id']: row for row in screening['results'][candidate]['cases']}
        totals = {}
        for condition in ('control', 'shared'):
            group = [call for call in rows if case_by_id[call['case_id']]['cache_condition'] == condition]
            costs = [components(call) for call in group]
            def native(key):
                values = [call.get(key) if call.get('receipt_status') in ('accepted', 'rejected') else None for call in group]
                return {'known_sum': sum(value for value in values if integer(value)),
                        'unknown_count': sum(value is None for value in values),
                        'total': sum(values) if all(integer(value) for value in values) else None}
            totals[condition] = {
                'calls': len(group), 'successes': sum(scores[call['case_id']]['success'] for call in group),
                'input_tokens': native('input_tokens'), 'output_tokens': native('output_tokens'),
                'cached_input_tokens': native('cached_input_tokens'), 'cache_write_tokens': native('cache_write_tokens'),
                'input_cost_microusd': sum(cost['input_microusd'] for cost in costs) if all(cost is not None for cost in costs) else None,
                'output_cost_microusd': sum(cost['output_microusd'] for cost in costs) if all(cost is not None for cost in costs) else None,
                'unknown_cost_components': sum(cost is None for cost in costs),
            }
        pairs = []
        by_id = {call['case_id']: call for call in rows}
        for pair in sorted({case['pair_id'] for case in corpus['cases']}):
            a, b = by_id[pair + '-control'], by_id[pair + '-shared']
            ac, bc = components(a), components(b)
            pairs.append({'pair_id': pair,
                          'both_correct': scores[a['case_id']]['success'] and scores[b['case_id']]['success'],
                          'shared_quality_regression': scores[a['case_id']]['success'] and not scores[b['case_id']]['success'],
                          'input_savings_microusd': ac['input_microusd'] - bc['input_microusd'] if ac and bc else None,
                          'output_delta_microusd': bc['output_microusd'] - ac['output_microusd'] if ac and bc else None})
        control, shared = totals['control'], totals['shared']
        complete_cost = control['input_cost_microusd'] is not None and shared['input_cost_microusd'] is not None
        savings = control['input_cost_microusd'] - shared['input_cost_microusd'] if complete_cost else None
        output_delta = shared['output_cost_microusd'] - control['output_cost_microusd'] if complete_cost else None
        total_savings = savings - output_delta if complete_cost else None
        read_complete = not any(row['cached_input_tokens']['unknown_count'] for row in totals.values())
        write_complete = not any(row['cache_write_tokens']['unknown_count'] for row in totals.values())
        read_gain = (shared['cached_input_tokens']['total'] - control['cached_input_tokens']['total']) if read_complete else None
        quality_ok = all(pair['both_correct'] for pair in pairs)
        qualified = (screening['complete'] and quality_ok and read_complete and write_complete
                     and savings is not None and savings > 0 and total_savings > 0
                     and read_gain is not None and read_gain > 0)
        result[candidate] = {'conditions': totals, 'pairs': pairs,
                             'input_savings_microusd': savings, 'output_delta_microusd': output_delta,
                             'total_savings_microusd': total_savings, 'cache_read_gain_tokens': read_gain,
                             'observed_shared_cache_read': shared['cached_input_tokens']['known_sum'] > 0,
                             'all_pairs_correct': quality_ok, 'qualified_with_complete_accounting': qualified,
                             'warmup_included_in_totals': True}
    return {'batch': evidence['batch'], 'results': result, 'screening': screening,
            'limitation': 'One balanced serial synthetic run; control is not guaranteed cold. Warmup is included. Unknown native counters stay unknown; cost reconstruction uses historical conservative ledger operands. Positive reads prove observed reuse, not a guaranteed hit rate, native prefix token count, or invoice savings. No automatic policy changes.'}

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--corpus', type=Path, required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    corpus, corpus_hash = read_input(args.corpus)
    evidence, evidence_hash = read_input(args.evidence)
    report = analyze(corpus, evidence)
    report['input_sha256'] = {'corpus': corpus_hash, 'evidence': evidence_hash}
    publish_report(args.output, report)
