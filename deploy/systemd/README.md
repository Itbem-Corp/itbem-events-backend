# Linux role workers

These assets install the reviewed Go `itbem-ai-agent` binary as five separate
systemd services. The installer never enables or starts a service.

## Preconditions

- Deploy only a backend commit with all required checks green and an
  independent review of that exact SHA.
- Deploy the additive queue stack from infrastructure PR #51 and record its
  exact outputs.
- Build the binary from the exact reviewed backend commit and verify its
  SHA-256 before copying it to the Linux host.
- Provision outbound-only network access. No worker needs an inbound port.
- On a host outside AWS, configure IAM Roles Anywhere independently per lane
  with the reviewed `aws_signing_helper` and a self-managed CA. Its
  `credential_process` returns renewable temporary sessions to the Go SDK;
  never install IAM access keys. AWS Roles Anywhere has no additional service
  charge, while AWS Private CA does, so this runbook does not require Private
  CA. An EC2 deployment must use per-lane instance/task roles instead.
- Keep the GitHub App PEM and explicit comma-separated installation allow-list
  only in the release secret file. Model keys belong only to inference roles;
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
sudo install -m 0640 -o root -g itbem-agent-release /secure/source/bema-review-bot.pem /etc/itbem-ai-agent/secrets/release/github-app.pem
```

Each role file owns its own `ITBEM_AI_WORKSPACES_JSON`. Register only managed
checkouts below `/srv/itbem-agent-workspaces/<lane>` for that exact lane. The
installer makes the common root non-listable/non-writable (`0711`) and each
lane root private to its Unix account (`0700`); do not weaken those modes or
reuse a checkout across Engineer, Reviewer, QA and Release. This provides the
independent checkout boundary required by review and exact-SHA evidence.
On upgrades from the earlier shared root, move or freshly clone each managed
base into its lane root and move the registry from `common.env` into every role
file before preflight. The tightened unit intentionally makes an old shared
path unreadable/unwritable so migration mistakes fail closed.

Install one AWS shared config per lane under
`/etc/itbem-ai-agent/secrets/<lane>/aws-config`, mode `0640`, owned by
`root:itbem-agent-<lane>`, based on `roles-anywhere-aws-config.example`.
Install that lane's X.509 certificate and private key beside it, or use the
helper's PKCS#11/TPM options so the key is non-exportable. Verify the helper's
published SHA-256 before installation. The common environment disables EC2
metadata and points the long-term shared-credentials path at `/dev/null`, so a
missing/expired certificate fails closed instead of falling back to a machine
or developer identity. See the official
[credential helper guide](https://docs.aws.amazon.com/rolesanywhere/latest/userguide/credential-helper.html).
The infrastructure profile intentionally rejects a caller-supplied role
session name, so the helper configuration must not include
`--role-session-name`. Copy each exact queue URL, input bucket, output bucket,
profile ARN, role ARN and trust-anchor ARN from the reviewed stack outputs.
Every checked-in `REPLACE_WITH_*_STACK_OUTPUT` value is deliberately invalid;
the worker doctor must fail until an operator replaces all of them.

Each temporary IAM role must be limited to that lane's queue plus task-input
read and task-output write. Release may additionally
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
poll SQS or mutate a workspace. The Release doctor additionally fails unless
its GitHub App identity is complete; other lanes may remain locally useful
without publication authority. Do not enable the worker units or configure
backend lane routing until all five preflights, queue IAM checks and heartbeat
identities are verified. Then start the role units, switch the backend with the
complete lane map in one deployment, and observe one canary per lane. The
combined worker remains running only long enough to drain its retained queue.

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
