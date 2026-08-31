# Repository onboarding runbook

This flow is generic for any GitHub repository accessible to a configured
installation of the platform GitHub App.

## Prerequisites

- Create the Delivery project and grant the operator project `manage` access.
- Configure the GitHub App ID, private key and explicit installation ID
  allow-list in the backend secret provider.
- Grant repository metadata and contents read access. Onboarding does not need
  source write, merge or deployment permission.

## Inspect

Call `POST /api/automation/projects/:id/repository-onboardings/inspect`:

```json
{"repository_url":"https://github.com/example/service"}
```

For a pull-request or coordinated change, pin the exact 40-character head SHA
instead of following a mutable branch:

```json
{"repository_url":"https://github.com/example/service","revision":"0123456789abcdef0123456789abcdef01234567"}
```

The backend tries only configured installations, resolves the actual default
branch, pins its full SHA, reads a bounded safe tree and stores a proposal.
Review repository/default branch/SHA, inventory truncation, detected stacks,
proposed commands, every capability state, provenance and the Vault digest.

The deterministic manifest records path-only evidence for dependency
manifests, API contracts, data schemas/migrations, CI, ownership,
infrastructure, tests, documentation, runbooks/ADRs and allow-listed
environment declaration templates. Secret-bearing paths and real `.env`
files are excluded. A separate bounded parser may read allow-listed templates
at the exact SHA, but emits only valid variable names and discards every value;
raw template contents never become model context or Vault data. The Vault
stores names, path, exact inspected SHA and confidence.

`unknown` is expected when no dry-run or policy proves a capability. Do not
change it to ready manually.

Capability dry-runs use a separate deterministic projection. Each result must
name an existing capability, match the inspected full SHA, carry a SHA-256
identity for private sandbox evidence and come from the QA, Release or
Orchestrator role. It may set only `ready` or `blocked`; it cannot replace the
source/Vault authority. Stale SHAs, unknown capabilities, duplicate results,
unsealed evidence or an Engineer-authored readiness claim fail closed. A
blocked probe makes the whole onboarding blocked. A canonical subject digest
binds repository, commit, capability, `ready|blocked` verdict, executor role
and evidence digest so the same artifact cannot be replayed with a flipped
result or to prove a different capability/checkpoint.

Accepted probe attestations are stored in an append-only ledger keyed by the
onboarding checkpoint, producing automation task and capability. The control
plane persists only repository/SHA, verdict, executor role, evidence digest,
subject digest and bounded reason; updates and deletes are rejected. Re-running
a probe therefore creates new evidence instead of rewriting the prior result.

### Execute command capability probes

Before approval, register the repository on the Linux execution plane with
the exact GitHub `repository_url`, inspected `base_branch`, and
`repository:read`, `repository:fetch`, and `worktree:create` capabilities.
Give every executable validation/QA command a matching operator-owned kind.
The API accepts only capability names; it never accepts argv or a local path.

Call
`POST /api/automation/projects/:id/repository-onboardings/:onboardingID/probes`:

```json
{
  "expected_revision": "0123456789abcdef0123456789abcdef01234567",
  "workspace_reference": "workspace://service",
  "capabilities": ["unit", "integration", "contract"]
}
```

Supported command probes are `unit`, `integration`, `contract`, `e2e`,
`preview`, `staging`, `health`, and `recovery`. The QA lane fetches origin with
pruning, verifies that the inspected SHA belongs to the configured remote
default branch, creates a detached ephemeral worktree at that exact SHA, and
runs only the matching registry commands with worker credentials removed from
the environment. A missing/non-zero command records `blocked`; exit zero
records `ready`. Changing HEAD or a tracked file fails the task, and the
ephemeral worktree is removed. Command output is redacted and reduced to a
digest before encrypted evidence is stored; the callback contains only sealed
value-free evidence.

Only one probe task may be active for an onboarding proposal digest. A prior
task becomes stale if another probe updates the proposal. Use
`GET /api/automation/projects/:id/repository-onboardings/:onboardingID/probes`
to inspect the append-only history and the safe queued/running/terminal task
projection, then re-read the onboarding before approving its latest
digest/SHA. Both the enqueue response and history expose only this safe task
projection; input/output object references, worker leases and internal errors
are never returned.

## Approve

Call
`POST /api/automation/projects/:id/repository-onboardings/:onboardingID/approve`
with the inspected SHA:

```json
{
  "expected_revision": "0123456789abcdef0123456789abcdef01234567",
  "expected_proposal_sha256": "64-character digest shown by the latest onboarding response"
}
```

Approval is idempotent. In one transaction it creates/updates the approved
repository context, allocates the next repository Vault version and marks the
proposal approved. A stale SHA, blocked proposal, malformed/tampered manifest
or duplicate context fails closed. The proposal digest is mandatory because a
completed capability probe can update readiness without changing the source
SHA; active probes block approval until their evidence is committed.

Use `GET /api/automation/projects/:id/repository-onboardings` and
`GET /api/automation/projects/:id/vault/revisions` for audit history.

## Reconcile

When the repository advances or its API, schema, dependency, infrastructure,
environment declaration, workflow, ownership, tests, decision or runbook
changes, inspect the new SHA and approve another Vault version. Historical
revisions are append-only. Reconciliation compares structured values rather
than prose or agent claims: new facts become `active`, replaced values are
retained as `deprecated` history, absent facts become `removed`, and unchanged
facts preserve their original `valid_from_sha` while advancing
`valid_through_sha`. The proposal exposes an added/modified/unchanged/removed
diff and binds it to both Vault digests. Never edit or delete prior revisions
to conceal drift.

## Current safety boundary

Static inspection plus isolated command dry-runs can prove the eight local
harness capabilities above. Branch/PR write, independent review, release and
other cloud authority remain unknown until their separate GitHub/policy probes
are implemented; they cannot be inferred from a passing repository command.
Onboarding approval does not authorize merge or deployment.
