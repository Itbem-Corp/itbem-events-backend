from pathlib import Path
import argparse
import hashlib
import json
import math
import statistics

CANDIDATES = {
    'minimax-m3': ('minimax', 'MiniMax-M3'),
    'deepseek-flash-high': ('deepseek', 'deepseek-flash'),
    'luna-high': ('openrouter', 'openai/gpt-6-luna'),
}

def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False)

def decode_model_answer(raw):
    def unique_object(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('Duplicate model answer field.')
            result[key] = value
        return result

    def reject_constant(value):
        raise ValueError('Non-JSON numeric constant in model answer.')

    return json.loads(raw, object_pairs_hook=unique_object, parse_constant=reject_constant)

def score(corpus, evidence):
    cases = {case['id']: case for case in corpus['cases']}
    calls = evidence['calls']
    batch = evidence['batch']
    budget = batch.get('budget_microusd')
    reservation = batch.get('reservation_microusd')
    if type(budget) is not int or budget != 1_000_000 or type(reservation) is not int or not 0 < reservation <= budget:
        raise ValueError('Missing or invalid normal USD 1 batch reservation.')
    if len(cases) != 20 or len(calls) != 60:
        raise ValueError('Expected exactly 20 cases and 60 recorded outcomes, including failures.')
    cache_profile = corpus.get('corpus_version') == 'synthetic-prefix-cache-20-2026-10-01-v1'
    clean_denominator = 10 if cache_profile else 5
    if cache_profile and batch.get('corpus_version') != corpus['corpus_version']:
        raise ValueError('Cache evidence does not match the versioned corpus.')
    if sum(case.get('category') == 'review_clean' for case in cases.values()) != clean_denominator:
        raise ValueError('Unexpected number of clean review cases.')
    seen = set()
    task_ids = set()
    for call in calls:
        key = (call['case_id'], call['candidate'])
        if key in seen or key[0] not in cases or key[1] not in CANDIDATES:
            raise ValueError('Unexpected or duplicate case/candidate binding.')
        seen.add(key)
        task_id = call.get('task_id')
        if not isinstance(task_id, str) or not task_id.strip() or task_id in task_ids:
            raise ValueError('Missing or duplicate task identity.')
        task_ids.add(task_id)
        if call.get('receipt_status') == 'accepted' and (not call.get('run_id') or not call.get('receipt_id')):
            raise ValueError('Accepted result requires run and receipt identity.')
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
                routes = json.loads(call.get('sealed_routes_json', '[]'))
                effort = '' if candidate == 'minimax-m3' else 'high'
                if len(routes) != 1 or (routes[0].get('provider'), routes[0].get('model')) != expected_route or routes[0].get('reasoning_enabled') is not True or routes[0].get('reasoning_effort', '') != effort:
                    raise ValueError('Accepted call has an unexpected sealed reasoning route.')
            actual_route = (call.get('actual_provider', '').lower(), call.get('actual_model', ''))
            attributed = actual_route == expected_route
            attribution_errors += receipt_ok and not attributed
            truncated = call.get('finish_reason') in ('length', 'max_tokens', 'max_output_tokens')
            truncations += truncated
            outcome_ok = call.get('status') == 'completed' and receipt_ok and attributed and not truncated and not call.get('result_error')
            matched = outcome_ok and format_ok and canonical(answer) == canonical(case['expected'])
            success += matched
            errors += not outcome_ok
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
            scored.append({'case_id': call['case_id'], 'task_id': call['task_id'], 'run_id': call.get('run_id'), 'receipt_id': call.get('receipt_id'), 'success': bool(matched), 'valid_json': format_ok, 'attributed': attributed, 'truncated': truncated, 'status': call.get('status')})
        ordered = sorted(latencies)
        summary[candidate] = {
            'successes': success, 'denominator': 20, 'success_rate': success / 20,
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
    return {'batch': evidence['batch'], 'screening_only': True, 'complete': evidence['batch']['status'] == 'completed', 'limitation': '20 synthetic cases per model; not general quality certification or multi-file implementation validation. MiniMax values are API-equivalent, not a subscription invoice.', 'results': summary}

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--corpus', required=True, type=Path)
    parser.add_argument('--evidence', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    report = score(json.loads(args.corpus.read_text(encoding='utf-8-sig')), json.loads(args.evidence.read_text(encoding='utf-8-sig')))
    args.output.write_text(json.dumps(report, indent=2, ensure_ascii=False, allow_nan=False), encoding='utf-8')
