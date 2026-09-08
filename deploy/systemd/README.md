# Linux role workers

These assets install the reviewed Go `itbem-ai-agent` binary as five separate
systemd services. The installer never enables or starts a service.

## Preconditions

- Deploy only a backend commit with all required checks green and an
  independent review of that exact SHA.
- Confirm the backend owns the configured role-lane SQS queues and private
  input/output buckets. The physical host does not receive those AWS outputs.
- Obtain the `itbem-ai-agent-release-<source-sha>` artifact published by the
  **Publish validated Linux agent release artifact** step in the successful
  `Deploy Backend to EC2` workflow run for the exact independently reviewed
  backend revision.
  It contains the Linux/amd64 binary, `release-manifest.json`, and
  `itbem-ai-agent.sha256`. Confirm the manifest `source_revision` matches the
  reviewed Git SHA and the manifest digest matches the checksum file before
  copying the binary to the Linux host. The installer rejects a binary whose
  digest differs from that approved value; never calculate the expected digest
  from the untrusted local copy.
- Provision outbound-only network access. No worker needs an inbound port.
- Configure five distinct gateway tokens derived by the backend deployment
  from `AUTOMATION_CALLBACK_SECRET`, one for each exact role/lane. The Linux
  host talks only to `ITBEM_API_BASE_URL` over HTTPS: it receives sealed task
  leases while SQS receipt handles, AWS credentials and S3 authority remain in
  the backend. Never install AWS profiles, IAM keys, certificates or the root
  callback secret on this host.
- Use three distinct GitHub App roles. The **Source App** has only Contents
  read and Metadata read and is used to clone/fetch an operator-registered
  GitHub workspace. It cannot approve, create checks, push, merge or deploy.
  Give every lane that registers a GitHub workspace its own private Source App
  key file; these keys may belong to the same read-only App, but never to the
  Reviewer or Release App. Keep its explicit installation allow-list only in
  that lane's secret file.
  Keep the Reviewer App PEM and explicit
  installation allow-list only in the review secret file; it needs metadata
  and contents read plus pull-request and checks read/write solely to publish
  exact-SHA reviews and their required check.
  Keep the Release App PEM and allow-list only in the release secret file; it
  owns approved publication and release operations. Never reuse either App or
  its PEM across the two lanes. Model keys belong only to inference roles;
  Release must not contain one.

## Install without activation

```bash
# Copy this digest from the approved release manifest, not from /tmp.
APPROVED_SHA256=replace-with-approved-release-sha256
sudo deploy/systemd/install.sh "$APPROVED_SHA256" /tmp/itbem-ai-agent
sudoedit /etc/itbem-ai-agent/common.env
sudoedit /etc/itbem-ai-agent/roles/orchestration.env
sudoedit /etc/itbem-ai-agent/roles/engineering.env
sudoedit /etc/itbem-ai-agent/roles/review.env
sudoedit /etc/itbem-ai-agent/roles/qa.env
sudoedit /etc/itbem-ai-agent/roles/release.env
for lane in orchestration engineering review qa release; do
  sudo install -d -m 0710 -o root -g "itbem-agent-${lane}" "/etc/itbem-ai-agent/secrets/${lane}"
  sudo install -m 0640 -o root -g "itbem-agent-${lane}" "/secure/source/bema-source-bot-${lane}.pem" "/etc/itbem-ai-agent/secrets/${lane}/source-github-app.pem"
  sudo stat -c '%a %U %G %n' "/etc/itbem-ai-agent/secrets/${lane}"
done
sudo install -m 0640 -o root -g itbem-agent-review /secure/source/bema-review-bot.pem /etc/itbem-ai-agent/secrets/review/github-app.pem
sudo install -m 0640 -o root -g itbem-agent-release /secure/source/bema-delivery-bot.pem /etc/itbem-ai-agent/secrets/release/github-app.pem
```

Each `stat` line must report mode `710`, owner `root`, the matching
`itbem-agent-<lane>` group, and that lane's secret directory. Stop if any lane
differs; do not compensate by loosening permissions.

The PEM does not encode its GitHub App ID. For every lane, set the matching
`ITBEM_GITHUB_SOURCE_APP_ID`, installation allow-list and
`ITBEM_GITHUB_SOURCE_APP_PRIVATE_KEY_FILE` in that lane's role file, then run
its `--github-auth-probe` during preflight. That bounded GitHub read proves the
lane's copied key belongs to the configured read-only Source App and can access
only an allowed installation; a filename, copied PEM or successful `install`
command is not identity proof.

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

Any lane with a `github.com` workspace refuses to fetch or synchronize it
until its dedicated Source App is configured. It resolves the exact configured
installation and mints a short-lived token restricted to that repository for
each Git command. The registered `origin` remains operator-owned; the token
is supplied only to a temporary askpass process and never becomes part of a
remote URL, command, log, result or Vault. Release separately resolves its
own configured GitHub App installation for approved publication/release
operations. Installations outside an allow-list, PATs, SSH credentials and
credential helpers are never fallback paths; no lane receives a Reviewer or
Release PEM except its owning role.

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
message. It requires no AWS identity. A lane with a registered GitHub workspace
fails doctor until its Source App configuration is present; Review and Release
also fail unless their own publication App identity is complete. Every worker
start then runs `--github-auth-probe`: it mints a short-lived token and makes
one bounded read-only repository-access request for every configured Source
and/or publication App installation that the lane needs. A lane with neither
reports `not_required` without contacting GitHub. Do not enable the worker
units until all five gateway, provider, workspace and GitHub identity
preflights pass. Then start one role unit at a time and observe one canary per
lane before enabling the next.

The service retries a failed preflight every 60 seconds without a systemd
start-limit lockout. This is intentionally safe: all preflight operations are
non-mutating and cannot lease queue work, invoke a model, publish a review or
release a change. Fix a persistent configuration error through its root-only
environment file or use the lane/all kill switch; do not turn a transient DNS
or gateway outage into a permanent stopped worker.

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
developer's checkout or another lane's checkout. For every code-reading,
implementation, and QA task, the worker itself fetches/prunes and safely
fast-forwards an operator-managed base checkout before it proceeds. It rejects
stale, divergent, dirty, or unpinned sources rather than pulling destructively.
Every implementation then uses a task-specific worktree created at the exact
frozen remote SHA, never an ambient local `HEAD`.
