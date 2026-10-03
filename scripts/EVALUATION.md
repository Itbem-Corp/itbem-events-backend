# Offline model evaluation reports

Score exported gateway outcomes against the frozen screening corpus:

```sh
python scripts/score_model_evaluation.py --corpus scripts/model-evaluation-screening-corpus.json --evidence receipts.json --output screening-report.json
```

For the matched-prefix cache experiment, use `scripts/analyze_cache_evaluation.py` with `scripts/model-evaluation-cache-corpus.json` and the matching exported receipts.

Choose a new output filename for each report. Both commands publish a completed report without replacing an existing file, including input evidence or a hard link to it. Publication requires hard-link support on the destination filesystem; unsupported filesystems fail without an overwrite fallback. Concurrent writers have one winner. This guarantees complete-file visibility during publication, not durability through filesystem or power failure.

Each CLI report includes `input_sha256.corpus` and `input_sha256.evidence`, computed from the exact bytes read, including any BOM or whitespace. These hashes identify inputs; they do not authenticate gateway receipts or prove that an exported file is complete.

The scorer treats duplicate decoded model-answer fields, including escaped aliases and nested duplicates, and non-JSON numeric constants as format failures. Failed answers remain in the denominator; known usage and costs remain counted. Exact answer matching, attribution and receipt checks still apply independently.

These commands are offline and make no provider calls. Synthetic fixtures validate the evaluator, not live model quality. Run their tests in Linux, where the full sandbox suite is supported:

```sh
PYTHONPATH=scripts python3 -m unittest discover -s scripts/tests -p 'test_*.py'
```
