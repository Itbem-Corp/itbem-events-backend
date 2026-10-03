import hashlib
import json
import unittest

from validate_implementation_response import MAX_RESPONSE_BYTES, validate


def response():
    return {'changes': [{'path': 'page.go', 'content': 'package pagination\n'},
                        {'path': 'store.go', 'content': 'package pagination\n'}]}


class ImplementationResponseTests(unittest.TestCase):
    def test_contract_success_is_not_correctness_or_execution(self):
        raw = json.dumps(response()).encode()
        report = validate(raw)
        self.assertTrue(report['response_contract_valid'])
        self.assertFalse(report['candidate_executed'])
        self.assertIsNone(report['implementation_correctness'])
        self.assertEqual(report['response_sha256'], hashlib.sha256(raw).hexdigest())
        self.assertEqual(set(report['files']), {'page.go', 'store.go'})

    def test_unexpected_or_repeated_paths_are_rejected(self):
        for path in ('../page.go', '/page.go', 'C:\\page.go', 'oracle/page_test.go',
                     'go.mod', 'PAGE.GO', ' page.go', 'store.go', None, []):
            with self.subTest(path=path):
                value = response()
                value['changes'][0]['path'] = path
                with self.assertRaises(ValueError):
                    validate(json.dumps(value).encode())

    def test_malformed_content_and_envelopes_are_rejected(self):
        for content in ('', ' ', '\x00', '\ud800', None, True, []):
            with self.subTest(content=repr(content)):
                value = response()
                value['changes'][0]['content'] = content
                with self.assertRaises(ValueError):
                    validate(json.dumps(value).encode())
        for raw in (b'{}', b'[]', b'{"changes":[],"extra":true}',
                    b'{"changes":[],"changes":[]}', b'\xff',
                    b'x' * (MAX_RESPONSE_BYTES + 1)):
            with self.subTest(raw=raw[:40]), self.assertRaises(ValueError):
                validate(raw)
