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

## Approve

Call
`POST /api/automation/projects/:id/repository-onboardings/:onboardingID/approve`
with the inspected SHA:

```json
{"expected_revision":"0123456789abcdef0123456789abcdef01234567"}
```

Approval is idempotent. In one transaction it creates/updates the approved
repository context, allocates the next repository Vault version and marks the
proposal approved. A stale SHA, blocked proposal, malformed/tampered manifest
or duplicate context fails closed.

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

This foundation performs static inspection only. Command dry-run, branch/PR
write, review, QA, preview, staging, release, health and recovery capabilities
remain unknown/proposed until their dedicated isolated capability probes and
policies are implemented. Onboarding approval does not authorize merge or
deployment.
