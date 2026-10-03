"""Compare a retained final answer with one exported gateway receipt; no authentication."""
import argparse
import hashlib
from pathlib import Path
import uuid

from evaluation_report import decode_json, publish_report
from validate_implementation_response import MAX_RESPONSE_BYTES

MAX_CALL_BYTES = 65536


def canonical_id(value):
    if not isinstance(value, str):
        raise ValueError('Expected a canonical nonzero UUID.')
    try:
        identity = uuid.UUID(value)
    except ValueError:
        raise ValueError('Expected a canonical nonzero UUID.') from None
    if not identity.int or str(identity) != value:
        raise ValueError('Expected a canonical nonzero UUID.')
    return value


def verify(response, exported_call, task_id, receipt_id):
    if len(response) > MAX_RESPONSE_BYTES or len(exported_call) > MAX_CALL_BYTES:
        raise ValueError('Response binding evidence exceeds its byte bound.')
    response.decode('utf-8')
    task_id, receipt_id = canonical_id(task_id), canonical_id(receipt_id)
    call = decode_json(exported_call.decode('utf-8'))
    if not isinstance(call, dict):
        raise ValueError('Expected one exported evaluation call.')
    if call.get('task_id') != task_id or call.get('receipt_id') != receipt_id:
        raise ValueError('Exported task or receipt identity differs from the requested binding.')
    if call.get('receipt_status') != 'accepted' or call.get('status') != 'completed':
        raise ValueError('Expected a completed task and accepted gateway receipt.')
    canonical_id(call.get('receipt_run_id'))
    digest = hashlib.sha256(response).hexdigest()
    if call.get('response_sha256') != digest or type(call.get('response_bytes')) is not int or call['response_bytes'] != len(response):
        raise ValueError('Retained response differs from the recorded final-answer digest or size.')
    return {'schema_version': 1, 'response_receipt_binding_consistent': True,
            'task_id': task_id, 'receipt_id': receipt_id, 'receipt_run_id': call['receipt_run_id'],
            'response_sha256': digest, 'response_bytes': len(response),
            'exported_call_sha256': hashlib.sha256(exported_call).hexdigest(),
            'provider_provenance_authenticated': False, 'candidate_executed': False,
            'implementation_correctness': None, 'cost_verified': False}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--response', required=True, type=Path)
    parser.add_argument('--call', required=True, type=Path)
    parser.add_argument('--task-id', required=True)
    parser.add_argument('--receipt-id', required=True)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    with args.response.open('rb') as source:
        response = source.read(MAX_RESPONSE_BYTES + 1)
    with args.call.open('rb') as source:
        call = source.read(MAX_CALL_BYTES + 1)
    publish_report(args.output, verify(response, call, args.task_id, args.receipt_id))
