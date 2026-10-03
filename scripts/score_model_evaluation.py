from pathlib import Path
import argparse
import hashlib
import json
import math
import statistics
from evaluation_report import decode_json, publish_report, read_input

CANDIDATES = {
    'minimax-m3': ('minimax', 'MiniMax-M3'),
    'deepseek-flash-high': ('deepseek', 'deepseek-flash'),
    'luna-high': ('openrouter', 'openai/gpt-6-luna'),
}

def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False)

SCREENING_VERSION = 'synthetic-screening-20-2026-09-30-v1'
CACHE_VERSION = 'synthetic-prefix-cache-20-2026-10-01-v1'

def decode_model_answer(raw):
    return decode_json(raw)

def score(corpus, evidence):
    version = corpus.get('corpus_version', SCREENING_VERSION)
    references = {SCREENING_VERSION: 'model-evaluation-screening-corpus.json',
                  CACHE_VERSION: 'model-evaluation-cache-corpus.json'}
    if version not in references:
        raise ValueError('Unrecognized evaluation corpus version.')
    trusted, _ = read_input(Path(__file__).resolve().parent / references[version])
    # Canonical JSON retains the boolean/numeric distinction that dict equality
    # loses (True == 1), and compares the complete list before indexing cases.
    if canonical(corpus) != canonical(trusted):
        raise ValueError('Evaluation corpus differs from the frozen answer key.')
    if evidence['batch'].get('corpus_version') != version:
        raise ValueError('Evidence batch does not match the frozen corpus version.')
    cases = {case['id']: case for case in corpus['cases']}
    calls = evidence['calls']
    batch = evidence['batch']
    budget = batch.get('budget_microusd')
    reservation = batch.get('reservation_microusd')
    if type(budget) is not int or budget != 1_000_000 or type(reservation) is not int or not 0 < reservation <= budget:
        raise ValueError('Missing or invalid normal USD 1 batch reservation.')
    if len(cases) != 20 or len(calls) != 60:
        raise ValueError('Expected exactly 20 cases and 60 recorded outcomes, including failures.')
    cache_profile = version == CACHE_VERSION
    clean_denominator = 10 if cache_profile else 5
    if sum(case.get('category') == 'review_clean' for case in cases.values()) != clean_denominator:
        raise ValueError('Unexpected number of clean review cases.')
    seen = set()
    task_ids = set()
    outcome_ids = {'run_id': set(), 'receipt_id': set()}
    for call in calls:
        key = (call['case_id'], call['candidate'])
        if key in seen or key[0] not in cases or key[1] not in CANDIDATES:
            raise ValueError('Unexpected or duplicate case/candidate binding.')
        seen.add(key)
        task_id = call.get('task_id')
        if not isinstance(task_id, str) or not task_id.strip() or task_id in task_ids:
            raise ValueError('Missing or duplicate task identity.')
        task_ids.add(task_id)
        for field, recorded in outcome_ids.items():
            identity = call.get(field)
            if identity is None or identity == '':
                if call.get('receipt_status') == 'accepted':
                    raise ValueError('Accepted result requires run and receipt identity.')
                continue
            if not isinstance(identity, str) or not identity.strip() or identity != identity.strip():
                raise ValueError('Invalid evaluation outcome identity.')
            if identity in recorded:
                raise ValueError('Reused evaluation outcome identity.')
            recorded.add(identity)
        prompt = corpus['system'].strip() + '\n\n' + cases[key[0]]['prompt'].strip()
        if call['prompt_sha256'] != hashlib.sha256(prompt.encode('utf-8')).hexdigest():
            raise ValueError('Prompt hash does not match the reviewed corpus.')
    summary = {}
    for candidate, expected_route in CANDIDATES.items():
        group = [call for call in calls if call['candidate'] == candidate]
        if len(group) != 20:
            raise ValueError('Each candidate must retain all 20 outcomes.')
        success = valid_json = false_positive = invalid_clean = errors = truncations = attribution_errors = 0
        costs = 0
        unknown_costs = 0
        unknown_usage = 0
        latencies = []
        tokens = {key: 0 for key in ('input_tokens', 'output_tokens', 'cached_input_tokens', 'cache_write_tokens', 'reasoning_tokens')}
        unknown_tokens = {key: 0 for key in tokens}
        scored = []
        categories = {}
        failure_counts = {reason: 0 for reason in (
            'task_not_completed', 'receipt_not_accepted', 'unexpected_route',
            'truncated', 'result_error', 'invalid_json', 'answer_mismatch')}
        for call in group:
            case = cases[call['case_id']]
            answer = None
            try:
                answer = decode_model_answer(call.get('final_answer', ''))
                format_ok = isinstance(answer, dict)
            except (ValueError, TypeError):
                format_ok = False
            valid_json += format_ok
            receipt_ok = call.get('receipt_status') == 'accepted'
            if receipt_ok:
                routes = decode_json(call.get('sealed_routes_json', '[]'))
                effort = '' if candidate == 'minimax-m3' else 'high'
                if len(routes) != 1 or (routes[0].get('provider'), routes[0].get('model')) != expected_route or routes[0].get('reasoning_enabled') is not True or routes[0].get('reasoning_effort', '') != effort:
                    raise ValueError('Accepted call has an unexpected sealed reasoning route.')
            actual_route = (call.get('actual_provider', '').lower(), call.get('actual_model', ''))
            attributed = actual_route == expected_route
            attribution_errors += receipt_ok and not attributed
            truncated = call.get('finish_reason') in ('length', 'max_tokens', 'max_output_tokens')
            truncations += truncated
            outcome_ok = call.get('status') == 'completed' and receipt_ok and attributed and not truncated and not call.get('result_error')
            answer_matches = format_ok and canonical(answer) == canonical(case['expected'])
            matched = outcome_ok and answer_matches
            failure_flags = {
                'task_not_completed': call.get('status') != 'completed',
                'receipt_not_accepted': not receipt_ok,
                'unexpected_route': not attributed,
                'truncated': truncated,
                'result_error': bool(call.get('result_error')),
                'invalid_json': not format_ok,
                'answer_mismatch': format_ok and not answer_matches,
            }
            failure_reasons = [reason for reason, failed in failure_flags.items() if failed]
            for reason in failure_reasons:
                failure_counts[reason] += 1
            success += matched
            errors += not outcome_ok
            category = categories.setdefault(case['category'], {
                'denominator': 0, 'successes': 0, 'valid_json_count': 0,
                'errors_or_incomplete': 0})
            category['denominator'] += 1
            category['successes'] += int(bool(matched))
            category['valid_json_count'] += int(format_ok)
            category['errors_or_incomplete'] += int(not outcome_ok)
            if case['category'] == 'review_clean':
                false_positive += format_ok and answer.get('bug') is True
                invalid_clean += not outcome_ok or not format_ok
            cost = call.get('total_cost_microusd')
            basis = call.get('pricing_basis')
            cost_verified = call.get('receipt_status') in ('accepted', 'rejected') and cost is not None and isinstance(basis, str) and bool(basis.strip()) and basis != 'unpriced'
            if cost_verified:
                if type(cost) is not int or cost < 0:
                    raise ValueError('Invalid ledger cost.')
                costs += cost
            else:
                unknown_costs += 1
            latency = call.get('gateway_latency_ms')
            if latency is not None and (type(latency) not in (int, float) or not math.isfinite(latency) or latency < 0):
                raise ValueError('Invalid latency measurement.')
            if latency is not None and latency > 0:
                latencies.append(latency)
            usage_verified = True
            for key in tokens:
                value = call.get(key)
                if call.get('receipt_status') in ('accepted', 'rejected') and value is not None:
                    if type(value) is not int or value < 0:
                        raise ValueError('Invalid token accounting.')
                    tokens[key] += value
                else:
                    unknown_tokens[key] += 1
                    usage_verified = False
            if not usage_verified:
                unknown_usage += 1
            scored.append({'case_id': call['case_id'], 'category': case['category'], 'task_id': call['task_id'], 'run_id': call.get('run_id'), 'receipt_id': call.get('receipt_id'), 'success': bool(matched), 'valid_json': format_ok, 'attributed': attributed, 'truncated': truncated, 'status': call.get('status'), 'failure_reasons': failure_reasons})
        ordered = sorted(latencies)
        for category in categories.values():
            category['failures'] = category['denominator'] - category['successes']
            category['success_rate'] = category['successes'] / category['denominator']
        summary[candidate] = {
            'successes': success, 'denominator': 20, 'success_rate': success / 20,
            'by_category': categories,
            'failure_reason_counts': failure_counts,
            'valid_json_count': valid_json, 'valid_json_rate': valid_json / 20,
            'clean_false_positives': false_positive, 'clean_denominator': clean_denominator,
            'clean_false_positive_rate': false_positive / clean_denominator, 'clean_missing_or_invalid': invalid_clean,
            'errors_or_incomplete': errors, 'truncations': truncations,
            'unexpected_routes_or_fallbacks': attribution_errors,
            'gateway_latency_samples': len(latencies),
            'gateway_latency_median_ms': statistics.median(latencies) if latencies else None,
            'gateway_latency_p95_ms': ordered[math.ceil(.95 * len(ordered)) - 1] if ordered else None,
            'p95_method': 'nearest-rank over available positive receipt latencies',
            'verified_tokens': tokens, 'unknown_usage_count': unknown_usage,
            'unknown_token_counts': unknown_tokens,
            'total_tokens': {key: None if unknown_tokens[key] else value for key, value in tokens.items()},
            'verified_api_equivalent_cost_usd': costs / 1_000_000,
            'unknown_cost_count': unknown_costs,
            'api_equivalent_cost_usd': None if unknown_costs else costs / 1_000_000,
            'tokens_per_case': {key: None if unknown_tokens[key] else value / 20 for key, value in tokens.items()},
            'tokens_per_success': {key: value / success if success and not unknown_tokens[key] else None for key, value in tokens.items()},
            'api_equivalent_cost_per_case_usd': None if unknown_costs else costs / 1_000_000 / 20,
            'api_equivalent_cost_per_success_usd': costs / 1_000_000 / success if success and not unknown_costs else None,
            'cases': scored,
        }
    reported_complete = batch.get('status') == 'completed'
    incomplete = sum(call.get('status') != 'completed' or call.get('receipt_status') != 'accepted'
                     for call in calls)
    return {'batch': batch, 'screening_only': True,
            'reported_complete': reported_complete, 'incomplete_outcome_count': incomplete,
            'complete': reported_complete and incomplete == 0,
            'limitation': '20 synthetic cases per model; not general quality certification or multi-file implementation validation. MiniMax values are API-equivalent, not a subscription invoice.',
            'results': summary}

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--corpus', required=True, type=Path)
    parser.add_argument('--evidence', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    corpus, corpus_hash = read_input(args.corpus)
    evidence, evidence_hash = read_input(args.evidence)
    report = score(corpus, evidence)
    report['input_sha256'] = {'corpus': corpus_hash, 'evidence': evidence_hash}
    publish_report(args.output, report)
