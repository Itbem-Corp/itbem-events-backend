# Slack Notifications

The backend exposes a reusable Slack notification module for operational and
product events. Each deployed application is wired to exactly one logical
destination; individual messages cannot select or override it. Webhook URLs
remain encrypted in AWS Systems Manager Parameter Store.

## Destinations

| Application / section | Slack channel | SSM parameter |
|---|---|---|
| `operations / general` | `#app-notifications` | `/eventiapp/notifications/slack/operations/general` |
| `eventiapp / general` | `#eventiapp-notifications` | `/eventiapp/notifications/slack/eventiapp/general` (workers only) |
| `cafettonhouse / general` | `#cafettonhouse-notifications` | `/eventiapp/notifications/slack/cafettonhouse/general` |
| `itbem / general` | `#itbem-notifications` | `/eventiapp/notifications/slack/itbem/general` |
| `workers / general` | `#workers-notifications` | `/eventiapp/notifications/slack/workers/general` |

Callers cannot provide destinations or arbitrary webhook URLs. The composition
root creates a notifier scoped to the application identity and its `general`
section. The repository
retrieves only its allow-listed parameter and caches the decrypted value for 15
minutes. IAM must grant that workload access to only the matching parameter.

## Sending a notification

```go
err := notifications.Send(ctx, dtos.SlackNotification{
    Severity:    dtos.SlackSeverityWarning,
    Title:       "Certificate nearing expiration",
    Summary:     "The API certificate needs attention.",
    Fields: []dtos.SlackField{
        {Label: "Domain", Value: "api.eventiapp.com.mx"},
        {Label: "Days remaining", Value: "14"},
        {Label: "Environment", Value: "Production"},
    },
    ThumbnailURL: "https://cdn.example.com/icons/tls-warning.png",
    ImageURL:     "https://cdn.example.com/charts/certificate-validity.png",
    ImageAlt:     "Certificate validity timeline",
    Context:      []string{"Automated TLS monitor", "Checked 2026-07-17 08:15 UTC"},
    Actions: []dtos.SlackAction{
        {Label: "Open dashboard", URL: "https://dashboard.eventiapp.com.mx", Style: "primary"},
    },
})
```

`Send` persists a `notification.slack` outbox job. The API never reads a
webhook and never calls Slack. `itbem-events-workers` consumes the SQS job,
builds Block Kit, reads only EventiApp's `general` SecureString, and performs
delivery with the existing retry and DLQ behavior.

`notifications.Send` creates only EventiApp jobs; it carries no route or webhook.
The EventiApp worker is configured with one exact SSM parameter and cannot
redirect delivery to Cafetton House, ITBEM, Workers, or Operations.

## Visual design contract

Slack messages do not render arbitrary HTML. The module generates Block Kit
JSON with:

- A severity icon and header.
- A colored attachment border: blue information, green success, yellow warning,
  or red error.
- A concise summary plus up to 10 two-column data fields.
- An optional HTTPS thumbnail next to the summary.
- An optional full-width HTTPS image, chart, or status graphic.
- Context metadata in compact text.
- Up to five HTTPS link buttons using `primary` or `danger` styles.
- A top-level fallback string for mobile notifications and screen readers.
- Accessible alternative text for every image.

Images must be available through a public HTTPS URL that Slack can fetch. Do
not use private S3 object URLs without generating a sufficiently-lived presigned
URL. Do not include secrets, tokens, customer private data, or raw stack traces
in messages or images.

## Presentation guidance

- Put the outcome in the title: `Payment failed`, `Worker recovered`, or
  `Certificate expires in 7 days`.
- Keep the summary to one or two sentences.
- Prefer labeled fields for environment, entity, owner, time, and metric values.
- Use a thumbnail for identity and a full image only when the visual adds useful
  evidence, such as a chart or rendered report.
- Include a button only when the target is safe and immediately actionable.
- Send routine events to the application channel. Reserve the general channel
  for cross-application summaries and high-severity incidents.

## Security and operations

- Parameters are `SecureString` values and are never logged or returned to callers.
- Each application workload role must grant `ssm:GetParameter` for only its
  matching path. The separate infrastructure/TLS-monitor identity may read the
  operational destinations it monitors, but must not be used by application code.
- HTTP requests have a 10-second timeout and response bodies are bounded.
- Only `https://hooks.slack.com/services/` webhook values are accepted.
- Image and action URLs must be absolute HTTPS URLs without embedded credentials.
- Rotate a webhook by replacing its SSM parameter; the new value is used after
  the cache expires or the process restarts.
