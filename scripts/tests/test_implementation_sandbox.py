import copy
import json
import unittest

from verify_implementation_sandbox import MARKER, PACKAGE, SUBTESTS, TEST, verify


def controls():
    result = []
    for index, name in enumerate(('fixture', 'reference')):
        events = [{'Package': 'synthetic-pagination', 'Action': 'pass',
                   'Test': 'TestPaginationContract/' + case} for case in sorted(SUBTESTS)]
        if name == 'fixture':
            events = [{'Package': 'synthetic-pagination', 'Action': 'fail',
                       'Test': 'TestPaginationContract/second'}]
        ordered = [{'Package': 'synthetic-pagination', 'Action': 'start'},
                   {'Package': 'synthetic-pagination', 'Action': 'run', 'Test': 'TestPaginationContract'}]
        for event in events:
            ordered.append(dict(event, Action='run'))
            ordered.append(event)
        terminal = 'fail' if name == 'fixture' else 'pass'
        ordered += [{'Package': 'synthetic-pagination', 'Action': terminal, 'Test': 'TestPaginationContract'},
                    {'Package': 'synthetic-pagination', 'Action': terminal}]
        events = ordered
        result.append({'case': 'pagination-v1', 'control': name, 'model_quality_measured': False,
                       'exit_code': 1 if name == 'fixture' else 0,
                       'output': '\n'.join(json.dumps(e) for e in events),
                       'sandbox_lease': {'runtime': 'docker', 'isolation_mode': 'docker_container',
                           'status': 'completed', 'task_id': 'implementation-control-' + name,
                           'lease_id': f'00000000-0000-0000-0000-{index:012d}',
                           'worktree_digest': 'sha256:' + 'a' * 64,
                           'started_at': '2026-10-03T00:00:00Z', 'finished_at': '2026-10-03T00:00:01Z'}})
    return result


def log(values, end='pass'):
    text = ''.join('    test.go:1: ' + MARKER + json.dumps(value) + '\n' for value in values)
    events = [{'Package': PACKAGE, 'Test': TEST, 'Action': 'run'}]
    events += [{'Package': PACKAGE, 'Test': TEST, 'Action': 'output', 'Output': text[i:i+57]}
               for i in range(0, len(text), 57)]
    events += [{'Package': PACKAGE, 'Test': TEST, 'Action': end}]
    return ('\n'.join(json.dumps(e) for e in events) + '\n').encode()


class SandboxEvidenceTests(unittest.TestCase):
    def test_outcomes_require_runs_and_package_completion(self):
        for mode in ('missing-run', 'missing-terminal', 'contradictory-terminal'):
            values = controls()
            events = [json.loads(row) for row in values[1]['output'].splitlines()]
            if mode == 'missing-run':
                events = [event for event in events if event['Action'] != 'run']
            elif mode == 'missing-terminal':
                events.pop()
            else:
                events[-1]['Action'] = 'fail'
            values[1]['output'] = '\n'.join(json.dumps(event) for event in events)
            with self.assertRaises(ValueError):
                verify(log(values), 1)

    def test_fragmented_output_reassembles_without_model_quality_claim(self):
        report = verify(log(controls()), 1)
        self.assertTrue(report['sandbox_controls_verified'])
        self.assertFalse(report['model_quality_measured'])
        self.assertFalse(report['provenance_authenticated'])

    def test_missing_repetition_and_reused_lease_are_rejected(self):
        raw = log(controls())
        for evidence, repetitions in ((raw, 2), (raw + raw, 2), (log(controls(), 'skip'), 1)):
            with self.assertRaises(ValueError):
                verify(evidence, repetitions)

    def test_oracle_and_isolation_mutations_are_rejected(self):
        original = controls()
        mutations = []
        for field, value in [('runtime', 'process'), ('worktree_digest', ''), ('status', 'running')]:
            changed = copy.deepcopy(original)
            changed[1]['sandbox_lease'][field] = value
            mutations.append(changed)
        changed = copy.deepcopy(original)
        changed[1]['output'] = changed[1]['output'].split('\n')[0]
        mutations.append(changed)
        changed = copy.deepcopy(original)
        changed[0]['exit_code'] = 0
        mutations.append(changed)
        for values in mutations:
            with self.assertRaises(ValueError):
                verify(log(values), 1)
