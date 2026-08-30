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

The backend tries only configured installations, resolves the actual default
branch, pins its full SHA, reads a bounded safe tree and stores a proposal.
Review repository/default branch/SHA, inventory truncation, detected stacks,
proposed commands, every capability state, provenance and the Vault digest.

`unknown` is expected when no dry-run or policy proves a capability. Do not
change it to ready manually.

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
revisions are append-only. Never edit or delete them to conceal drift.

## Current safety boundary

This foundation performs static inspection only. Command dry-run, branch/PR
write, review, QA, preview, staging, release, health and recovery capabilities
remain unknown/proposed until their dedicated isolated capability probes and
policies are implemented. Onboarding approval does not authorize merge or
deployment.
