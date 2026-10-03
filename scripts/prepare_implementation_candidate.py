"""Prepare evaluator-owned sandbox package data; never materializes or executes source."""
import argparse
import hashlib
import struct
from pathlib import Path

from evaluation_report import publish_report
from prepare_implementation_evaluation import CASE, prepare
from validate_implementation_response import MAX_RESPONSE_BYTES, assess


def package_worktree_digest(files):
    """Bind exact UTF-8 bytes with non-executable modes, as the sandbox does."""
    digest = hashlib.sha256(b'itbem-sandbox-source-v1\n')
    for name in sorted(files, key=lambda value: value.encode('utf-8')):
        path, content = name.encode('utf-8'), files[name].encode('utf-8')
        digest.update(struct.pack('>Q', len(path)))
        digest.update(path)
        digest.update(b'\x00')
        digest.update(struct.pack('>Q', len(content)))
        digest.update(hashlib.sha256(content).digest())
    return 'sha256:' + digest.hexdigest()


def package(raw):
    assessment = assess(raw)
    result = {'schema_version': 1, 'case_version': 'pagination-v1',
              'candidate_package_prepared': False, 'candidate_executed': False,
              'implementation_correctness': None, 'assessment': assessment,
              'prepared_prompt_sha256': prepare()['prompt_sha256'],
              'execution_requirement': 'authorized_isolated_sandbox_only',
              'package_files': {}, 'expected_worktree_digest': None}
    if not assessment['response_contract_valid']:
        return result
    files = {name: value['content'] for name, value in assessment['files'].items()}
    for name, source in [('go.mod', CASE / 'fixture/go.mod'),
                         ('page_test.go', CASE / 'oracle/page_test.go')]:
        files[name] = source.read_bytes().decode('utf-8').replace('\r\n', '\n')
    result.update(candidate_package_prepared=True, package_files=files,
                  expected_worktree_digest=package_worktree_digest(files),
                  package_file_modes={name: '0644' for name in files},
                  package_file_sha256={name: hashlib.sha256(content.encode()).hexdigest()
                                       for name, content in files.items()})
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--response', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    with args.response.open('rb') as source:
        raw = source.read(MAX_RESPONSE_BYTES + 1)
    report = package(raw)
    publish_report(args.output, report)
    raise SystemExit(0 if report['candidate_package_prepared'] else 1)
