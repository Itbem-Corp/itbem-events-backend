# Offline model evaluation reports

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
