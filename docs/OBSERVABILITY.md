# Observability contract

Backend and worker logs are single-line JSON delivered directly by Docker's
`awslogs` driver. Do not wrap application JSON with the `json-file` driver or a
second shipper: CloudWatch metric filters depend on top-level JSON fields.

Every operational event uses stable snake_case keys. The common fields are
`service`, `environment`, `source_revision`, `component`, `event`, and `level`.
HTTP work adds `request_id` and `correlation_id`. Durable jobs preserve that
`correlation_id` through the outbox and add `job_id`, `application`, `lane`,
`job_type`, and `attempt` in the worker.

Successful `/health` requests are intentionally omitted. Failed health checks
remain request logs at WARN or ERROR. Other HTTP responses use INFO below 400,
WARN for 4xx, and ERROR for 5xx.

Never log authorization headers, cookies, request bodies, Slack webhooks,
queue receipt handles, invitation tokens, passwords, or raw query values.
Route templates and query-key names are safe; query values remain redacted.

Production application logs are retained for 14 days. Durable security audit
records and worker execution state live in PostgreSQL and are not replaced by
CloudWatch logs.

The infrastructure installs four CloudWatch Logs Insights saved queries under
`EventiApp/`: correlation tracing across backend/worker/media, operational
errors, slow HTTP requests, and failed worker jobs. Replace the placeholder in
the correlation query before running it. Saved definitions do not execute on a
schedule and therefore do not scan logs until an operator runs them.

After a coordinated release, perform this drill:

1. Send one synthetic request that queues a Slack notification and retain its
   response request ID.
2. Query that ID and verify backend request, outbox completion, SNS/SQS worker
   start, and worker completion events.
3. Queue one staging image and verify the same correlation reaches the Lambda
   callback.
4. Trigger a controlled application-level 5xx in staging and confirm
   `EventiApp/Backend/ServerErrors` increments; do not add a production-only
   failure endpoint for this purpose.
5. Confirm all production DLQs remain empty after the retry window.

Staging uses the single public Slack channel `apps-staging-notifications`
(`C0BHZ71HU66`). Its SSM parameter remains
`/eventiapp/notifications/slack/staging/general`; populate it only with the
Incoming Webhook created for that exact channel. Never copy a production app
webhook into this path.
