# Offline model evaluation reports

The root-scoped backend provenance endpoint retains partial batches for inspection, but rejects exports with duplicate task rows or more than 60 joined outcomes. It reads one extra row to detect receipt join expansion instead of truncating away another task. An ambiguous export is unavailable for scoring; this does not modify durable receipts or trigger another inference.

Each candidate has `by_category` results with the frozen corpus category, denominator, successes, failures, JSON validity and execution errors. Each case also retains its category. Category totals reconcile with the overall totals; failures remain counted. Screening covers five categories with unequal sizes, while the cache corpus covers only defective and clean code review. Category rates describe these small synthetic samples, not general role competence or a statistically established ranking.

`reported_complete` preserves whether the batch declares completion. `complete` also requires all 60 recorded tasks to be completed with accepted receipts; `incomplete_outcome_count` discloses contradictory unfinished/unaccepted rows. A completed run may still contain wrong or invalid model answers, so completion does not mean quality success. An active batch is never promoted to complete solely because its rows appear completed. Cache qualification uses the checked `complete` value.

Each scored case includes `failure_reasons`; each candidate includes `failure_reason_counts`. Reasons distinguish task/receipt failures, unexpected routes, truncation, recorded result errors, invalid JSON and exact-answer mismatches. They may overlap on one case, so their counts must not be summed as a number of failed cases. A parsed JSON object with the wrong keys, types or values is an answer mismatch; invalid JSON is a separate failure. Successes, denominators and accounting retain their existing definitions. Cache analysis includes these diagnostics in its nested `screening` report.

Every supplied run and receipt identity must be a nonblank string without surrounding whitespace, and may occur only once per export. Accepted outcomes require both identities. Failed outcomes may omit identities that were never assigned and remain in the denominator; reused identities are rejected even on failed rows. This prevents counting an exported receipt more than once, but does not authenticate the receipt itself.

Score exported gateway outcomes against the frozen screening corpus:

```sh
python scripts/score_model_evaluation.py --corpus scripts/model-evaluation-screening-corpus.json --evidence receipts.json --output screening-report.json
```

For the matched-prefix cache experiment, use `scripts/analyze_cache_evaluation.py` with `scripts/model-evaluation-cache-corpus.json` and the matching exported receipts.

The scorer compares the complete parsed corpus with its checked-in frozen reference, including expected answer types and case-list entries. Formatting may differ, but changing answers, prompts, metadata or cases requires a separately reviewed corpus revision. Receipts must carry the matching `batch.corpus_version`: `synthetic-screening-20-2026-09-30-v1` for screening or `synthetic-prefix-cache-20-2026-10-01-v1` for cache. Missing versions are rejected rather than inferred. The local reference is part of the trusted checkout; this comparison does not authenticate that checkout.

Choose a new output filename for each report. Both commands publish a completed report without replacing an existing file, including input evidence or a hard link to it. Publication requires hard-link support on the destination filesystem; unsupported filesystems fail without an overwrite fallback. Concurrent writers have one winner. This guarantees complete-file visibility during publication, not durability through filesystem or power failure.

Each CLI report includes `input_sha256.corpus` and `input_sha256.evidence`, computed from the exact bytes read, including any BOM or whitespace. These hashes identify inputs; they do not authenticate gateway receipts or prove that an exported file is complete.

Input files use the same strict JSON decoder as model answers: duplicate decoded field names (including escaped aliases and nested duplicates) and `NaN`/infinity constants are rejected before scoring. Valid UTF-8 BOMs and repeated field names in separate objects are supported. Input decoding failure does not publish a report or change the input file.

The scorer treats duplicate decoded model-answer fields, including escaped aliases and nested duplicates, and non-JSON numeric constants as format failures. Failed answers remain in the denominator; known usage and costs remain counted. Exact answer matching, attribution and receipt checks still apply independently.

These commands are offline and make no provider calls. Synthetic fixtures validate the evaluator, not live model quality. Run their tests in Linux, where the full sandbox suite is supported:

```sh
PYTHONPATH=scripts python3 -m unittest discover -s scripts/tests -p 'test_*.py'
```
