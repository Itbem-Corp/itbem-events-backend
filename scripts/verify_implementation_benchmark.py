"""Verify checked-in evaluator controls only; never accepts generated candidate code."""
import argparse
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from evaluation_report import decode_json, publish_report

ROOT = Path(__file__).resolve().parents[1]
CASE = ROOT / 'internal/automationagent/testdata/implementation/pagination-v1'
SUBTESTS = {'first', 'second', 'partial-tail', 'past-tail', 'invalid-defaults', 'oversized-limit'}


def verify(go='go'):
    files = {path.relative_to(CASE).as_posix(): path.read_bytes()
             for path in CASE.rglob('*') if path.is_file()}
    env = os.environ.copy()
    env.update(GOTOOLCHAIN='local', GOWORK='off', GOPROXY='off', GOSUMDB='off')
    version = subprocess.run([go, 'version'], env=env, capture_output=True, text=True,
                             check=True, timeout=15).stdout.strip()
    if not version.startswith('go version go1.25.13 '):
        raise ValueError('Implementation controls require pinned Go 1.25.13.')
    results = []
    for control in ('fixture', 'reference', 'offset-only', 'tail-only', 'aliases-source'):
        with tempfile.TemporaryDirectory(prefix='implementation-control-') as directory:
            tree = 'reference' if control in ('reference', 'aliases-source') else 'fixture'
            for name in ('go.mod', 'page.go', 'store.go'):
                Path(directory, name).write_bytes(files[tree + '/' + name])
            Path(directory, 'page_test.go').write_bytes(files['oracle/page_test.go'])
            if control == 'offset-only':
                Path(directory, 'page.go').write_bytes(files['reference/page.go'])
            if control == 'tail-only':
                Path(directory, 'store.go').write_bytes(files['reference/store.go'])
            if control == 'aliases-source':
                path = Path(directory, 'store.go')
                source = path.read_text()
                original = 'append([]string{}, items[offset:end]...)'
                if source.count(original) != 1:
                    raise ValueError('Alias control no longer matches the reference.')
                path.write_text(source.replace(original, 'items[offset:end]'))
            run = subprocess.run([go, 'test', '-json', '-count=1', './...'], cwd=directory,
                                 env=env, capture_output=True, text=True, timeout=45)
            events = [decode_json(line) for line in run.stdout.splitlines()]
            passed = {e.get('Test') for e in events if e.get('Action') == 'pass'}
            failed = {e.get('Test') for e in events if e.get('Action') == 'fail'}
            if control == 'reference':
                required = {'TestPaginationContract/' + name for name in SUBTESTS}
                if run.returncode or not required <= passed or failed:
                    raise ValueError('Reference must pass every oracle subtest.')
            else:
                required = {'fixture': 'second', 'offset-only': 'partial-tail',
                            'tail-only': 'second', 'aliases-source': 'first'}[control]
                if run.returncode != 1 or 'TestPaginationContract/' + required not in failed:
                    raise ValueError('Defective control did not fail its required oracle test.')
            results.append({'control': control, 'exit_code': run.returncode,
                            'stdout': run.stdout, 'stderr': run.stderr,
                            'stdout_sha256': hashlib.sha256(run.stdout.encode()).hexdigest()})
    if any((CASE / name).read_bytes() != raw for name, raw in files.items()):
        raise ValueError('Checked-in benchmark changed during verification.')
    return {'schema_version': 1, 'case': 'pagination-v1', 'go_version': version,
            'evaluator_controls_verified': True, 'model_quality_measured': False,
            'provider_calls': 0, 'results': results,
            'source_sha256': {name: hashlib.sha256(raw).hexdigest() for name, raw in files.items()}}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go', default=shutil.which('go') or 'go')
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    publish_report(args.output, verify(args.go))
