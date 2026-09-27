# Delivery plan-step activity journal

The activity journal is an append-only, allow-listed record of observable work
performed while a local agent owns a delivery plan-step lease. It complements
`DeliveryPlanStepEvent`, which records lifecycle transitions; it does not
replace the task execution/cost ledgers or private evidence references.

## Worker callback

`POST /api/internal/automation/steps/:id/activity` is on the internal ITBEM
worker route group and requires a registered Ed25519 machine identity, just
like claim, lease, heartbeat and task-result callbacks. The worker signs the
method, exact request URI, raw body digest, instance ID, timestamp and nonce
with the local private key; the API enforces a ±90-second clock window and
durable single-use nonce. The body is strictly decoded and rejects unknown fields:

```json
{
  "event_id": "<uuid>",
  "task_id": "<uuid>",
  "run_id": "<uuid>",
  "worker_id": "<uuid>",
  "agent_key": "generalist",
  "machine_id": "<uuid>",
  "fencing_token": "4",
  "sequence": 12,
  "action": "tool",
  "phase": "completed",
  "tool_name": "stagehand_click",
  "duration_ms": 42
}
```

The supported actions are `inference`, `tool`, `file_read`, `file_change`,
`command`, `validation`, and `evidence`; phases are `started`, `completed`, and
`failed`. `tool_name` is optional and limited to a short identifier for `tool`
events. `duration_ms` is optional, non-negative, and bounded to 24 hours; it is
not accepted for a `started` event. Only terminal events may include the
optional typed `details` object. For example, a `file_change` terminal event
may include these allow-listed fields:

```json
{
  "resource_references": ["workspace://backend"],
  "changed_files": ["internal/orders.go"]
}
```

For `command` and `validation`, the alternative command-only projection is
`{"executable_name":"go","argument_count":2,"exit_code":0,"captured_output_bytes":128,"resource_references":["workspace://backend"]}`.
Command details are accepted only for those actions and require all four
command-summary fields. The executable must be an allow-listed basename;
argument values and command lines are never accepted. Output is represented by
a bounded byte count only (at most 24,000 bytes), not captured text. The details
object is capped at 4 KiB. Repository references must be canonical
`workspace://<frozen-workspace-id>[/<relative-path>]` references, and the server
binds each workspace ID to the work item's frozen repository snapshots.
Changed files must be repository-relative. Both are capped and reject absolute
paths, traversal, `.git`, `.env*`, and common credential/key, token, password,
and certificate filenames. Unknown nested fields are rejected by strict JSON
decoding; no arbitrary text, prompt, reasoning, model response, stdout/stderr,
or secret is accepted.

No prompt, hidden reasoning, raw command, argument, file contents, provider
request/response, or caller-authored summary is accepted or persisted. The
server derives the summary from action/phase enums and compares normalized
typed details exactly when acknowledging a replay.

For a terminal successful `inference` activity, the worker also sends exactly
two opaque UUIDs: `call_id` and `receipt_id`. Start events carry neither. A
failed inference may omit both when the gateway did not provide a durable
receipt; a partial pair is rejected, as are IDs on any other action. The
backend cannot prove absence of an omitted receipt, so callers must preserve
these IDs whenever the gateway returns them. The worker does not
send provider, model, usage, cost, or pricing fields. In the same transaction
that validates the task and step leases, the API resolves both IDs against
`AutomationInferenceReceipt` and verifies its task, run, worker, agent profile,
machine, finalized status, and active authenticated task instance. Unknown or
mismatched receipts are rejected, including a receipt whose canonical nullable
`plan_step_id` does not equal the callback route's step UUID. The worker does
not choose the activity's provider attribution: it sends `plan_step_id` only
with the inference request, and the gateway verifies the approved plan,
task/run/worker/agent/machine tuple, assignment, and live lease before storing
that ID on the canonical receipt. Planning and other task-level calls leave
the field `NULL`; no timestamp or event-order heuristic is used to attach
them later. The activity event stores the opaque pair for idempotency and
read-time correlation; unique indexes prevent one gateway call/receipt from
being attached to multiple activity events.

The authorized activity read may add a top-level `inference` object to a
terminal inference row. It is assembled at read time from the canonical
receipt and contains only `receipt_id`, `provider`, `model`, `status`,
`input_tokens`, `output_tokens`, `cached_input_tokens`, `cache_write_tokens`,
`reasoning_tokens` (count only), `total_tokens`, `total_cost_microusd`,
`currency`, and `pricing_basis`. It is omitted if the event/receipt binding no
longer validates, the receipt has no `plan_step_id`, the step IDs differ, or
the receipt is unresolved. `unpriced` is reported as the
pricing basis with a zero ledger total, but must be presented as unavailable
cost rather than evidence of a free call. Prompts, completions, credentials,
hidden reasoning, provider JSON, usage JSON, and pricing snapshots are never
projected. The current receipt model does not persist `AgentInstanceID`; the
write path therefore binds the receipt's task/run/worker tuple to the task's
active instance authenticated by the signed callback, then records that
verified instance on the append-only activity event.

`plan_step_id` is a nullable UUID introduced through the ordered startup
`AutoMigrate`. Existing receipts remain `NULL`, preserving their original
task-level accounting without retroactive step guesses.

The handler revalidates the current task lease, worker/profile identity,
approved plan, step owner/run/fencing token/lease, and the child-task →
execution → step assignment (including approved plan hash when assigned) inside
the write transaction. Stale or mismatched bindings return conflict. Event UUID
and `(step, run, sequence)` are unique. Retries are acknowledged only when
their persisted semantic payload matches; reusing either key with different
content returns conflict.

Plan-step callbacks capture the lease-check time after acquiring the task row
lock; renewal and transition capture it again after acquiring the step row
lock, then recheck both task and step expiry. Runtime handlers leave the
service clock override unset so waiting on a database lock cannot validate an
expired lease against an earlier timestamp. Fixed clock overrides are reserved
for deterministic service tests.

Legacy sequential claims (which have no persisted machine/profile assignment)
and distributed fan-out use the same plan-row lock boundary. A legacy claimant
holds `FOR SHARE` and refuses to claim while the plan has a pending, dispatching,
or running `DeliveryPlanExecution`. Fan-out creation takes `FOR UPDATE` on that
plan and refuses creation while any running step has a live lease but no
assignment. Thus whichever transaction acquires the plan lock first commits
the ownership mode before the other can proceed; assigned fan-out keeps its
existing workspace-affinity and capacity reservations.

## Authorized read API

`GET /api/automation/plans/:id/steps/:stepId/activity?limit=25&cursor=...`
uses the same organization and project-view authorization as the plan-step
lifecycle timeline. `limit` is 1–100, default 25. The opaque, versioned cursor
is bound to the plan and step and paginates descending by `(occurred_at, id)`.
The response contains an allow-listed DTO only; fixed summaries are recomputed
from persisted enums and malformed opaque IDs/tool names degrade to empty safe
values. Typed details are decoded and validated again before projection;
unknown, malformed, or unsafe stored details are omitted. Fencing tokens and
database-only data are never projected.

## Storage and retention

`delivery_plan_step_activity_events` is registered with the normal GORM schema
migration and protected by a PostgreSQL trigger rejecting UPDATE and DELETE.
Unique constraints enforce event ID and per-step/per-run sequence idempotency.
The activity journal is observable progress, not proof of a verified patch,
test result, or accepted fan-in artifact. Private artifacts and financial usage
remain in their dedicated stores/ledgers.
