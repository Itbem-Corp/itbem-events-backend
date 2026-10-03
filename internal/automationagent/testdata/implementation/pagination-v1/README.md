# Pagination implementation case v1

Prepared synthetic benchmark; no model has been evaluated on this case.

Repair the supplied Go package so pages are one-based, page < 1 defaults to 1,
size < 1 defaults to 2, offset advances by page size without integer overflow, the final page is bounded
by the available records, and a page beyond the end returns an empty slice.
Returned pages must own their storage so callers cannot mutate the source.

The fixture contains two defects across page.go and store.go. The reference
repairs both. Only those two source files may be changed by a future candidate.
go.mod and the evaluator-owned oracle are fixed; candidate tests cannot replace
the oracle. Both fixture and reference are separate standard-library Go modules,
outside the application build.

Validation so far: baseline fails, reference passes eight cases, each single-file
repair fails, and a reference variant returning a source slice fails.
The oracle uses independent input per subtest to prevent cascading failures.

A future model run must retain source revision, prompt, original patch, route,
receipt, usage and costs. Execute candidate code only through the existing
authorized isolated sandbox. Local fixture/reference controls validate the
evaluator; they do not measure model quality or authorize production changes.
Run evaluator controls with pinned Go 1.25.13:
python3 scripts/verify_implementation_benchmark.py --output <new-report.json>
The output parent must exist. Reports use exclusive publication and retain source
hashes, actual logs and exits. CI runs these controls before canonical regression
and preserves implementation-controls.json alongside regression evidence.
The runner accepts no candidate path and makes no provider calls.

Prepare the model-facing input without running a model:
python3 scripts/prepare_implementation_evaluation.py --output <new-input.json>
Send only the prompt field to the authorized model task. The other fields retain
provenance for evaluation; private_oracle_sha256 is not model input. The prompt
contains only the fixture and task, never the reference or oracle. Preparation
does not authorize provider spend, execute a candidate, or measure model quality.

Validate response structure without executing or writing candidate source:
python3 scripts/validate_implementation_response.py --response <response.json> --output <new-report.json>
The raw response is limited to 64 KiB, uses strict JSON and contains exactly one
replacement for each editable file. Other paths, duplicate fields, empty content,
NUL and invalid UTF-8 are rejected. The report hashes the response and replacement
contents. response_contract_valid does not mean the Go package compiles or meets
the task: candidate_executed remains false and implementation_correctness null.

Model-facing fixture text uses LF line endings so Windows CRLF and Linux LF
checkouts produce the same prompt. fixture_sha256 hashes normalized model input;
fixture_raw_sha256 separately identifies the original checkout bytes. This is
line-ending normalization only, not rewriting candidate answers or the oracle.

Invalid responses now publish an explicit failed validation report and return
exit 1, so failed attempts remain inspectable. Complete inputs retain their exact
response hash. Oversized inputs are read only to 64 KiB + 1; their report records
the observed byte count, response_read_complete=false and response_sha256=null.
Publication still refuses existing destinations. No failed response files are
promoted as replacements, and no candidate code is executed.

Prepare sandbox package data without materializing or executing source:
python3 scripts/prepare_implementation_candidate.py --response <response.json> --output <new-package.json>
The package contains the two validated replacement files plus evaluator-owned
go.mod and page_test.go, with hashes for every file. Invalid responses retain
their failed assessment and yield no package files. Only an authorized isolated
sandbox may consume this data. prepared_prompt_sha256 identifies the current
task input; it does not attest which prompt a model received. Bind that separately
to the actual central-gateway receipt before attributing model quality.
