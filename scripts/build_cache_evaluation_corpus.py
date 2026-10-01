"""Build the sealed synthetic corpus and separate offline answer key. No network."""
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
VERSION = 'synthetic-prefix-cache-20-2026-10-01-v1'

def build():
    original = json.loads((ROOT / 'scripts/model-evaluation-screening-corpus.json').read_text(encoding='utf-8'))
    reference = (ROOT / 'scripts/cache-evaluation-reference.md').read_text(encoding='utf-8').strip()
    cases = []
    for index, source in enumerate(original['cases'][:10]):
        # Balance within-pair order; dispatch still serializes all three routes.
        conditions = ('control', 'shared') if index % 2 == 0 else ('shared', 'control')
        for condition in conditions:
            partition = 'shared-000' if condition == 'shared' else f'unique-{index:03d}'
            prefix = f'Experiment partition: {partition}\n\n{reference}\n\nCase evidence:\n'
            cases.append({
                'id': source['id'] + '-' + condition,
                'category': source['category'], 'pair_id': source['id'],
                'cache_condition': condition,
                'prefix_sha256': hashlib.sha256(prefix.encode('utf-8')).hexdigest(),
                'prompt': prefix + source['prompt'], 'expected': source['expected'],
            })
    offline = {**original, 'corpus_version': VERSION,
               'purpose': 'Matched synthetic prefix reuse experiment; no general cache certification',
               'cache_experiment': {'pairs': 10, 'conditions': ['control', 'shared'],
                   'control_is_guaranteed_cold': False, 'native_counters_required': True,
                   'reference_sha256': hashlib.sha256(reference.encode('utf-8')).hexdigest()},
               'cases': cases}
    server = {'system': original['system'],
              'cases': [{'id': case['id'], 'prompt': case['prompt']} for case in cases]}
    return server, offline

if __name__ == '__main__':
    server, offline = build()
    for path, value in ((ROOT / 'internal/modelevaluation/cache_corpus.json', server),
                        (ROOT / 'scripts/model-evaluation-cache-corpus.json', offline)):
        path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + '\n', encoding='utf-8')
