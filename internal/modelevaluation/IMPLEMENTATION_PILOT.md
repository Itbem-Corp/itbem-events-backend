# Implementation pilot

Version `synthetic-implementation-pagination-2026-10-03-v1` contains one synthetic
pagination repair case for the three existing model routes. The root-only
evaluation endpoint admits this reviewed version using the normal encrypted
input upload, budget reservation, worker lease and central gateway. Requests
provide only an evaluation ID and corpus version; routes and prompts are fixed
by the server. No real model run has been performed by this qualification.

The frozen prompt is checked against the Python implementation preparation tool;
it includes fixture source only. The reference and eight-case oracle remain
outside model input. The plan reserves the existing completion bound and the
full normal worker envelope, rejects unavailable pricing and retains the USD 1
maximum. Price tests use an explicitly synthetic catalog and do not certify live
prices or incur provider usage. Existing screening/cache compilation still
requires exactly 20 cases and prepares 60 calls.

Dispatch and export bind their row limits to the corpus version: 60 for
screening/cache and three for the pilot. PostgreSQL qualification with
synthetic ledger rows rejects two/four-call pilot batches and queues exactly one
outbox event for three calls. The normal HTTP admission test also qualifies three
encrypted inputs, idempotent admission, frozen prompt/message/route bindings,
concurrent dispatch and rejection of a new batch ID after completed history.
It then runs all three routes through the real gateway with synthetic HTTP
responses, verifies receipt identities, token usage and bounded costs, rejects
altered prompts and repeated inference, and dispatches each next task until the
batch completes. Synthetic responses do not contain implementation solutions;
this qualification establishes accounting behavior, not model quality.
The completed pilot export is checked against all three durable call bindings
and receipts, including original run IDs, routes, policy hashes, tokens and costs.
The gateway also rejects unknown corpus versions, a pilot sequence beyond three,
a foreign pilot case and a candidate assigned to the wrong pilot sequence. Its
existing message, route, budget and cross-run receipt checks still apply.
Pilot inference additionally requires the embedded corpus digest, frozen prompt
digest and exact normal worker message digest. Rebinding an altered prompt into
the call ledger cannot authorize different model input.

After an authorized model run, retain the actual response, receipt identity,
usage and costs and bind that response to evaluator-owned oracle execution in
the authorized sandbox. Admission creates tasks; it does not execute generated
source or certify implementation correctness. Screening/cache scorers do not
score this distinct corpus. A single case can describe a pilot result only; it
cannot establish general software engineering model quality.

`scripts/prepare_implementation_candidate.py` prepares the two validated source
files with the evaluator-owned module and oracle. Its `expected_worktree_digest`
uses the same versioned byte manifest as the sandbox, with explicit non-executable
0644 file modes. An execution must match that digest before its result can be
associated with this package. Changes to candidate bytes, oracle bytes, execution
bits or the file inventory change the binding. Preparation alone does not prove
execution, authenticate a provider response or measure implementation correctness.

Before sandbox admission, `scripts/verify_implementation_candidate.py --response
response.json --package prepared.json --worktree candidate-directory --output
binding.json` regenerates the package from the original response and current
evaluator-owned files, requires exact published-package equality, and checks the
directory inventory, regular-file 0644 modes and source digest. It reads files
only. The sandbox must independently recheck its source binding at admission;
this preflight is not an execution attestation or provider authentication.

`automationagent.ExecuteImplementationPilotOracle` provides the fixed execution
entry point for an authorized caller. It requires a task UUID, the qualified Go
Docker image digest, network none, exactly four regular 0644 files, the embedded
evaluator module/oracle bytes and the prepared source digest. It runs only
`go test -json -count=1 ./...` with offline Go settings and retains output, exit
code and the completed sandbox lease bound to task and source. It grants no task
authorization and authenticates no provider response. CI executes this entry
point twice with the defective fixture and reference, requiring rejection of
the fixture and all eight reference oracle cases. Actual model candidates have
not yet been executed through it.

`scripts/verify_implementation_oracle.py` independently reconstructs the retained
execution test output. Each repetition requires fixture and reference controls,
unique nonzero task UUIDs and leases, exact source digests reconstructed from
the benchmark, completed isolated Docker execution and ordered oracle outcomes.
The reference must pass all eight cases and the fixture must fail its defect.
CI publishes this report and artifact replay recomputes it from the downloaded
log. The report continues to state that model quality and provider provenance
are unverified.

`automationagent.ExecuteImplementationPilotResponse` connects a bounded response
to the executor. It rejects ambiguous JSON, foreign/duplicate paths, extra fields,
NUL and empty content, then creates an owned temporary directory containing only
the two replacement files and embedded evaluator module/oracle. The prepared
digest must match before Docker runs. It retains the raw response SHA-256 with
the output and lease, and removes its temporary directory afterward. It does
not modify the caller workspace or authorize a task. CI exercises synthetic
fixture/reference responses; no actual provider response has been run yet.

New observed gateway receipts retain `_itbem_response` metadata in the immutable
usage ledger: version `utf8-final-answer-v1`, SHA-256 of the exact final-answer
UTF-8 bytes, and byte length. The gateway computes this after discarding
provider-supplied bindings; it never copies the answer body into the ledger.
Evaluation export exposes nullable `response_sha256` and `response_bytes`.
Historical, malformed or unavailable bindings stay null. Compare the retained
response bytes and execution response hash with this digest before attributing
an oracle outcome to a receipt. This digest identifies the gateway-observed
answer, not the provider HTTP body or a normalized/reserialized patch.

### Candidate execution control replay

CI retains `implementation-candidate-execution.json` and independently reconstructs it from the Go JSONL log during artifact replay. `scripts/verify_implementation_candidate_execution.py` requires both fixture and reference executions in every repetition, unique nonzero task/lease UUIDs, exact frozen source digests, and the SHA256 of the exact synthetic Go JSON response envelope (including Go HTML escaping). A substituted or absent response hash fails verification. The defective fixture must fail the oracle and the reference must pass every required case.

These are controlled synthetic response executions. This report leaves model quality and provider provenance unmeasured; an actual model answer must still be retained and matched to its gateway receipt before its correctness or cost can be attributed to a model.

Candidate decoding rejects unpaired JSON Unicode surrogate escapes before Go can replace them with U+FFFD. Valid surrogate pairs, literal replacement characters and text containing an escaped backslash remain valid. This preserves the same Unicode content contract as the Python candidate preparer.
