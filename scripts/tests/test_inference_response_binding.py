import hashlib
import json
import unittest

from verify_inference_response_binding import verify

TASK = '00000000-0000-4000-8000-000000000001'
RECEIPT = '00000000-0000-4000-8000-000000000002'
RUN = '00000000-0000-4000-8000-000000000003'


def exported(response):
    return {'task_id': TASK, 'receipt_id': RECEIPT, 'receipt_run_id': RUN,
            'status': 'completed', 'receipt_status': 'accepted',
            'response_sha256': hashlib.sha256(response).hexdigest(), 'response_bytes': len(response)}


def raw(call):
    return json.dumps(call).encode()


class ResponseBindingTests(unittest.TestCase):
    def test_exact_utf8_empty_and_whitespace_answers_without_provenance_claim(self):
        for response in (b'', b'{}\n', ' válido '.encode()):
            r = verify(response, raw(exported(response)), TASK, RECEIPT)
            self.assertTrue(r['response_receipt_binding_consistent'])
            self.assertFalse(r['provider_provenance_authenticated'])
            self.assertFalse(r['cost_verified'])
            self.assertIsNone(r['implementation_correctness'])

    def test_changed_bytes_historical_digest_and_rebound_identity_rejected(self):
        response = b'{}'
        for field, value in [('response_sha256', None), ('response_bytes', None),
                             ('response_bytes', True), ('response_bytes', 2.0),
                             ('receipt_status', 'rejected'), ('status', 'running'),
                             ('task_id', RECEIPT), ('receipt_id', TASK),
                             ('receipt_run_id', '00000000-0000-0000-0000-000000000000')]:
            call = exported(response)
            call[field] = value
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                verify(response, raw(call), TASK, RECEIPT)
        with self.assertRaises(ValueError):
            verify(response + b'\n', raw(exported(response)), TASK, RECEIPT)

    def test_duplicate_fields_oversize_and_invalid_utf8_rejected(self):
        for response, call in [(b'{}', b'{"task_id":"x","task_id":"y"}'),
                               (b'x' * 65537, b'{}'), (b'{}', b'x' * 65537),
                               (bytes([255]), b'{}')]:
            with self.assertRaises(ValueError):
                verify(response, call, TASK, RECEIPT)
