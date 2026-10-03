import copy
import json
import unittest

from test_implementation_sandbox import controls, PACKAGE
from prepare_implementation_candidate import package_worktree_digest
from prepare_implementation_evaluation import CASE
from verify_implementation_oracle import verify


def evidence():
    values = controls()
    for index, value in enumerate(values):
        name = value['control']
        files = {path: (CASE / source).read_bytes().decode().replace('\r\n', '\n')
                 for path, source in {'go.mod': 'fixture/go.mod', 'page.go': name + '/page.go',
                                      'store.go': name + '/store.go', 'page_test.go': 'oracle/page_test.go'}.items()}
        value['provider_provenance_authenticated'] = False
        value['sandbox_lease']['task_id'] = f'00000000-0000-4000-8000-{index+1:012d}'
        value['sandbox_lease']['lease_id'] = f'00000000-0000-4000-8001-{index+1:012d}'
        value['sandbox_lease']['worktree_digest'] = package_worktree_digest(files)
    return values


def oracle_log(values):
    output = ''.join('test.go:1: implementation oracle execution evidence: ' + json.dumps(value) + '\n'
                     for value in values)
    identity = {'Package': PACKAGE, 'Test': 'TestImplementationPilotOracleDockerRoundTrip'}
    events = [dict(identity, Action='run')]
    events += [dict(identity, Action='output', Output=output[index:index+31])
               for index in range(0, len(output), 31)]
    events += [dict(identity, Action='pass')]
    return ('\n'.join(json.dumps(event) for event in events) + '\n').encode()


class OracleExecutionEvidenceTests(unittest.TestCase):
    def test_bound_execution_without_model_provenance_claim(self):
        report = verify(oracle_log(evidence()), 1)
        self.assertTrue(report['oracle_execution_entry_verified'])
        self.assertFalse(report['model_quality_measured'])
        self.assertFalse(report['provenance_authenticated'])

    def test_rebound_digest_reused_task_and_claims_rejected(self):
        original = evidence()
        for field, value in [('worktree_digest', 'sha256:' + 'a' * 64),
                             ('task_id', original[0]['sandbox_lease']['task_id']),
                             ('task_id', '00000000-0000-0000-0000-000000000000')]:
            changed = copy.deepcopy(original)
            changed[1]['sandbox_lease'][field] = value
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                verify(oracle_log(changed), 1)
        changed = copy.deepcopy(original)
        changed[1]['provider_provenance_authenticated'] = True
        with self.assertRaises(ValueError):
            verify(oracle_log(changed), 1)

    def test_missing_oracle_case_rejected(self):
        changed = evidence()
        events = [json.loads(line) for line in changed[1]['output'].splitlines()]
        changed[1]['output'] = '\n'.join(json.dumps(event) for event in events
                                         if event.get('Test') != 'TestPaginationContract/maximum-size')
        with self.assertRaises(ValueError):
            verify(oracle_log(changed), 1)
