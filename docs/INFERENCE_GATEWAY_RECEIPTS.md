# Durable inference gateway receipts

All production local-agent and Stagehand inference requests include a UUID
`call_id`. The gateway validates the signed run capability and immutable
attempt-policy snapshot, locks the task row, and inserts a `reserved` receipt
before reading provider credentials or contacting a provider. The signed
snapshot carries the server-derived per-operation `max_inference_calls`:
implementation 6, QA 2 (primary plus Stagehand), publish 0, and 1 for other
operations. The receipt count is scoped to `(automation_task_id, run_id)` and
includes accepted, rejected, and ambiguous reservations. Capability renewal
does not change the run ID, policy signature, or quota.
The limit is derived from the persisted task operation when the snapshot is
created, signed into that snapshot, and revalidated by the gateway; workers
cannot request a larger limit in their capability or inference body.

The task-row lock serializes concurrent reservations for one run, and a unique
index on `(automation_task_id, run_id, call_id)` makes a call ID one-shot. A
duplicate ID is rejected before provider work; it does not replay a response.
The caller must use the same ID only when retrying the exact same logical call,
and must not treat a duplicate rejection as permission to create unlimited new
IDs. If a provider outcome or gateway accounting write is ambiguous, the
reservation is retained and cannot be replayed. Any later retry must be a new
explicitly authorized run, with its own quota; the prior ambiguity remains
auditable. This intentionally favors bounded at-most-once billing over
automatic recovery from a lost provider response.

After a normalized provider response is received, only the gateway resolves the
receipt to `accepted` or `rejected`. The receipt contains provider/model,
allow-listed numeric usage dimensions, the server-side pricing snapshot and
micro-USD totals, provider response ID, and opaque task/run/call/worker identity.
It never stores prompts, completions, credentials, or arbitrary provider
extensions. Database protection permits a single transition from `reserved` to
an outcome and rejects later edits/deletes.

The worker returns `call_id` and `receipt_id` with its callback. The control
plane loads the receipt and verifies its task, original run (including stored
result recovery), call, authenticated worker/agent/machine identity, and final
state. Callback-supplied provider, model, response ID, or usage is never used
for cost attribution. The execution/tool ledger copies accounting directly
from the receipt and stores its UUID for durable correlation.

The receipt ID is an identifier, not a bearer credential. Possessing it grants
no read access; callback authorization and all identity/binding checks remain
mandatory.

For `delivery.implementation` inference while a worker holds an approved plan
step, the worker sends only that opaque `plan_step_id` UUID alongside the
normal inference request. The gateway requires the signed task/run/worker/
agent/machine capability, locks the step, verifies the approved plan and the
persisted child-task assignment (including its approved content hash when
fan-out is used), then rechecks the live task and step leases before reserving
the receipt. The gateway persists the verified `plan_step_id` on the receipt;
it never accepts provider, token, or cost values from that field or from the
worker. Planning and other task-level inference omit `plan_step_id`, so their
receipt column remains `NULL`. No time-window, event-order, or nearest-activity
heuristic is used to infer a step.

The field is a nullable UUID added by the existing ordered startup
`AutoMigrate` path. Existing receipt rows stay `NULL` and are deliberately not
back-attributed. This is an additive migration: no table rebuild or provider
call is required. Step activity callbacks that reference a receipt must match
the receipt's canonical step UUID to the route's step UUID; authorized read
projections enforce the same equality and omit legacy/null or mismatched
attribution.
