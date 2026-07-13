# Backend operations runbook

Every production alarm must route to a confirmed, monitored SNS email
subscription. A CloudFormation stack reaching `CREATE_COMPLETE` is not proof of
delivery: email subscriptions remain `PendingConfirmation` until a person
accepts the AWS message. The deployment verifier rejects that state.

## Public API health alarm

1. Confirm the customer impact from two independent networks:
   `curl --fail --show-error --silent https://api.eventiapp.com.mx/health`.
2. Inspect the active container and recent structured logs on the API host.
3. Check PostgreSQL, Redis, nginx, certificate validity, and the latest
   deployment revision. `/health` returns `503` when a required dependency is
   degraded and exposes a non-secret `data.environment` marker; production
   gates must require `production`, while isolated staging gates require
   `staging`.
4. If the incident began with a deployment, use the candidate deployment
   rollback path before attempting manual data repair.
5. Record the incident time, alarm history, revision, and recovery action.

## EC2 status alarm

`StatusCheckFailed_System` points to AWS host or network infrastructure;
`StatusCheckFailed_Instance` points to the guest OS. Inspect the EC2 status
check details and system log before rebooting. Prefer an AWS instance recovery
action for a system failure. For an instance failure, capture logs and disk
state first, then restart only when the evidence supports it.

## Alert-route verification

Provision `infra/observability.yml` with a real operations inbox, accept the
SNS confirmation, then run:

```bash
python3 scripts/verify_alerting.py \
  --topic-arn "$BACKEND_ALERT_TOPIC_ARN" \
  --email "$BACKEND_ALERT_EMAIL" \
  --alarm-prefix "eventiapp-backend-" \
  --alarm-suffix="-$ENV" \
  --expected-alarm "eventiapp-backend-ec2-system-status-$ENV" \
  --expected-alarm "eventiapp-backend-ec2-instance-status-$ENV" \
  --expected-alarm "eventiapp-backend-host-$ENV" \
  --routed-alarm "eventiapp-backend-host-$ENV"
```

The command is read-only. A delivery drill is separate and must be deliberate:
publish a clearly labelled test message, verify receipt in the monitored inbox,
then record the time and recipient. Never use `set-alarm-state` on production
automation without an approved drill window.

## Rust worker DLQ alarm

Preserve the message and correlate its job ID, receive count, worker revision,
execution lease, and structured ECS logs. Fix and test the root cause before a
single-message staging redrive. Never delete DLQ evidence just to clear an
alarm.

## Rust worker queue age alarm

Compare queue depth and age to ECS desired/running task count, SQS receive
errors, database readiness, and visibility heartbeats. A desired count of zero
is valid only during an explicit staged rollout.

## Rust worker error alarm

Inspect the worker JSON error records for SQS, Postgres, job, lease, heartbeat,
or health-server failures. Failed jobs remain unacknowledged so the queue's
redrive policy, not an ad-hoc loop, owns retry behavior.

## Rust worker task alarm

Inspect ECS service events and stopped-task reasons. Verify ECR, Secrets
Manager, subnet egress, security groups, and container readiness. Confirm stable
running task count and queue recovery after the deployment circuit breaker has
completed any automatic rollback.
