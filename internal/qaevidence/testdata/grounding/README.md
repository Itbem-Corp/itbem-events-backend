# Synthetic grounding replay

These frozen files are invented local test data, not a runtime capture or a model
quality evaluation. The observation has a passing preview/unit check and a failed
security check. The correct claims retain that failure. The adversarial claims
invent a repository while preserving the expected global failed verdict.

From the repository root, with the required Go toolchain:

```sh
go run ./cmd/score-qa-grounding -observation internal/qaevidence/testdata/grounding/observation.json -claims internal/qaevidence/testdata/grounding/grounded-claims.json
go run ./cmd/score-qa-grounding -observation internal/qaevidence/testdata/grounding/observation.json -claims internal/qaevidence/testdata/grounding/invented-claims.json
```

The first exits 0 with `passed=true`: the claims match the evidence, including
its failed security result. This does **not** mean QA passed. The JSON identifies `score_kind=structured_qa_grounding` and independently shows `observed_qa_verdict=failed` and `claimed_qa_verdict=failed`. The second exits 1
with `passed=false` and an unknown-command identity diagnostic. `go run` may also
print `exit status 1` to stderr for the expected negative case.

To retain evidence add `-output <new-score-path>`; existing files are protected.
CLI regression tests replay both fixtures, including the precise rejection reason.
No credentials, inference requests, application execution or release are involved.
