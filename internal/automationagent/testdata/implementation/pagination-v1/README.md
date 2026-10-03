# Pagination implementation case v1

Prepared synthetic benchmark; no model has been evaluated on this case.

Repair the supplied Go package so pages are one-based, page < 1 defaults to 1,
size < 1 defaults to 2, offset advances by page size, the final page is bounded
by the available records, and a page beyond the end returns an empty slice.
Returned pages must own their storage so callers cannot mutate the source.

The fixture contains two defects across page.go and store.go. The reference
repairs both. Only those two source files may be changed by a future candidate.
go.mod and the evaluator-owned oracle are fixed; candidate tests cannot replace
the oracle. Both fixture and reference are separate standard-library Go modules,
outside the application build.

Validation so far: baseline fails, reference passes six cases, each single-file
repair fails, and a reference variant returning a source slice fails.
The oracle uses independent input per subtest to prevent cascading failures.

A future model run must retain source revision, prompt, original patch, route,
receipt, usage and costs. Execute candidate code only through the existing
authorized isolated sandbox. Local fixture/reference controls validate the
evaluator; they do not measure model quality or authorize production changes.