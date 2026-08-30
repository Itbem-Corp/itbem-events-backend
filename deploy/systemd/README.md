# Linux role workers

These assets install the reviewed Go `itbem-ai-agent` binary as five separate
systemd services. The installer never enables or starts a service.

## Preconditions

- Merge and deploy the reviewed backend stack through PR #74.
- Deploy the additive queue stack from infrastructure PR #51 and record its
  exact outputs.
- Build the binary from the exact reviewed backend commit and verify its
  SHA-256 before copying it to the Linux host.
- Provision outbound-only network access. No worker needs an inbound port.
- Provide AWS credentials through a root-managed credential source. Each lane
  should receive only its queue, task-input read and task-output write actions.
  Do not reuse the broad credentials of a developer workstation.
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

Install one AWS shared-credentials file per lane under
`/etc/itbem-ai-agent/secrets/<lane>/aws-credentials`, mode `0640`, owned by
`root:itbem-agent-<lane>`. Each IAM principal must be limited to that lane's
queue plus task-input read and task-output write. Release may additionally
resolve the exact configured GitHub App installation for the approved
repository and mint a short-lived token restricted to that repository.
Installations outside the allow-list, PATs and SSH credentials are never
fallback paths; no other lane receives the PEM.

Environment files are root-only. systemd reads them before switching to the
unprivileged per-lane Unix account. Never place their values in a repository,
unit file, command argument or journal message.

## Preflight and activation

Run every doctor while the backend still publishes to the combined queue:

```bash
for lane in orchestration engineering review qa release; do
  sudo systemctl start "itbem-ai-agent@${lane}.service"
  sudo systemctl is-active "itbem-ai-agent@${lane}.service"
  sudo systemctl stop "itbem-ai-agent@${lane}.service"
done
```

Starting a unit runs `--doctor` first. Do not enable the units or configure
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

Orchestration and Review receive the workspace tree read-only through systemd
drop-ins. Engineering, QA and Release may write only under the dedicated state
and workspace roots; filesystem ownership/ACLs must further restrict the
configured repositories. No unit may use a developer's checkout. Base
checkouts are explicitly synchronized to their configured main branch before a
task, and every implementation uses a task-specific worktree.
