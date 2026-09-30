# Isolated synthetic model evaluation

This extension is for a bounded platform screening. It does not change global
action policies, existing tasks, worker permissions, or provider credentials.

## Admission and immutable evidence

Only the authenticated primary platform root may create or dispatch a batch.
The server owns the synthetic corpus and the three candidate routes. Clients
select the published corpus version; they cannot supply routes, provider keys,
attempt signatures, or arbitrary input references. Each batch contains twenty
cases times three candidates. Each task retains its case ID, candidate ID,
prompt hash, full message hash, route hash, and batch ID.

The existing `ai.chat` worker contract supplies the same role instruction and
synthetic prompt for all candidates. At lease acquisition, evaluation tasks
freeze their server-authored single route instead of reading the global
`ai.chat` policy. Ordinary tasks retain the existing policy behavior. Attempt
signatures, capability identity, project scope, and leases remain mandatory.

## Cost and execution limits

Admission uses `automationcost.EstimateUpperBound` with the existing production
price catalog and all sixty complete message byte counts. Reserve the complete
batch upper bound; reject admission if it exceeds 1,000,000 micro-USD. MiniMax
amounts describe an API-equivalent estimate, not a subscription invoice.

Dispatch one pending task at a time. Every task permits at most one gateway
receipt across all runs, not merely one per lease. Reserved and ambiguous
receipts count as consumed calls. A failed task, ambiguous receipt, authorization
failure, accounting failure, or exhausted budget stops further dispatch until
an operator resolves it; no automatic retry may make a second provider call.
All sixty outcomes remain in the score denominator, including failures.

At gateway admission, verify the full message hash, 4096 output-token ceiling,
sealed candidate route, one-call limit, batch reservation, and exclusive active
call while holding database locks. Normal receipts and ledger prices account
for actual usage. Any cost beyond its conservative reservation stops the batch.
No fallback route is present.

## Required verification before enabling

Prove primary-root authorization; reject client-authored routes and signatures;
prove that global policy changes cannot change evaluation attempts; reject
modified messages, stale identities, leases and scopes; serialize concurrent
dispatch/calls; enforce the one-dollar reserve and sixty-call bound across
leases; stop on ambiguous paid results; preserve receipt, task and run linkage.
Run the real gateway adapter against local fake providers before billable use.

The worker stays on WSL and must already have authorized `ai.chat` capabilities.
If this requires new sensitive access, stop and request the exact human
authorization before changing identity or permissions.
