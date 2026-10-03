"""Verify prepared response and worktree bindings without executing candidate code."""
import argparse
import hashlib
import json
from pathlib import Path
import stat

from evaluation_report import decode_json, publish_report
from prepare_implementation_candidate import package
from sandbox_worktree import snapshot_worktree
from validate_implementation_response import MAX_RESPONSE_BYTES

MAX_PACKAGE_BYTES = 512 * 1024


def verify(response, published, worktree):
    if len(response) > MAX_RESPONSE_BYTES or len(published) > MAX_PACKAGE_BYTES:
        raise ValueError('Candidate evidence exceeds its byte bound.')
    expected = package(response)
    if not expected['candidate_package_prepared']:
        raise ValueError('Response cannot prepare a candidate package.')
    actual = decode_json(published.decode('utf-8'))
    # Canonical JSON equality keeps booleans distinct from integer claims.
    if json.dumps(actual, sort_keys=True, ensure_ascii=False, allow_nan=False) != json.dumps(expected, sort_keys=True, ensure_ascii=False, allow_nan=False):
        raise ValueError('Published package differs from the original response or evaluator files.')
    root = Path(worktree)
    if root.is_symlink() or not root.is_dir():
        raise ValueError('Candidate worktree must be a real directory.')
    if {entry.name for entry in root.iterdir()} != set(expected['package_files']):
        raise ValueError('Candidate worktree inventory differs from the prepared package.')
    for name in expected['package_files']:
        info = (root / name).lstat()
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o644:
            raise ValueError('Candidate files must be regular files with mode 0644.')
    digest, count, size = snapshot_worktree(root)
    if digest != expected['expected_worktree_digest']:
        raise ValueError('Candidate worktree content differs from the prepared package.')
    return {'schema_version': 1, 'case_version': 'pagination-v1',
            'candidate_package_binding_verified': True,
            'response_sha256': hashlib.sha256(response).hexdigest(),
            'published_package_sha256': hashlib.sha256(published).hexdigest(),
            'worktree_digest': digest, 'file_count': count, 'source_bytes': size,
            'candidate_executed': False, 'implementation_correctness': None,
            'provider_provenance_authenticated': False}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--response', required=True, type=Path)
    parser.add_argument('--package', required=True, type=Path)
    parser.add_argument('--worktree', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    with args.response.open('rb') as source:
        response = source.read(MAX_RESPONSE_BYTES + 1)
    with args.package.open('rb') as source:
        published = source.read(MAX_PACKAGE_BYTES + 1)
    publish_report(args.output, verify(response, published, args.worktree))
