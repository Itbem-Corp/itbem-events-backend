"""Replay downloaded build artifacts against a Git revision; no network or provider calls."""
import argparse
import hashlib
import os
from pathlib import Path
import re
import shlex
import subprocess

from evaluation_report import decode_json, publish_report
from verify_implementation_benchmark import SUBTESTS
from verify_implementation_sandbox import verify as verify_sandbox, verify_inner
from verify_implementation_oracle import verify as verify_oracle
from verify_implementation_candidate_execution import verify as verify_candidate

ROOT = Path(__file__).resolve().parents[1]
CASE = 'internal/automationagent/testdata/implementation/pagination-v1/'


def git(*args):
    return subprocess.check_output(['git', *args], cwd=ROOT, timeout=30)


def workflow_scopes(text):
    scopes = {}
    for line in text.splitlines():
        if not line.strip().startswith('go run ./cmd/verify-test-evidence '):
            continue
        tokens = shlex.split(line.strip())
        if tokens[:3] != ['go', 'run', './cmd/verify-test-evidence']:
            continue
        args = tokens[3:]
        if '-log' not in args:
            raise ValueError('Evidence verifier lacks log identity.')
        name = args[args.index('-log') + 1]
        if name in scopes:
            raise ValueError('Ambiguous workflow evidence scope.')
        scopes[name] = args
    required = {'agent-regression.jsonl', 'localstack-integration.jsonl', 'sandbox-integration.jsonl'}
    if set(scopes) != required:
        raise ValueError('Workflow evidence scope changed; review the artifact verifier.')
    return scopes


def verify_controls(raw, revision):
    report = decode_json(raw.decode('utf-8'))
    if (report.get('case') != 'pagination-v1' or report.get('schema_version') != 1
            or report.get('evaluator_controls_verified') is not True or report.get('model_quality_measured') is not False
            or type(report.get('provider_calls')) is not int or report['provider_calls'] != 0):
        raise ValueError('Unverified implementation controls.')
    expected = {'fixture': 'second', 'offset-only': 'partial-tail',
                'tail-only': 'second', 'aliases-source': 'first', 'reference': None}
    results = report['results']
    if len(results) != 5 or {row['control'] for row in results} != set(expected):
        raise ValueError('Missing or repeated implementation control.')
    for row in results:
        if row.get('control_verified') is not True or type(row.get('exit_code')) is not int:
            raise ValueError('Invalid control result.')
        if hashlib.sha256(row['stdout'].encode()).hexdigest() != row['stdout_sha256']:
            raise ValueError('Control output hash mismatch.')
        events = [decode_json(line) for line in row['stdout'].splitlines()]
        verify_inner(events, 'pass' if row['control'] == 'reference' else 'fail')
        if any(e.get('Action') == 'skip' for e in events):
            raise ValueError('Implementation control skipped.')
        passed = {e.get('Test') for e in events if e.get('Action') == 'pass'}
        failed = {e.get('Test') for e in events if e.get('Action') == 'fail'}
        if row['control'] == 'reference':
            if row['exit_code'] != 0 or failed or not {'TestPaginationContract/' + s for s in SUBTESTS} <= passed:
                raise ValueError('Reference oracle incomplete.')
        elif row['exit_code'] != 1 or 'TestPaginationContract/' + expected[row['control']] not in failed:
            raise ValueError('Negative control did not reject its defect.')
    paths = git('ls-tree', '-r', '--name-only', revision, '--', CASE).decode().splitlines()
    if set(report['source_sha256']) != {path[len(CASE):] for path in paths}:
        raise ValueError('Control source inventory differs from Git revision.')
    for path in paths:
        if hashlib.sha256(git('show', revision + ':' + path)).hexdigest() != report['source_sha256'][path[len(CASE):]]:
            raise ValueError('Control source differs from Git revision.')
    return {'verified': True, 'reference_cases': len(SUBTESTS), 'negative_controls': 4,
            'report_sha256': hashlib.sha256(raw).hexdigest()}


def verify_binaries(directory):
    result = []
    for artifact, filename in [('itbem-ai-agent-windows-amd64', 'itbem-ai-agent.exe'),
                               ('itbem-ai-agent-linux-amd64', 'itbem-ai-agent')]:
        folder = directory / artifact
        manifest = folder / 'SHA256SUMS.txt'
        binary = folder / filename
        if folder.is_symlink() or manifest.is_symlink() or binary.is_symlink():
            raise ValueError('Binary evidence must not use symlinks.')
        match = re.fullmatch(r'([0-9a-fA-F]{64})\s+\*?' + re.escape(filename) + r'\s*', manifest.read_text())
        if not match:
            raise ValueError('Unexpected binary manifest entries.')
        digest = hashlib.sha256(binary.read_bytes()).hexdigest()
        if digest != match[1].lower():
            raise ValueError('Binary manifest hash mismatch.')
        result.append({'artifact': artifact, 'file': filename, 'sha256': digest})
    return result


def summarize_log(raw):
    events = [decode_json(line) for line in raw.decode('utf-8').splitlines()]
    tests = [event for event in events if event.get('Test')]
    skips = [{'package': event['Package'], 'test': event['Test']}
             for event in tests if event.get('Action') == 'skip']
    return {'passed_tests': sum(event.get('Action') == 'pass' for event in tests),
            'skipped_tests': len(skips), 'skips': skips,
            'failed_events': sum(event.get('Action') == 'fail' for event in events),
            'completed_packages': sorted({event['Package'] for event in events
                                          if event.get('Action') == 'pass' and not event.get('Test')})}


def verify(revision, regression, runtime, binaries, go):
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('Full canonical Git revision required.')
    workflow = git('show', revision + ':.github/workflows/build-ai-agent.yml')
    scopes = workflow_scopes(workflow.decode())
    environment = dict(os.environ, GOTOOLCHAIN='local', GOWORK='off')
    version = subprocess.check_output([go, 'version'], env=environment, timeout=15).decode().strip()
    if not version.startswith('go version go1.25.13 '):
        raise ValueError('Pinned Go 1.25.13 required for artifact replay.')
    logs = {}
    for name, args in scopes.items():
        path = (regression if name == 'agent-regression.jsonl' else runtime) / name
        arguments = list(args)
        arguments[arguments.index('-log') + 1] = str(path.resolve())
        run = subprocess.run([go, 'run', './cmd/verify-test-evidence', *arguments], cwd=ROOT,
                             capture_output=True, text=True, timeout=90, env=environment)
        if run.returncode != 0:
            raise ValueError('Go evidence verification failed for ' + name + ': ' + run.stderr)
        raw = path.read_bytes()
        logs[name] = {'sha256': hashlib.sha256(raw).hexdigest(), 'scope': args,
                      'summary': summarize_log(raw)}
    sandbox_args = scopes['sandbox-integration.jsonl']
    count = int(sandbox_args[sandbox_args.index('-repetitions') + 1])
    sandbox = verify_sandbox((runtime / 'sandbox-integration.jsonl').read_bytes(), count)
    published = decode_json((runtime / 'implementation-sandbox.json').read_text())
    if published != sandbox:
        raise ValueError('Published inner sandbox report differs from independent replay.')
    oracle = None
    if b'python3 scripts/verify_implementation_oracle.py ' in workflow:
        oracle = verify_oracle((runtime / 'sandbox-integration.jsonl').read_bytes(), count)
        if decode_json((runtime / 'implementation-oracle.json').read_text()) != oracle:
            raise ValueError('Published oracle execution report differs from independent replay.')
    candidate = None
    if b'python3 scripts/verify_implementation_candidate_execution.py ' in workflow:
        candidate = verify_candidate((runtime / 'sandbox-integration.jsonl').read_bytes(), count)
        if decode_json((runtime / 'implementation-candidate-execution.json').read_text()) != candidate:
            raise ValueError('Published candidate execution report differs from independent replay.')
    return {'schema_version': 1, 'artifact_source_revision': revision,
            'evaluator_revision': git('rev-parse', 'HEAD').decode().strip(),
            'evaluator_worktree_clean': not bool(git('status', '--porcelain')),
            'evaluator_script_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            'artifact_contents_verified': True, 'ci_check_status_inspected': False,
            'artifact_origin_authenticated': False, 'model_quality_measured': False,
            'workflow_sha256': hashlib.sha256(workflow).hexdigest(), 'logs': logs,
            'implementation_controls': verify_controls((regression / 'implementation-controls.json').read_bytes(), revision),
            'sandbox_repetitions': count, 'oracle_execution_controls': oracle,
            'candidate_execution_controls': candidate,
            'binaries': verify_binaries(binaries)}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--regression', required=True, type=Path)
    parser.add_argument('--runtime', required=True, type=Path)
    parser.add_argument('--binaries', required=True, type=Path)
    parser.add_argument('--go', default='go')
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    publish_report(args.output, verify(args.revision, args.regression, args.runtime, args.binaries, args.go))
