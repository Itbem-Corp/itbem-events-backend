# Linux role workers

These assets install the reviewed Go `itbem-ai-agent` binary as five separate
systemd services. The installer never enables or starts a service.

## Preconditions

- Deploy only a backend commit with all required checks green and an
  independent review of that exact SHA.
- Confirm the backend owns the configured role-lane SQS queues and private
  input/output buckets. The physical host does not receive those AWS outputs.
- Build the binary from the exact reviewed backend commit and verify its
  SHA-256 before copying it to the Linux host.
- Provision outbound-only network access. No worker needs an inbound port.
- Configure five distinct gateway tokens derived by the backend deployment
  from `AUTOMATION_CALLBACK_SECRET`, one for each exact role/lane. The Linux
  host talks only to `ITBEM_API_BASE_URL` over HTTPS: it receives sealed task
  leases while SQS receipt handles, AWS credentials and S3 authority remain in
  the backend. Never install AWS profiles, IAM keys, certificates or the root
  callback secret on this host.
- Use two distinct GitHub Apps. Keep the Reviewer App PEM and explicit
  installation allow-list only in the review secret file; it needs metadata
  and contents read plus pull-request read/write solely to publish reviews.
  Keep the Release App PEM and allow-list only in the release secret file; it
  owns approved publication and release operations. Never reuse either App or
  its PEM across the two lanes. Model keys belong only to inference roles;
  Release must not contain one.

## Install without activation

```bash
sudo deploy/systemd/install.sh /tmp/itbem-ai-agent
sudoedit /etc/itbem-ai-agent/common.env
sudoedit /etc/itbem-ai-agent/roles/orchestration.env
sudoedit /etc/itbem-ai-agent/roles/engineering.env
sudoedit /etc/itbem-ai-agent/roles/review.env
sudoedit /etc/itbem-ai-agent/roles/qa.env
sudoedit /etc/itbem-ai-agent/roles/release.env
sudo install -m 0640 -o root -g itbem-agent-review /secure/source/bema-review-bot.pem /etc/itbem-ai-agent/secrets/review/github-app.pem
sudo install -m 0640 -o root -g itbem-agent-release /secure/source/bema-delivery-bot.pem /etc/itbem-ai-agent/secrets/release/github-app.pem
```

Each role file owns its own `ITBEM_AI_WORKSPACES_JSON`. Register only managed
checkouts below `/srv/itbem-agent-workspaces/<lane>` for that exact lane. The
installer makes the common root non-listable/non-writable (`0711`) and each
lane root private to its Unix account (`0700`); do not weaken those modes or
reuse a checkout across Engineer, Reviewer, QA and Release. This provides the
independent checkout boundary required by review and exact-SHA evidence.
When tests need pinned content that intentionally lives outside Git, declare
its repository-relative directory in the workspace's
`read_only_fixture_paths`. The worker copies only that operator-approved,
bounded, non-secret content into isolated worktrees and rejects links, Git
metadata, credentials and missing fixtures. Never use this setting for keys or
runtime secrets.
On upgrades from the earlier shared root, move or freshly clone each managed
base into its lane root and move the registry from `common.env` into every role
file before preflight. The tightened unit intentionally makes an old shared
path unreadable/unwritable so migration mistakes fail closed.

Each `ITBEM_AI_GATEWAY_TOKEN` is HMAC-SHA256 over
`itbem-agent-gateway:v1:<role>:<lane>` using the backend root callback secret,
encoded as unpadded base64url. Derive it inside the protected backend secret
boundary and install only the resulting lane token in that lane's mode-0600
environment file. A token is accepted only with its matching role/lane
headers. Leases are AES-GCM sealed, expire, bind the exact input reference and
restrict writes to `automation/<task-id>/`; receipt handles never cross in
plaintext. Rotate the root with the existing current/previous overlap, then
replace every lane token and remove the previous root after all workers have
restarted.

Release may additionally
resolve the exact configured GitHub App installation for the approved
repository and mint a short-lived token restricted to that repository.
Installations outside the allow-list, PATs and SSH credentials are never
fallback paths; no other lane receives the PEM.

Environment files are root-only. systemd reads them before switching to the
unprivileged per-lane Unix account. Never place their values in a repository,
unit file, command argument or journal message.

## Preflight and activation

Run every isolated oneshot doctor while the backend still publishes to the
combined queue:

```bash
for lane in orchestration engineering review qa release; do
  sudo systemctl start "itbem-ai-agent-doctor@${lane}.service"
  sudo journalctl -u "itbem-ai-agent-doctor@${lane}.service" -n 20 --no-pager
done
```

The doctor unit runs only the local, non-billable `--doctor` command and cannot
lease work or mutate a workspace. Before every worker start, the service then
runs `--runtime-auth-probe`, which authenticates to the backend gateway and
verifies queue/storage readiness without receiving, deleting or changing a
message. It requires no AWS identity. Review and Release each fail unless their own
GitHub App identity is complete; other lanes remain locally useful without
publication authority. Every worker start also runs `--github-auth-probe`:
Review and Release must mint a short-lived installation token and complete one
bounded read-only repository-access request, while non-publishing lanes report
`not_required` without contacting GitHub. Do not enable the worker units until all five gateway,
provider, workspace and GitHub identity preflights pass. Then start one role
unit at a time and observe one canary per lane before enabling the next.

## Kill switch and recovery

Create `/etc/itbem-ai-agent/disabled/all` to prevent every subsequent start, or
`/etc/itbem-ai-agent/disabled/<lane>` for one lane, then stop the affected
units. Removing the file permits a later operator-controlled start; it does not
start anything automatically.

Never auto-replay a DLQ. Reconcile the database task, immutable input SHA,
attempt history and external side effects first. Release recovery additionally
requires the exact reviewed branch/SHA, valid short-lived publication grant,
green required checks and the deterministic Gatekeeper decision.

## Workspace isolation

Orchestration and Review receive only their own lane workspace tree read-only
through systemd drop-ins. Engineering, QA and Release may write only under
their distinct private state and lane workspace roots. No unit may use a
developer's checkout or another lane's checkout. Base
checkouts are explicitly synchronized to their configured main branch before a
task, and every implementation uses a task-specific worktree.
