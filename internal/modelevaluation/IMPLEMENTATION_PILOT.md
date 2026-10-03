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
concurrent dispatch and rejection of a new batch ID after halted history.
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
