import copy
import hashlib
import json
import unittest

from prepare_implementation_evaluation import CASE
from test_inference_response_binding import TASK, RECEIPT, exported, raw
from test_implementation_oracle import evidence
from prepare_implementation_candidate import package
from verify_implementation_sandbox import candidate_response_bytes
from verify_implementation_execution import verify


def inputs():
    response = candidate_response_bytes({name: (CASE / ('reference/' + name)).read_text()
                                         for name in ('page.go', 'store.go')})
    execution = evidence()[1]
    execution['sandbox_lease']['task_id'] = TASK
    execution['sandbox_lease']['worktree_digest'] = package(response)['expected_worktree_digest']
    execution['response_sha256'] = hashlib.sha256(response).hexdigest()
    return response, exported(response), execution


class ImplementationExecutionTests(unittest.TestCase):
    def test_complete_bound_oracle_pass_without_model_attribution(self):
        response, call, execution = inputs()
        result = verify(response, raw(call), raw(execution), TASK, RECEIPT)
        self.assertTrue(result['observed_oracle_passed'])
        self.assertFalse(result['model_quality_measured'])
        self.assertFalse(result['provider_provenance_authenticated'])
        self.assertFalse(result['cost_verified'])

    def test_complete_failed_oracle_remains_observed_failure(self):
        response, call, execution = inputs()
        events = [json.loads(line) for line in execution['output'].splitlines()]
        for event in events:
            if event['Action'] == 'pass' and event.get('Test') in (None, 'TestPaginationContract', 'TestPaginationContract/second'):
                event['Action'] = 'fail'
        execution['output'] = '\n'.join(json.dumps(event) for event in events)
        execution['exit_code'] = 1
        result = verify(response, raw(call), raw(execution), TASK, RECEIPT)
        self.assertFalse(result['observed_oracle_passed'])
        self.assertEqual(result['failed_oracle_cases'], ['TestPaginationContract/second'])

    def test_rebound_response_source_task_and_claims_rejected(self):
        response, call, original = inputs()
        for field, value in [('task_id', RECEIPT), ('worktree_digest', 'sha256:' + 'a' * 64),
                             ('runtime', 'host'), ('status', 'running')]:
            execution = copy.deepcopy(original)
            execution['sandbox_lease'][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                verify(response, raw(call), raw(execution), TASK, RECEIPT)
        for field, value in [('response_sha256', 'a' * 64), ('provider_provenance_authenticated', True),
                             ('model_quality_measured', True), ('exit_code', True)]:
            execution = copy.deepcopy(original)
            execution[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                verify(response, raw(call), raw(execution), TASK, RECEIPT)

    def test_partial_or_contradictory_oracle_is_inconclusive(self):
        response, call, execution = inputs()
        execution['output'] = '\n'.join(line for line in execution['output'].splitlines()
                                       if json.loads(line).get('Test') != 'TestPaginationContract/maximum-size')
        with self.assertRaises(ValueError):
            verify(response, raw(call), raw(execution), TASK, RECEIPT)
        execution = inputs()[2]
        execution['exit_code'] = 1
        with self.assertRaises(ValueError):
            verify(response, raw(call), raw(execution), TASK, RECEIPT)
