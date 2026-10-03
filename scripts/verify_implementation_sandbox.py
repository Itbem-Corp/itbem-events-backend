"""Inspect retained synthetic Docker controls; does not authenticate model provenance."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import uuid

from evaluation_report import decode_json, publish_report
from verify_implementation_benchmark import SUBTESTS
from prepare_implementation_candidate import package_worktree_digest
from prepare_implementation_evaluation import CASE

PACKAGE = 'events-stocks/internal/automationagent'
TEST = 'TestDockerSandboxRoundTrip'
MARKER = 'implementation sandbox evidence: '


def verify_inner(events, expected):
    started, finished = False, False
    running, outcomes = set(), {}
    for event in events:
        if not isinstance(event, dict) or event.get('Package') != 'synthetic-pagination':
            raise ValueError('Unexpected inner test package.')
        action, test = event.get('Action'), event.get('Test')
        if finished:
            raise ValueError('Inner evidence continues after package completion.')
        if action == 'start' and not test:
            if started:
                raise ValueError('Duplicate inner package start.')
            started = True
        elif not started:
            raise ValueError('Inner evidence precedes package start.')
        elif action == 'run' and test:
            if test in running or test in outcomes:
                raise ValueError('Repeated inner test.')
            running.add(test)
        elif action in ('pass', 'fail') and test:
            if test not in running:
                raise ValueError('Inner test outcome without execution.')
            running.remove(test)
            outcomes[test] = action
        elif action in ('pass', 'fail') and not test:
            if running or action != expected or outcomes.get('TestPaginationContract') != expected:
                raise ValueError('Incomplete or contradictory inner package result.')
            finished = True
        elif action == 'output':
            if test and test not in running:
                raise ValueError('Inner test output outside execution.')
        else:
            raise ValueError('Unsupported inner evidence action.')
    if not finished:
        raise ValueError('Missing inner package completion.')


def candidate_response_bytes(files):
    """Reconstruct the fixed Go json.Marshal control envelope, including HTML escaping."""
    response = json.dumps({'changes': [{'path': path, 'content': files[path]}
                                      for path in ('page.go', 'store.go')]},
                          sort_keys=True, separators=(',', ':'), ensure_ascii=False)
    for character, escaped in (('&', r'\u0026'), ('<', r'\u003c'), ('>', r'\u003e'),
                               (chr(0x2028), r'\u2028'), (chr(0x2029), r'\u2029')):
        response = response.replace(character, escaped)
    return response.encode('utf-8')


def verify(raw, repetitions=2, *, oracle_entry=False, candidate_entry=False):
    if type(repetitions) is not int or repetitions < 1:
        raise ValueError('Positive repetition count required.')
    if oracle_entry and candidate_entry:
        raise ValueError('Only one execution entry can be verified.')
    bound_entry = oracle_entry or candidate_entry
    output, completed, running = '', [], False
    leases, tasks = set(), set()
    test_name = 'TestImplementationPilotOracleDockerRoundTrip' if oracle_entry else TEST
    marker = 'implementation oracle execution evidence: ' if oracle_entry else MARKER
    if candidate_entry:
        test_name = 'TestImplementationCandidateResponseDockerRoundTrip'
        marker = 'implementation candidate execution evidence: '
    for line in raw.decode('utf-8').splitlines():
        event = decode_json(line)
        if not isinstance(event, dict):
            raise ValueError('Go evidence event must be an object.')
        if event.get('Package') != PACKAGE or event.get('Test') != test_name:
            continue
        action = event.get('Action')
        if action == 'run':
            if running:
                raise ValueError('Overlapping sandbox tests.')
            running, output = True, ''
        elif action == 'output':
            if not running or not isinstance(event.get('Output'), str):
                raise ValueError('Sandbox output outside a running test.')
            output += event['Output']
        elif action in ('pass', 'fail', 'skip'):
            if not running or action != 'pass':
                raise ValueError('Sandbox test must complete without failure or skip.')
            controls = []
            for text in output.splitlines():
                if marker in text:
                    controls.append(decode_json(text.split(marker, 1)[1]))
            if len(controls) != 2 or {c.get('control') for c in controls} != {'fixture', 'reference'}:
                raise ValueError('Each repetition requires both implementation controls.')
            for control in controls:
                name = control['control']
                lease = control.get('sandbox_lease', {})
                if (control.get('case') != 'pagination-v1' or control.get('model_quality_measured') is not False
                        or lease.get('runtime') != 'docker' or lease.get('isolation_mode') != 'docker_container'
                        or lease.get('status') != 'completed'
                        or (not bound_entry and lease.get('task_id') != 'implementation-control-' + name)
                        or not re.fullmatch(r'sha256:[0-9a-f]{64}', lease.get('worktree_digest', ''))
                        or not lease.get('started_at') or not lease.get('finished_at')
                        or not re.fullmatch(r'[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}', lease.get('lease_id', ''))):
                    raise ValueError('Incomplete Docker control lease.')
                if lease['lease_id'] in leases:
                    raise ValueError('Reused Docker control lease.')
                leases.add(lease['lease_id'])
                if bound_entry:
                    try:
                        task = uuid.UUID(lease.get('task_id', ''))
                    except (ValueError, AttributeError, TypeError):
                        raise ValueError('Oracle execution requires a task UUID.') from None
                    if task.int == 0 or uuid.UUID(lease['lease_id']).int == 0 or str(task) != lease['task_id'] or task in tasks:
                        raise ValueError('Reused or invalid oracle task UUID.')
                    tasks.add(task)
                    sources = {'go.mod': 'fixture/go.mod', 'page.go': name + '/page.go',
                               'store.go': name + '/store.go', 'page_test.go': 'oracle/page_test.go'}
                    files = {path: (CASE / source).read_bytes().decode('utf-8').replace('\r\n', '\n')
                             for path, source in sources.items()}
                    if (lease['worktree_digest'] != package_worktree_digest(files)
                            or control.get('provider_provenance_authenticated') is not False):
                        raise ValueError('Oracle execution does not bind evaluator source bytes.')
                    if candidate_entry and control.get('response_sha256') != hashlib.sha256(candidate_response_bytes(files)).hexdigest():
                        raise ValueError('Candidate execution does not bind exact response bytes.')
                inner = [decode_json(row) for row in control['output'].splitlines()]
                verify_inner(inner, 'pass' if name == 'reference' else 'fail')
                passed = [e.get('Test') for e in inner if e.get('Action') == 'pass']
                failed = [e.get('Test') for e in inner if e.get('Action') == 'fail']
                if any(e.get('Action') == 'skip' for e in inner):
                    raise ValueError('Inner oracle cannot skip.')
                if name == 'reference':
                    required = {'TestPaginationContract/' + case for case in SUBTESTS}
                    if control.get('exit_code') != 0 or failed or any(passed.count(case) != 1 for case in required):
                        raise ValueError('Reference did not pass every oracle case once.')
                elif control.get('exit_code') != 1 or 'TestPaginationContract/second' not in failed:
                    raise ValueError('Defective fixture was not rejected by the oracle.')
            completed.append(controls)
            running = False
    if running or len(completed) != repetitions:
        raise ValueError('Incomplete sandbox repetition evidence.')
    report = {'schema_version': 1, 'case': 'pagination-v1', 'sandbox_controls_verified': True,
            'repetitions': repetitions, 'controls': completed,
            'input_sha256': hashlib.sha256(raw).hexdigest(), 'model_quality_measured': False,
            'provenance_authenticated': False}
    if oracle_entry:
        report['oracle_execution_entry_verified'] = True
    if candidate_entry:
        report['candidate_execution_entry_verified'] = True
    return report


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--log', required=True, type=Path)
    parser.add_argument('--repetitions', type=int, default=2)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    publish_report(args.output, verify(args.log.read_bytes(), args.repetitions))
