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

## Security audit trail

`middleware/audit.Mutations` writes one append-only `audit_logs` row for every
protected POST/PUT/PATCH/DELETE and for every 401/403 a handler returns,
whatever the method. Denials from the token and `applicationaccess.Require`
middleware (for example 403 `Application capability denied`) happen before
`Mutations` runs and are not in the trail. Rows are never updated or deleted
(database triggers in `configuration/gorm.go` enforce it).

One exception exists. When `POST /api/session/organization-context` succeeds
and its handler called `audit.SkipOnSuccess` (the body organization equals the
organization the request already selected through `X-Organization-ID`), no row
is written: the dashboard renews that credential every 4m15s and after each
session revalidation, and the renewal changes no state. The mark is honoured
only for a successful POST on that exact route, so no other handler can use it
to hide a state change. Still audited:

- organization switches (body organization differs from the header, or no
  header organization);
- 400, 401, 403 (including an organization outside the session) and 5xx;
- every other route, marked or not.

When reviewing the trail, the absence of organization-context rows during an
idle session is expected; explicit switches and denials remain visible.

## Event clients kill switch (`EVENT_CLIENT_ACCESS`)

EventiApp event clients are off unless the GitHub Actions repository variable
`EVENT_CLIENT_ACCESS` is `on`. The deploy workflow renders it into the runtime
env file (`--optional EVENT_CLIENT_ACCESS`), so a change only takes effect after
a `deploy-backend` run (workflow_dispatch with `confirm_production`). Rollback
time is one full deploy; record the measured duration on the first toggle.

- Off: grants are kept but ignored; clients get 403 (no organization) on their
  next request after the deploy, and the event-client routes answer 404.
- On: watch audit rows on `/api/events/:id/clients*` and
  `/api/clients/:id/members/event-clients*`, the 403 rate, and the Owner
  oversight list.
- A revocation is live for per-event authorization immediately and for the
  cached session within 15 s on other instances.

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
