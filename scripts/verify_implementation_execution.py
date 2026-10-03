"""Check receipt/response/source/oracle consistency; does not authenticate evidence."""
import argparse
import hashlib
from pathlib import Path

from evaluation_report import decode_json, publish_report
from prepare_implementation_candidate import package
from validate_implementation_response import MAX_RESPONSE_BYTES
from verify_inference_response_binding import verify as verify_receipt, canonical_id, MAX_CALL_BYTES
from verify_implementation_sandbox import verify_inner
from verify_implementation_benchmark import SUBTESTS

MAX_EXECUTION_BYTES = 1024 * 1024


def verify(response, exported_call, execution_raw, task_id, receipt_id):
    if len(execution_raw) > MAX_EXECUTION_BYTES:
        raise ValueError('Execution evidence exceeds its byte bound.')
    binding = verify_receipt(response, exported_call, task_id, receipt_id)
    prepared = package(response)
    if not prepared['candidate_package_prepared']:
        raise ValueError('Response does not satisfy the implementation contract.')
    call = decode_json(exported_call.decode('utf-8'))
    if call.get('case_id') != 'pagination-v1' or call.get('prompt_sha256') != prepared['prepared_prompt_sha256']:
        raise ValueError('Receipt call does not bind the frozen implementation case and prompt.')
    execution = decode_json(execution_raw.decode('utf-8'))
    if not isinstance(execution, dict) or execution.get('case') != 'pagination-v1':
        raise ValueError('Expected pagination implementation execution evidence.')
    lease = execution.get('sandbox_lease')
    if not isinstance(lease, dict):
        raise ValueError('Missing sandbox execution lease.')
    canonical_id(lease.get('lease_id'))
    if (lease.get('task_id') != task_id or lease.get('runtime') != 'docker'
            or lease.get('isolation_mode') != 'docker_container' or lease.get('status') != 'completed'
            or lease.get('worktree_digest') != prepared['expected_worktree_digest']
            or execution.get('response_sha256') != binding['response_sha256']):
        raise ValueError('Execution differs from the task, retained answer or evaluator-owned source.')
    if execution.get('provider_provenance_authenticated') is not False or execution.get('model_quality_measured') is not False:
        raise ValueError('Unsupported provenance or model-quality claim.')
    exit_code, output = execution.get('exit_code'), execution.get('output')
    if type(exit_code) is not int or exit_code not in (0, 1) or not isinstance(output, str):
        raise ValueError('Expected retained oracle output and a bounded test outcome.')
    events = [decode_json(line) for line in output.splitlines()]
    verify_inner(events, 'pass' if exit_code == 0 else 'fail')
    outcomes = {event['Test']: event['Action'] for event in events
                if event.get('Test') and event.get('Action') in ('pass', 'fail')}
    required = {'TestPaginationContract/' + name for name in SUBTESTS}
    if not set(outcomes) <= required | {'TestPaginationContract'}:
        raise ValueError('Execution contains an unexpected oracle case.')
    if exit_code == 0 and set(outcomes) != required | {'TestPaginationContract'}:
        raise ValueError('A passing outcome requires every oracle case; partial execution is inconclusive.')
    failures = sorted(name for name in required if outcomes.get(name) == 'fail')
    if bool(failures) != bool(exit_code):
        raise ValueError('Oracle cases contradict the recorded exit code.')
    return {'schema_version': 1, 'case_version': 'pagination-v1',
            'response_receipt_execution_binding_consistent': True,
            'task_id': task_id, 'receipt_id': receipt_id, 'receipt_run_id': binding['receipt_run_id'],
            'response_sha256': binding['response_sha256'],
            'exported_call_sha256': binding['exported_call_sha256'],
            'execution_sha256': hashlib.sha256(execution_raw).hexdigest(),
            'worktree_digest': prepared['expected_worktree_digest'], 'lease_id': lease['lease_id'],
            'prepared_prompt_sha256': prepared['prepared_prompt_sha256'],
            'observed_oracle_passed': exit_code == 0, 'failed_oracle_cases': failures,
            'provider_provenance_authenticated': False, 'model_quality_measured': False,
            'cost_verified': False}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('response', 'call', 'execution', 'output'):
        parser.add_argument('--' + name, required=True, type=Path)
    parser.add_argument('--task-id', required=True)
    parser.add_argument('--receipt-id', required=True)
    args = parser.parse_args()
    values = []
    for path, bound in ((args.response, MAX_RESPONSE_BYTES), (args.call, MAX_CALL_BYTES),
                        (args.execution, MAX_EXECUTION_BYTES)):
        with path.open('rb') as source:
            values.append(source.read(bound + 1))
    publish_report(args.output, verify(*values, args.task_id, args.receipt_id))
