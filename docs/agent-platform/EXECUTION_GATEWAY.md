# Outbound execution gateway

Status: active for physical Linux workers.

This decision supersedes the earlier physical-host IAM Roles Anywhere and TPM
design. AWS remains the control plane, but only the backend process holds SQS
and S3 authority. Each systemd lane connects outbound to the existing backend
over HTTPS with a distinct derived token.

## Trust boundaries

- `AUTOMATION_CALLBACK_SECRET` is a backend-only root. It is never copied to a
  worker, repository, Vault, task payload or journal.
- A worker receives only the HMAC-derived token for one exact role/lane. The
  backend compares it in constant time and validates the same identity on
  lifecycle callbacks and heartbeats.
- SQS receipt handles are encrypted and authenticated in an expiring AES-GCM
  lease. The plaintext receipt never crosses the backend boundary.
- A lease binds the role, lane, task ID and exact input reference. Reads are
  limited to that input. Writes are limited to the configured output bucket
  and `automation/<task-id>/` namespace.
- ACK, defer and visibility renewal require the same sealed lease and lane
  token. Messages are deleted only after terminal worker processing.
- The backend runtime role, not the Linux host, has receive, visibility,
  acknowledgement and attribute-read authority only on the five agent lane
  queues. Its object authority is restricted to automation inputs and outputs;
  it has no general SQS or S3 wildcard.

## Availability and recovery

The backend continues using SQS visibility, at-least-once delivery, DLQ and
the existing idempotent task/result ledger. A gateway or worker failure leaves
the message for redelivery; it does not acknowledge work optimistically. The
database execution lease intentionally outlives a short provider-network retry
delay. When SQS redelivers during that interval, the callback returns the
remaining lease duration through an authenticated response header; the worker
defers the message instead of acknowledging it. Once the lease expires, exactly
one worker can reclaim the task with a new run ID. Ordinary conflicts for an
already-terminal task remain acknowledgements, so stale duplicate messages do
not loop forever.

The runtime probe checks authentication, a non-consuming attribute read on the
exact lane, and bucket-location access for both private automation buckets. It
does not receive work. This preflight must fail closed when the deployed IAM
role cannot act as the gateway, even if queue URLs and SDK clients are present.
Root-secret rotation accepts current and previous roots for a bounded overlap
while every lane token is replaced.

The explicit `aws` worker transport remains only for the local emulator and a
reviewed migration fallback. Production systemd examples select `gateway` and
contain no queue URL, AWS region/profile, certificate, TPM configuration or
root callback secret.
