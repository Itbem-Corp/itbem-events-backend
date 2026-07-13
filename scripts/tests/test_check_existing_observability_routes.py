import json
import subprocess
import unittest

from check_existing_observability_routes import (
    RouteSpec,
    describe_stack_topic,
    route_specs,
)
from verify_alerting import VerificationError


SPEC = RouteSpec(
    "us-east-2",
    "eventiapp-backend-prod-observability",
    "eventiapp-backend-alerts-prod",
)
TOPIC = "arn:aws:sns:us-east-2:123456789012:eventiapp-backend-alerts-prod"


def completed(returncode=0, stdout="", stderr=""):
    return subprocess.CompletedProcess([], returncode, stdout=stdout, stderr=stderr)


class ExistingObservabilityRouteTests(unittest.TestCase):
    def test_only_exact_validation_error_does_not_exist_is_absent(self):
        def runner(*_args, **_kwargs):
            return completed(
                255,
                stderr=(
                    "An error occurred (ValidationError) when calling the "
                    "DescribeStacks operation: Stack with id "
                    "eventiapp-backend-prod-observability does not exist"
                ),
            )

        self.assertIsNone(describe_stack_topic(SPEC, runner))

    def test_access_denied_throttle_and_network_errors_abort(self):
        for detail in (
            "An error occurred (AccessDenied) when calling DescribeStacks",
            "An error occurred (ThrottlingException) when calling DescribeStacks",
            "Could not connect to the endpoint URL",
        ):
            with self.subTest(detail=detail):
                with self.assertRaises(VerificationError):
                    describe_stack_topic(
                        SPEC,
                        lambda *_args, **_kwargs: completed(255, stderr=detail),
                    )

    def test_existing_stack_requires_unique_topic_output(self):
        payload = {"Stacks": [{"Outputs": []}]}
        with self.assertRaisesRegex(VerificationError, "AlertTopicArn"):
            describe_stack_topic(
                SPEC,
                lambda *_args, **_kwargs: completed(stdout=json.dumps(payload)),
            )

    def test_existing_stack_returns_exact_topic_output(self):
        payload = {
            "Stacks": [
                {"Outputs": [{"OutputKey": "AlertTopicArn", "OutputValue": TOPIC}]}
            ]
        }
        self.assertEqual(
            describe_stack_topic(
                SPEC,
                lambda *_args, **_kwargs: completed(stdout=json.dumps(payload)),
            ),
            TOPIC,
        )

    def test_route_specs_are_region_and_environment_exact(self):
        specs = route_specs("staging")
        self.assertEqual([item.region for item in specs], ["us-east-2", "us-east-1"])
        self.assertTrue(all("staging" in item.stack_name for item in specs))


if __name__ == "__main__":
    unittest.main()
