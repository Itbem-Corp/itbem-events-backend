import copy
import hashlib
import unittest

from test_implementation_oracle import evidence, oracle_log
from prepare_implementation_evaluation import CASE
from verify_implementation_sandbox import candidate_response_bytes
from verify_implementation_candidate_execution import verify


def candidate_evidence():
    values = evidence()
    for value in values:
        name = value['control']
        files = {path: (CASE / (name + '/' + path)).read_text().replace('\r\n', '\n')
                 for path in ('page.go', 'store.go')}
        value['response_sha256'] = hashlib.sha256(candidate_response_bytes(files)).hexdigest()
    return values


def candidate_log(values):
    return oracle_log(values, 'TestImplementationCandidateResponseDockerRoundTrip',
                      'implementation candidate execution evidence: ')


class CandidateExecutionEvidenceTests(unittest.TestCase):
    def test_exact_response_and_source_bound_without_provider_claim(self):
        report = verify(candidate_log(candidate_evidence()), 1)
        self.assertTrue(report['candidate_execution_entry_verified'])
        self.assertNotIn('oracle_execution_entry_verified', report)
        self.assertFalse(report['model_quality_measured'])
        self.assertFalse(report['provenance_authenticated'])

    def test_missing_rebound_and_noncanonical_response_digest_rejected(self):
        original = candidate_evidence()
        for digest in (None, '', 'a' * 64, original[0]['response_sha256'],
                       original[1]['response_sha256'].upper()):
            changed = copy.deepcopy(original)
            changed[1]['response_sha256'] = digest
            with self.subTest(digest=digest), self.assertRaises(ValueError):
                verify(candidate_log(changed), 1)
        changed = copy.deepcopy(original)
        changed[1]['sandbox_lease']['worktree_digest'] = 'sha256:' + 'a' * 64
        with self.assertRaises(ValueError):
            verify(candidate_log(changed), 1)

    def test_fixed_go_envelope_escaping(self):
        raw = candidate_response_bytes({'page.go': '<&>\u2028\u2029', 'store.go': 'x'})
        self.assertEqual(raw, br'{"changes":[{"content":"\u003c\u0026\u003e\u2028\u2029","path":"page.go"},{"content":"x","path":"store.go"}]}')

    def test_missing_repetition_reused_task_and_false_provenance_rejected(self):
        original = candidate_evidence()
        with self.assertRaises(ValueError):
            verify(candidate_log(original), 2)
        for field, value in [('task_id', original[0]['sandbox_lease']['task_id']),
                             ('lease_id', original[0]['sandbox_lease']['lease_id'])]:
            changed = copy.deepcopy(original)
            changed[1]['sandbox_lease'][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                verify(candidate_log(changed), 1)
        changed = copy.deepcopy(original)
        changed[1]['provider_provenance_authenticated'] = True
        with self.assertRaises(ValueError):
            verify(candidate_log(changed), 1)
