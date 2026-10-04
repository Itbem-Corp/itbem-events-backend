# Central workspace catalog

## Behavior

An enrolled machine discovers ready GitHub checkpoints in active projects for
the clients, repository owners and roles granted by the backend policy. The
catalog carries project/source IDs, repository, frozen SHA, base branch and an
execution profile ID. It never carries a local path, executable command or
credential. No inference is involved in discovery or preparation.

The role-specific provisioning service reconciles this catalog on startup and
every 60 seconds, with jitter. It generates private checkout paths from source
UUIDs and uses the dedicated Source App to clone missing repositories. Existing
clean bases are reused without another fetch if the frozen commit is present.
One unavailable repository is excluded from runnable readiness while healthy
repositories remain usable. Its reason is retained in the private cache.

Engineering reports prepared sandbox readiness over a signed, role-token-bound
request. The backend then registers the linked editable context source without
requiring an operator to add workspace paths to the project. Registration locks
projects in stable order, is idempotent, rechecks client scope and checkpoint,
and preserves the checkpoint's approved path restrictions and topology.

New work-item snapshots automatically include only the prepared workspace linked
to a caller-selected source in the same project at the same SHA. Existing task
snapshots are immutable and are not rewritten by catalog reconciliation.

The five-minute cache expires. Authentication/authorization rejection invalidates
it immediately; removed entries cease to resolve after reconciliation. Local
files remain for recovery. An expired/unavailable catalog keeps new Delivery
claims queued before inference. Central transport failures cannot leave startup
stuck behind a failed systemd dependency: the worker doctor performs the
fail-closed check and normal service restart retries continue.

## One-time backend policy

Set `AUTOMATION_WORKSPACE_CATALOG_POLICY_JSON` in the trusted backend environment:

```json
{
  "version": 1,
  "scopes": [{
    "agent_key": "generalist",
    "machine_ids": ["<enrolled-machine-uuid>"],
    "client_ids": ["<authorized-client-uuid>"],
    "roles": ["orchestrator", "principal_engineer", "reviewer", "qa", "release_manager"],
    "repository_owners": ["itbem-corp"],
    "default_profile": "go-v1",
    "allowed_profiles": ["go-v1", "node-v1"],
    "manifest_profiles": {"go.mod": "go-v1", "package.json": "node-v1"}
  }]
}
```

All UUIDs are explicit authority, not wildcards. Client scope is exact; list any
additional authorized child client IDs explicitly. Adding another active project
under an authorized client does not require editing worker configuration. Only
ready, immutable GitHub checkpoints with default-branch metadata are eligible.
The whole catalog is capped at 256 entries; exceeding the cap rejects a partial
catalog rather than treating omitted entries as revoked.

An approved root manifest can select a known profile using a revision-matched
GitHub inventory. Conflicting stack matches use the default profile. An explicit
`workspace_profile` checkpoint metadata value can select only an allowed profile.
Repository content cannot add profiles or change their executable commands.

## One-time worker profiles

Configure each lane's trusted environment with:

- `ITBEM_AI_WORKSPACE_CATALOG_ENABLED=true`
- `ITBEM_AI_WORKSPACE_CATALOG_ROOT=/srv/itbem-agent-workspaces/<lane>/catalog`
- `ITBEM_AI_WORKSPACE_CATALOG_FILE=/var/lib/itbem-ai-agent/<lane>/workspace-catalog.json`
- `ITBEM_AI_WORKSPACE_PROFILES_JSON`: a map of approved profile IDs to
  `WorkspaceConfig` templates, without `path`, `repository_url` or `base_branch`.

Profiles define approved command argv, sandbox image/digest, resources and
capabilities once for a host/toolchain class. Engineering and QA catalog profiles
require sandbox isolation. Orchestration and Review permit only repository read
and source fetch capability; their main service workspace mounts remain read-only.
Engineering cannot gain branch publication or PR creation from a profile.
Release still uses deterministic publication grants and no model credentials.

Static registered workspaces continue to work when the feature is disabled.
Dynamic entries cannot shadow their IDs. A changed host profile invalidates the
cached execution policy until reconciliation publishes the new profile digest.

Install the reviewed CI binary and systemd units using `deploy/systemd/install.sh`.
The worker pulls in its provisioning service and timer on activation. The
installer itself starts no services. Keep Source App PEMs, gateway tokens and
the separate publication App in the existing protected role environment.

## Validation and remaining scope

Tests cover signed role-bound discovery, scoped SQL filtering, real PostgreSQL
registration/idempotency/stale checkpoint/revocation, healthy plus unavailable
repositories, cache expiry, profile changes, selected-source scope and preservation
of repository files on removal. The catalog performs zero model calls.

This delivery implements discovery and reconciliation. CPU/RAM/disk admission,
attempt-scoped workspace leases, automatic evidence retention/garbage collection,
and worker autoscaling remain subsequent lifecycle work. A checkout is not itself
a sandbox, a green catalog is not delivery completion, and no human implementation,
publication, QA, merge or release gate is removed by this feature.
