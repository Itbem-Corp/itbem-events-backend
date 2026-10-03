"""Validate patch response structure only; never writes or executes candidate source."""
import argparse
import hashlib
from pathlib import Path

from evaluation_report import decode_json, publish_report
from prepare_implementation_evaluation import EDITABLE

MAX_RESPONSE_BYTES = 65_536


def validate(raw):
    if len(raw) > MAX_RESPONSE_BYTES:
        raise ValueError('Implementation response exceeds 64 KiB.')
    response = decode_json(raw.decode('utf-8'))
    if not isinstance(response, dict) or set(response) != {'changes'}:
        raise ValueError('Expected only the changes field.')
    changes = response['changes']
    if not isinstance(changes, list) or len(changes) != len(EDITABLE):
        raise ValueError('Expected exactly two replacement files.')
    files = {}
    for change in changes:
        if not isinstance(change, dict) or set(change) != {'path', 'content'}:
            raise ValueError('Expected only path and content for each file.')
        path, content = change['path'], change['content']
        if not isinstance(path, str) or path not in EDITABLE or path in files:
            raise ValueError('Unexpected or repeated replacement path.')
        if not isinstance(content, str) or not content.strip() or '\x00' in content:
            raise ValueError('Replacement content must be nonempty text without NUL.')
        try:
            encoded = content.encode('utf-8')
        except UnicodeEncodeError as error:
            raise ValueError('Replacement content must be valid UTF-8.') from error
        files[path] = {'content': content, 'sha256': hashlib.sha256(encoded).hexdigest()}
    return {'schema_version': 1, 'case_version': 'pagination-v1',
            'response_contract_valid': True, 'candidate_executed': False,
            'implementation_correctness': None, 'response_sha256': hashlib.sha256(raw).hexdigest(),
            'files': files}


def assess(raw):
    try:
        return validate(raw)
    except (ValueError, RecursionError) as error:
        # A bounded read of an oversized response does not identify the entire
        # input. Retain an explicit unavailable hash instead of hashing a prefix.
        complete = len(raw) <= MAX_RESPONSE_BYTES
        return {'schema_version': 1, 'case_version': 'pagination-v1',
                'response_contract_valid': False, 'candidate_executed': False,
                'implementation_correctness': None, 'validation_error': str(error),
                'response_sha256': hashlib.sha256(raw).hexdigest() if complete else None,
                'response_bytes_observed': len(raw), 'response_read_complete': complete,
                'files': {}}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--response', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    with args.response.open('rb') as source:
        raw = source.read(MAX_RESPONSE_BYTES + 1)
    report = assess(raw)
    publish_report(args.output, report)
    raise SystemExit(0 if report['response_contract_valid'] else 1)
