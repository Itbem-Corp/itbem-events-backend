"""Exercise the actual receipt migration trigger in disposable PostgreSQL 16.

Uses only synthetic rows; requires Docker and the postgres:16-alpine image.
"""
import pathlib
import re
import subprocess
import uuid

root = pathlib.Path(__file__).resolve().parents[1]
source = (root / 'configuration/gorm.go').read_text()
match = re.search(r'`(CREATE OR REPLACE FUNCTION protect_automation_inference_receipt\(\).*?\$\$ LANGUAGE plpgsql)`', source, re.S)
if not match:
    raise SystemExit('receipt trigger DDL not found')
name = 'diagnostic-regression-' + uuid.uuid4().hex[:12]

def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=True, timeout=180, **kwargs)

ddl = '''
CREATE TABLE automation_inference_receipts (
 id text PRIMARY KEY, automation_task_id text, run_id text, call_id text,
 plan_step_id text, operation text, worker_id text, agent_key text,
 machine_id text, policy_snapshot_hash text, quota_limit int,
 created_at timestamptz DEFAULT now(), resolved_at timestamptz,
 status text DEFAULT 'reserved', total_cost_micros bigint DEFAULT 0,
 input_tokens bigint DEFAULT 0, usage_json jsonb DEFAULT '{}',
 diagnostics_json jsonb NOT NULL DEFAULT '{}');
'''
tests = """
CREATE TRIGGER receipt_guard BEFORE UPDATE OR DELETE ON automation_inference_receipts
 FOR EACH ROW EXECUTE FUNCTION protect_automation_inference_receipt();
INSERT INTO automation_inference_receipts(id) VALUES ('synthetic');
UPDATE automation_inference_receipts SET diagnostics_json =
 '{"schema_version":1,"stage":"reserved","request_hash":"synthetic","request_capture":"available"}';
UPDATE automation_inference_receipts SET status='accepted', total_cost_micros=12, input_tokens=7;
UPDATE automation_inference_receipts SET diagnostics_json =
 '{"schema_version":1,"stage":"completed","request_hash":"synthetic","request_capture":"available","response_capture":"available","duration_ms":123}';
CREATE FUNCTION reject_mutation(statement text) RETURNS void AS $$
BEGIN
 BEGIN
  EXECUTE statement;
 EXCEPTION WHEN raise_exception THEN RETURN;
 END;
 RAISE EXCEPTION 'forbidden mutation was allowed: %', statement;
END;
$$ LANGUAGE plpgsql;
SELECT reject_mutation('UPDATE automation_inference_receipts SET total_cost_micros=99');
SELECT reject_mutation('UPDATE automation_inference_receipts SET status=''reserved''');
SELECT reject_mutation('UPDATE automation_inference_receipts SET diagnostics_json=''{}''');
SELECT reject_mutation('UPDATE automation_inference_receipts SET diagnostics_json=jsonb_set(diagnostics_json,''{duration_ms}'',''999'')');
SELECT reject_mutation('DELETE FROM automation_inference_receipts');
INSERT INTO automation_inference_receipts(id) VALUES ('binding-check');
UPDATE automation_inference_receipts SET diagnostics_json=
 '{"schema_version":1,"stage":"reserved","request_hash":"original"}' WHERE id='binding-check';
SELECT reject_mutation('UPDATE automation_inference_receipts SET diagnostics_json=''{"schema_version":1,"stage":"completed","request_hash":"changed"}'' WHERE id=''binding-check''');
SELECT reject_mutation('UPDATE automation_inference_receipts SET diagnostics_json=''{"schema_version":1,"stage":"completed","request_hash":"original"}'',input_tokens=99 WHERE id=''binding-check''');
SELECT reject_mutation('UPDATE automation_inference_receipts SET status=''ambiguous'', diagnostics_json=''{}'' WHERE id=''binding-check''');
UPDATE automation_inference_receipts SET status='ambiguous' WHERE id='binding-check';
UPDATE automation_inference_receipts SET diagnostics_json=
 '{"schema_version":1,"stage":"provider","request_hash":"original","failure_code":"provider_timeout"}' WHERE id='binding-check';
INSERT INTO automation_inference_receipts(id,status) VALUES ('legacy','accepted');
SELECT reject_mutation('UPDATE automation_inference_receipts SET diagnostics_json=''{"schema_version":1,"stage":"reserved"}'' WHERE id=''legacy''');
DO $$ BEGIN
 IF (SELECT total_cost_micros FROM automation_inference_receipts WHERE id='synthetic') <> 12
    OR (SELECT input_tokens FROM automation_inference_receipts WHERE id='synthetic') <> 7
 THEN RAISE EXCEPTION 'accounting changed'; END IF;
END $$;
"""
try:
    run('docker','run','--detach','--name',name,'--env','POSTGRES_HOST_AUTH_METHOD=trust','postgres:16-alpine')
    # The image first starts a temporary Unix-socket-only initialization server.
    # TCP readiness identifies the final server and avoids its shutdown race.
    run('docker','exec',name,'sh','-c','for attempt in $(seq 1 100); do pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1 && exit 0; sleep 0.2; done; exit 1')
    run('docker','exec','-i',name,'psql','-h','127.0.0.1','-U','postgres','-v','ON_ERROR_STOP=1',input=ddl+match.group(1)+';'+tests)
    print('PASS: PostgreSQL 16 diagnostic lifecycle, frozen accounting, binding, terminal immutability, ambiguous outcome and legacy guards')
finally:
    subprocess.run(['docker','rm','--force',name],check=False,capture_output=True)
