"""Verify retained candidate Docker controls and exact response/source digests."""
import argparse
from pathlib import Path

from evaluation_report import publish_report
from verify_implementation_sandbox import verify as verify_sandbox


def verify(raw, repetitions=2):
    return verify_sandbox(raw, repetitions, candidate_entry=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--log', required=True, type=Path)
    parser.add_argument('--repetitions', type=int, default=2)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    publish_report(args.output, verify(args.log.read_bytes(), args.repetitions))
