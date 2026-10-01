"""Validate the actual read-only spend report using synthetic PostgreSQL rows."""
from pathlib import Path
import subprocess
import uuid

name = 'spend-regression-' + uuid.uuid4().hex[:12]
report = (Path(__file__).parent / 'inference-spend-report.sql').read_text()

def run(*args, **kwargs):
    result = subprocess.run(args, text=True, capture_output=True, timeout=180, **kwargs)
    if result.returncode:
        raise RuntimeError(result.stderr)
    return result

fixture = """
CREATE TABLE automation_inference_receipts (
 id text PRIMARY KEY, provider text DEFAULT 'synthetic', model text DEFAULT 'fixture',
 currency text DEFAULT 'USD', pricing_basis text DEFAULT 'api_equivalent',
 status text DEFAULT 'accepted', created_at timestamptz DEFAULT now(),
 resolved_at timestamptz DEFAULT now(), input_tokens bigint DEFAULT 100,
 output_tokens bigint DEFAULT 40, cached_input_tokens bigint DEFAULT 80,
 cache_write_tokens bigint DEFAULT 0, reasoning_tokens bigint DEFAULT 30,
 input_cost_micros bigint DEFAULT 10, cached_cost_micros bigint DEFAULT 2,
 cache_write_cost_micros bigint DEFAULT 0, output_cost_micros bigint DEFAULT 88,
 total_cost_micros bigint DEFAULT 100);
ALTER TABLE automation_inference_receipts ADD COLUMN usage_json jsonb DEFAULT '{"prompt_tokens_details":{"cached_tokens":80}}';
CREATE TABLE automation_executions (
 inference_receipt_id text, total_cost_micros bigint, currency text DEFAULT 'USD',
 completed_at timestamptz DEFAULT now());
CREATE TABLE automation_tool_executions (LIKE automation_executions INCLUDING DEFAULTS);
INSERT INTO automation_inference_receipts(id) VALUES ('initial'), ('repair');
INSERT INTO automation_inference_receipts(id,status) VALUES ('billable-rejection','rejected');
INSERT INTO automation_inference_receipts(id,created_at) VALUES ('older',now()-interval '40 days');
UPDATE automation_inference_receipts SET usage_json='{}' WHERE id='older';
UPDATE automation_inference_receipts SET usage_json='{"prompt_tokens_details":{"cached_tokens":90}}' WHERE id='initial';
INSERT INTO automation_inference_receipts(id,status,resolved_at) VALUES ('ambiguous','ambiguous',now());
INSERT INTO automation_inference_receipts(id,status,resolved_at) VALUES ('pending','reserved',NULL);
INSERT INTO automation_executions(inference_receipt_id,total_cost_micros) VALUES ('repair',100), (NULL,50);
INSERT INTO automation_tool_executions(inference_receipt_id,total_cost_micros) VALUES ('billable-rejection',100), (NULL,25);
"""
try:
    run('docker', 'run', '--detach', '--name', name, '--env', 'POSTGRES_HOST_AUTH_METHOD=trust', 'postgres:16-alpine')
    run('docker', 'exec', name, 'sh', '-c', 'for attempt in $(seq 1 100); do pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1 && exit 0; sleep 0.2; done; exit 1')
    run('docker', 'exec', '-i', name, 'psql', '-h', '127.0.0.1', '-U', 'postgres', '-v', 'ON_ERROR_STOP=1', input=fixture)
    output = run('docker', 'exec', '-i', name, 'psql', '-h', '127.0.0.1', '-U', 'postgres', '-v', 'ON_ERROR_STOP=1', '--csv', '-q', input=report).stdout
    import csv
    import io
    lines = output.splitlines()
    first_header = lines.index(next(line for line in lines if line.startswith('period,')))
    first_end = next(i for i in range(first_header+1, len(lines)) if lines[i].startswith('currency,'))
    rows = list(csv.DictReader(io.StringIO('\n'.join(lines[first_header:first_end]))))
    lifetime = next(row for row in rows if row['period'] == 'all_time')
    recent = next(row for row in rows if row['period'] == 'last_30_days')
    assert lifetime['receipt_count'] == '6' and recent['receipt_count'] == '5'
    assert lifetime['observed_input_tokens'] == '400' and lifetime['observed_output_tokens'] == '160'
    assert lifetime['verified_total_cost_microusd'] == '400' and recent['verified_total_cost_microusd'] == '300'
    assert lifetime['verified_uncached_input_cost_microusd'] == '40'
    assert lifetime['verified_cached_input_cost_microusd'] == '8'
    assert lifetime['verified_output_cost_microusd'] == '352'
    assert lifetime['unknown_cost_count'] == '2' and lifetime['complete_total_cost_microusd'] == ''
    assert lifetime['ambiguous_count'] == '1' and lifetime['inconsistent_cost_count'] == '0'
    assert lifetime['unknown_cache_input_count'] == '1' and lifetime['inconsistent_cache_count'] == '1'
    assert 'USD,2,200' in output, 'initial and older receipts must remain visible without double-counting bound rows'
    assert 'legacy_agent,USD,1,50' in output and 'legacy_tool,USD,1,25' in output
    assert 'READ ONLY' in report and 'REPEATABLE READ' in report
    print('PASS: PostgreSQL spend report; input/output split, per-call coverage, billable rejection, ambiguous/pending unknowns, time ranges, explicit bindings and separate legacy costs')
finally:
    subprocess.run(['docker', 'rm', '--force', name], check=False, capture_output=True)
