"""Publish complete offline evaluation reports without replacing evidence."""
import hashlib
import json
import os
from pathlib import Path
import tempfile


def read_input(path):
    raw = Path(path).read_bytes()
    return json.loads(raw.decode('utf-8-sig')), hashlib.sha256(raw).hexdigest()


def publish_report(path, report):
    path = Path(path)
    payload = (json.dumps(report, indent=2, ensure_ascii=False, allow_nan=False) + '\n').encode('utf-8')
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, prefix='.evaluation-', delete=False) as output:
            temporary = Path(output.name)
            output.write(payload)
            output.flush()
            os.fsync(output.fileno())
        # Linking a completed file publishes it atomically and refuses any existing
        # destination, including aliases of input evidence. No overwrite fallback.
        os.link(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink()
