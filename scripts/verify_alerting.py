#!/usr/bin/env python3
"""Fail-closed verification for an SNS-backed CloudWatch alarm route.

The script intentionally uses the AWS CLI instead of an SDK so deployment
runners need no extra Python dependencies.  It never publishes a message or
changes alarm state; explicit delivery drills remain a human operation.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from typing import Any


EMAIL_PATTERN = re.compile(r"^[^@\s]+@[^@\s]+\.[^@\s]+$")
TOPIC_PATTERN = re.compile(r"^arn:aws[a-zA-Z-]*:sns:[a-z0-9-]+:[0-9]{12}:.+$")


class VerificationError(RuntimeError):
    """The alert path is present but cannot deliver actionable notifications."""


def confirmed_email_subscription(payload: dict[str, Any], expected_email: str) -> str:
    """Return the confirmed subscription ARN for ``expected_email``."""

    matches = [
        item
        for item in payload.get("Subscriptions", [])
        if item.get("Protocol") == "email"
        and str(item.get("Endpoint", "")).casefold() == expected_email.casefold()
    ]
    confirmed = [
        item
        for item in matches
        if str(item.get("SubscriptionArn", "")).startswith("arn:")
    ]
    if len(matches) != 1 or len(confirmed) != 1:
        raise VerificationError(
            "expected exactly one matching confirmed email subscription for "
            f"{expected_email!r}; matching={len(matches)} confirmed={len(confirmed)}"
        )
    return str(confirmed[0]["SubscriptionArn"])


def configured_alarms(
    payload: dict[str, Any],
    topic_arn: str,
    expected_names: list[str],
    routed_names: list[str],
    scope_suffix: str | None = None,
) -> list[str]:
    """Validate the exact alarm set and its deliberate notification boundary.

    Counting alarms by prefix can be fooled by stale alarms from an earlier
    deployment.  Callers therefore provide every expected name and the subset
    that is allowed to notify. Leaf alarms must remain silent so a composite
    incident does not fan out into duplicate pages.
    """

    if not expected_names or len(expected_names) != len(set(expected_names)):
        raise VerificationError("expected alarm names must be non-empty and unique")
    if len(routed_names) != len(set(routed_names)) or not set(routed_names).issubset(
        expected_names
    ):
        raise VerificationError("routed alarm names must be unique expected alarms")

    alarms: dict[str, dict[str, Any]] = {}
    for group in ("MetricAlarms", "CompositeAlarms"):
        for alarm in payload.get(group, []):
            name = str(alarm.get("AlarmName", ""))
            if name in alarms:
                raise VerificationError(f"alarm {name!r} was returned more than once")
            alarms[name] = alarm
    if scope_suffix is not None:
        if scope_suffix not in ("-staging", "-prod") or any(
            not name.endswith(scope_suffix) for name in expected_names
        ):
            raise VerificationError("alarm scope suffix does not match expected names")
        orphaned = sorted(
            name
            for name in alarms
            if not name.endswith(("-staging", "-prod"))
        )
        if orphaned:
            raise VerificationError(f"environment-less alarms exist: {orphaned}")
        alarms = {
            name: alarm for name, alarm in alarms.items() if name.endswith(scope_suffix)
        }

    missing = [name for name in expected_names if name not in alarms]
    if missing:
        raise VerificationError(f"expected alarms are missing: {missing}")
    unexpected = sorted(set(alarms).difference(expected_names))
    if unexpected:
        raise VerificationError(f"unexpected alarms exist in the verified scope: {unexpected}")

    routed = set(routed_names)
    for name in expected_names:
        alarm = alarms[name]
        actions = [str(value) for value in alarm.get("AlarmActions", [])]
        ok_actions = [str(value) for value in alarm.get("OKActions", [])]
        if name in routed:
            if alarm.get("ActionsEnabled", True) is not True:
                raise VerificationError(f"alarm actions are disabled for {name!r}")
            if actions != [topic_arn] or ok_actions != [topic_arn]:
                raise VerificationError(
                    f"alarm {name!r} must route ALARM and OK exactly once to "
                    f"{topic_arn!r}"
                )
        elif actions or ok_actions:
            raise VerificationError(
                f"leaf alarm {name!r} must not send duplicate notifications"
            )
    return routed_names


def aws_json(arguments: list[str]) -> dict[str, Any]:
    result = subprocess.run(
        ["aws", *arguments, "--output", "json"],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or "unknown AWS CLI error"
        raise VerificationError(f"AWS CLI failed: {detail}")
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise VerificationError("AWS CLI returned invalid JSON") from error


def verify_subscription_route(
    topic_arn: str,
    expected_email: str,
    expected_topic_name: str | None = None,
) -> str:
    if not TOPIC_PATTERN.fullmatch(topic_arn):
        raise VerificationError("topic ARN is not a valid SNS ARN")
    if not EMAIL_PATTERN.fullmatch(expected_email):
        raise VerificationError("monitored alert email is missing or invalid")
    if expected_topic_name is not None:
        if not re.fullmatch(r"[A-Za-z0-9_-]+", expected_topic_name):
            raise VerificationError("expected topic name is invalid")
        if topic_arn.rsplit(":", 1)[-1] != expected_topic_name:
            raise VerificationError("SNS topic ARN does not match the expected topic name")

    topic_region = topic_arn.split(":", 5)[3]
    attributes = aws_json(
        [
            "sns",
            "get-topic-attributes",
            "--topic-arn",
            topic_arn,
            "--region",
            topic_region,
        ]
    ).get("Attributes", {})
    if attributes.get("TopicArn") != topic_arn:
        raise VerificationError("SNS topic attributes do not match the expected ARN")

    subscriptions = aws_json(
        [
            "sns",
            "list-subscriptions-by-topic",
            "--topic-arn",
            topic_arn,
            "--region",
            topic_region,
        ]
    )
    return confirmed_email_subscription(subscriptions, expected_email)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--topic-arn", required=True)
    parser.add_argument("--expected-topic-name")
    parser.add_argument("--email", required=True)
    parser.add_argument("--subscription-only", action="store_true")
    parser.add_argument("--alarm-prefix")
    parser.add_argument("--alarm-suffix", choices=("-staging", "-prod"))
    parser.add_argument("--expected-alarm", action="append", default=[])
    parser.add_argument("--routed-alarm", action="append", default=[])
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    subscription_arn = verify_subscription_route(
        args.topic_arn,
        args.email,
        args.expected_topic_name,
    )
    if args.subscription_only:
        print(
            "alert subscription verified: "
            f"subscription={subscription_arn} topic={args.topic_arn}"
        )
        return 0

    if not args.alarm_prefix or not args.alarm_prefix.strip():
        raise VerificationError("alarm prefix must not be empty")
    if args.alarm_suffix is None:
        raise VerificationError("alarm suffix is required")
    if not args.expected_alarm or not args.routed_alarm:
        raise VerificationError("expected and routed alarm names are required")

    alarms = aws_json(
        [
            "cloudwatch",
            "describe-alarms",
            "--alarm-name-prefix",
            args.alarm_prefix,
            "--alarm-types",
            "MetricAlarm",
            "CompositeAlarm",
        ]
    )
    routed = configured_alarms(
        alarms,
        args.topic_arn,
        args.expected_alarm,
        args.routed_alarm,
        args.alarm_suffix,
    )

    print(
        "alerting verified: "
        f"subscription={subscription_arn} routed_alarms={len(routed)} "
        f"topic={args.topic_arn}"
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except VerificationError as error:
        print(f"alerting verification failed: {error}", file=sys.stderr)
        raise SystemExit(1) from error
