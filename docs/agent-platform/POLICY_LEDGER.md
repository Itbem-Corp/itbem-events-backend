# Immutable policy ledger

Delivery policy persistence uses two append-only PostgreSQL ledgers:

- `delivery_policy_revisions` stores one exact, digest-sealed proposal.
- `delivery_policy_decisions` records a later human `approved` or `revoked`
  decision for that exact revision and digest.

A revision is data, not authority. Creating it does not alter effective policy.
The tables have application hooks and PostgreSQL triggers rejecting update or
delete. Corrections append another revision or decision.

## Database safety boundary

The schema rejects unsupported hierarchy levels, malformed scopes, non-object
patch JSON, empty actors, and malformed SHA-256 values. A composite foreign key
binds every decision to both the immutable revision ID and its stored digest;
an approval cannot point at the right row with different content.

Raw proposer and decision actor identifiers are not part of the default JSON
projection. The patch is projected as a JSON object and malformed legacy data
fails closed to `{}`. Future API handlers must return a purpose-built safe
approval projection rather than exposing authentication subjects.

## Effective-selection contract

The subsequent selector/API increment must enforce these rules transactionally:

1. proposals without a decision have no effect;
2. at each exact hierarchy scope, the most recent decided revision is the only
   candidate—revoking it does not silently revive an older, more permissive
   revision;
3. the approver must be an authorized human distinct from the proposer;
4. repository and override scopes must be reconciled against an approved Vault
   repository, and overrides must match the exact change set and expiry;
5. selected rows are reconstructed as `deliverypolicy.Layer` values and passed
   through the deterministic resolver again before use;
6. policy resolution still grants no GitHub, queue, merge, or deployment
   capability. The exact-SHA action adapter remains a separate boundary.

The database schema intentionally precedes mutation endpoints so CI and an
independent reviewer can inspect the immutable evidence boundary in isolation.

## Deterministic row selection

`internal/deliverypolicystore.ResolveEffective` implements the read-side rules
without performing database I/O. Callers provide the authorized revision and
decision rows; the selector verifies strict patch JSON (unknown fields are
rejected), immutable content digests, decision-to-revision binding, decision
timestamps and independent approval before invoking the pure hierarchy
resolver.

Input ordering cannot affect the result. Pending revisions are ignored. The
latest decision at one exact scope wins, including a revocation. For an exact
change set, a repository-specific override takes precedence over a project-wide
override; revoking the repository-specific override blocks fallback to the
broader exception. This prevents a revoke from accidentally restoring older or
broader permissions.
