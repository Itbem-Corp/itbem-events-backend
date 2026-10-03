# Prepared implementation pilot

Version `synthetic-implementation-pagination-2026-10-03-v1` contains one synthetic
pagination repair case and prepares the three existing model routes. It is an
offline planning artifact, deliberately excluded from `SupportedCorpus`.
The public admission endpoint cannot dispatch it yet.

The frozen prompt is checked against the Python implementation preparation tool;
it includes fixture source only. The reference and eight-case oracle remain
outside model input. The plan reserves the existing completion bound and the
full normal worker envelope, rejects unavailable pricing and retains the USD 1
maximum. Price tests use an explicitly synthetic catalog and do not certify live
prices or incur provider usage. Existing screening/cache compilation still
requires exactly 20 cases and prepares 60 calls.

Before enabling central admission, implement version-specific dispatch/export
cardinality, preserve message/route/receipt bindings and cross-run paid-call
quotas, and qualify the flow through PostgreSQL with synthetic providers.
Then bind the actual response to evaluator-owned oracle execution in the
authorized sandbox. A single case can describe a pilot result only; it cannot
establish general software engineering model quality.
