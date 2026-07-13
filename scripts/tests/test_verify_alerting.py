import unittest
from unittest.mock import patch

from verify_alerting import (
    VerificationError,
    configured_alarms,
    confirmed_email_subscription,
    verify_subscription_route,
)


TOPIC = "arn:aws:sns:us-east-2:123456789012:eventiapp-backend-alerts-prod"


class VerifyAlertingTests(unittest.TestCase):
    @patch("verify_alerting.aws_json")
    def test_subscription_verification_uses_topic_region(self, aws_json_mock):
        topic = "arn:aws:sns:us-east-1:123456789012:eventiapp-backend-public-health-alerts-prod"
        aws_json_mock.side_effect = [
            {"Attributes": {"TopicArn": topic}},
            {
                "Subscriptions": [
                    {
                        "Protocol": "email",
                        "Endpoint": "ops@example.com",
                        "SubscriptionArn": f"{topic}:uuid",
                    }
                ]
            },
        ]

        verify_subscription_route(
            topic,
            "ops@example.com",
            "eventiapp-backend-public-health-alerts-prod",
        )

        for call in aws_json_mock.call_args_list:
            self.assertIn("--region", call.args[0])
            self.assertEqual(call.args[0][call.args[0].index("--region") + 1], "us-east-1")

    def test_requires_a_confirmed_matching_email(self):
        payload = {
            "Subscriptions": [
                {
                    "Protocol": "email",
                    "Endpoint": "ops@example.com",
                    "SubscriptionArn": "PendingConfirmation",
                }
            ]
        }

        with self.assertRaisesRegex(VerificationError, "confirmed email"):
            confirmed_email_subscription(payload, "ops@example.com")

    def test_accepts_one_confirmed_matching_email(self):
        subscription = {
            "Protocol": "email",
            "Endpoint": "ops@example.com",
            "SubscriptionArn": "arn:aws:sns:us-east-2:123456789012:topic:uuid",
        }

        self.assertEqual(
            confirmed_email_subscription({"Subscriptions": [subscription]}, "OPS@example.com"),
            subscription["SubscriptionArn"],
        )

    def test_rejects_pending_duplicate_of_confirmed_recipient(self):
        confirmed = {
            "Protocol": "email",
            "Endpoint": "ops@example.com",
            "SubscriptionArn": "arn:aws:sns:us-east-2:123456789012:topic:uuid",
        }
        pending = {
            "Protocol": "email",
            "Endpoint": "OPS@example.com",
            "SubscriptionArn": "PendingConfirmation",
        }

        with self.assertRaisesRegex(VerificationError, "matching=2 confirmed=1"):
            confirmed_email_subscription(
                {"Subscriptions": [confirmed, pending]}, "ops@example.com"
            )

    def test_rejects_disabled_alarm_actions(self):
        payload = {
            "MetricAlarms": [
                {
                    "AlarmName": "eventiapp-backend-health-prod",
                    "ActionsEnabled": False,
                    "AlarmActions": [TOPIC],
                    "OKActions": [TOPIC],
                }
            ]
        }

        with self.assertRaisesRegex(VerificationError, "disabled"):
            configured_alarms(
                payload,
                TOPIC,
                ["eventiapp-backend-health-prod"],
                ["eventiapp-backend-health-prod"],
            )

    def test_requires_exact_alarm_names_and_silent_leaf_alarms(self):
        payload = {
            "MetricAlarms": [
                {
                    "AlarmName": "eventiapp-backend-health-leaf-prod",
                    "AlarmActions": [],
                    "OKActions": [],
                },
                {
                    "AlarmName": "eventiapp-backend-health-prod",
                    "ActionsEnabled": True,
                    "AlarmActions": [TOPIC],
                    "OKActions": [TOPIC],
                }
            ]
        }

        self.assertEqual(
            configured_alarms(
                payload,
                TOPIC,
                [
                    "eventiapp-backend-health-leaf-prod",
                    "eventiapp-backend-health-prod",
                ],
                ["eventiapp-backend-health-prod"],
            ),
            ["eventiapp-backend-health-prod"],
        )

    def test_stale_count_cannot_replace_a_missing_expected_alarm(self):
        payload = {
            "MetricAlarms": [
                {
                    "AlarmName": "eventiapp-backend-stale-prod",
                    "AlarmActions": [TOPIC],
                    "OKActions": [TOPIC],
                }
            ]
        }

        with self.assertRaisesRegex(VerificationError, "missing"):
            configured_alarms(
                payload,
                TOPIC,
                ["eventiapp-backend-health-prod"],
                ["eventiapp-backend-health-prod"],
            )

    def test_leaf_alarm_cannot_page_independently(self):
        payload = {
            "MetricAlarms": [
                {
                    "AlarmName": "eventiapp-backend-health-leaf-prod",
                    "AlarmActions": [TOPIC],
                    "OKActions": [],
                }
            ]
        }

        with self.assertRaisesRegex(VerificationError, "duplicate"):
            configured_alarms(
                payload,
                TOPIC,
                ["eventiapp-backend-health-leaf-prod"],
                [],
            )

    def test_unexpected_alarm_in_scope_fails_closed(self):
        payload = {"MetricAlarms": [
            {
                "AlarmName": "eventiapp-backend-health-prod",
                "AlarmActions": [TOPIC],
                "OKActions": [TOPIC],
            },
            {
                "AlarmName": "eventiapp-backend-orphan-prod",
                "AlarmActions": [TOPIC],
                "OKActions": [TOPIC],
            },
        ]}
        with self.assertRaisesRegex(VerificationError, "unexpected"):
            configured_alarms(
                payload,
                TOPIC,
                ["eventiapp-backend-health-prod"],
                ["eventiapp-backend-health-prod"],
            )

    def test_environment_scope_ignores_only_the_other_known_environment(self):
        payload = {"MetricAlarms": [
            {
                "AlarmName": "eventiapp-backend-health-prod",
                "AlarmActions": [TOPIC],
                "OKActions": [TOPIC],
            },
            {
                "AlarmName": "eventiapp-backend-health-staging",
                "AlarmActions": ["arn:aws:sns:us-east-2:123456789012:staging"],
                "OKActions": ["arn:aws:sns:us-east-2:123456789012:staging"],
            },
        ]}
        self.assertEqual(
            configured_alarms(
                payload,
                TOPIC,
                ["eventiapp-backend-health-prod"],
                ["eventiapp-backend-health-prod"],
                "-prod",
            ),
            ["eventiapp-backend-health-prod"],
        )

    def test_environment_scope_rejects_legacy_unsuffixed_alarm(self):
        payload = {"MetricAlarms": [
            {
                "AlarmName": "eventiapp-backend-health-prod",
                "AlarmActions": [TOPIC],
                "OKActions": [TOPIC],
            },
            {
                "AlarmName": "eventiapp-backend-legacy",
                "AlarmActions": [TOPIC],
                "OKActions": [TOPIC],
            },
        ]}
        with self.assertRaisesRegex(VerificationError, "environment-less"):
            configured_alarms(
                payload,
                TOPIC,
                ["eventiapp-backend-health-prod"],
                ["eventiapp-backend-health-prod"],
                "-prod",
            )


if __name__ == "__main__":
    unittest.main()
