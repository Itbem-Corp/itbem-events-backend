"""Run qualification selectors and require their named tests to execute."""
import re
import subprocess
import sys
import tempfile
from pathlib import Path


def required_names(pattern):
    single = re.fullmatch(r"(Test[A-Za-z0-9_]+)\$", pattern)
    if single:
        return [single.group(1)]
    group = re.fullmatch(r"Test\(([A-Za-z0-9_|]+)\)\$", pattern)
    if not group:
        raise ValueError("qualification selectors must enumerate exact named tests")
    names = group.group(1).split("|")
    if any(not name for name in names) or len(set(names)) != len(names):
        raise ValueError("qualification test names must be unique and nonempty")
    return ["Test" + name for name in names]


def main(command):
    if command[:2] != ["go", "test"]:
        raise ValueError("expected go test")
    if "-run" not in command:
        return subprocess.run(command, check=False).returncode
    index = command.index("-run")
    names = required_names(command[index + 1])
    with tempfile.TemporaryDirectory(prefix="itbem-required-tests-") as directory:
        report = Path(directory) / "evidence.jsonl"
        with report.open("wb") as output:
            result = subprocess.run(command + ["-json"], stdout=output, check=False)
        if result.returncode:
            sys.stdout.buffer.write(report.read_bytes())
            return result.returncode
        verifier = Path(__file__).with_name("verify_go_test_evidence.py")
        return subprocess.run([sys.executable, str(verifier), str(report), *names], check=False).returncode


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except (ValueError, IndexError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(2)
