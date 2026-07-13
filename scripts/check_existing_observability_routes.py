#!/usr/bin/env python3
"""Fail closed before updating existing backend observability routes."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass
from typing import Any, Callable

from verify_alerting import VerificationError, verify_subscription_route


Runner = Callable[..., subprocess.CompletedProcess[str]]
ABSENT_ERROR = re.compile(
    r"\(ValidationError\).*Stack(?: with id)? .+ does not exist",
    re.IGNORECASE | re.DOTALL,
)


@dataclass(frozen=True)
class RouteSpec:
    region: str
    stack_name: str
    topic_name: str


def describe_stack_topic(
    spec: RouteSpec,
    runner: Runner = subprocess.run,
) -> str | None:
    result = runner(
        [
            "aws",
            "cloudformation",
            "describe-stacks",
            "--stack-name",
            spec.stack_name,
            "--region",
            spec.region,
            "--output",
            "json",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or "unknown AWS CLI error"
        if ABSENT_ERROR.search(detail):
            return None
        raise VerificationError(
            f"cannot inspect existing stack {spec.stack_name!r} in {spec.region}: {detail}"
        )

    try:
        payload: dict[str, Any] = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise VerificationError("CloudFormation returned invalid JSON") from error
    stacks = payload.get("Stacks", [])
    if not isinstance(stacks, list) or len(stacks) != 1:
        raise VerificationError(
            f"expected exactly one stack named {spec.stack_name!r} in {spec.region}"
        )
    outputs = stacks[0].get("Outputs", [])
    topics = [
        str(item.get("OutputValue", ""))
        for item in outputs
        if item.get("OutputKey") == "AlertTopicArn"
    ]
    if len(topics) != 1 or not topics[0]:
        raise VerificationError(
            f"existing stack {spec.stack_name!r} has no unique AlertTopicArn output"
        )
    return topics[0]


def route_specs(environment: str) -> list[RouteSpec]:
    if environment not in ("staging", "prod"):
        raise VerificationError("environment must be staging or prod")
    return [
        RouteSpec(
            "us-east-2",
            f"eventiapp-backend-{environment}-observability",
            f"eventiapp-backend-alerts-{environment}",
        ),
        RouteSpec(
            "us-east-1",
            f"eventiapp-backend-{environment}-public-health-observability",
            f"eventiapp-backend-public-health-alerts-{environment}",
        ),
    ]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--environment", choices=("staging", "prod"), required=True)
    parser.add_argument("--email", required=True)
    args = parser.parse_args()

    for spec in route_specs(args.environment):
        topic_arn = describe_stack_topic(spec)
        if topic_arn is None:
            print(f"alert route bootstrap allowed: stack={spec.stack_name} region={spec.region}")
            continue
        subscription_arn = verify_subscription_route(
            topic_arn,
            args.email,
            spec.topic_name,
        )
        print(
            "existing alert route verified before update: "
            f"stack={spec.stack_name} subscription={subscription_arn}"
        )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except VerificationError as error:
        print(f"observability preflight failed: {error}", file=sys.stderr)
        raise SystemExit(1) from error
