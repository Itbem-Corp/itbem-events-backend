# Published QA source delivery

For signed HTTP gateway workers, `delivery.qa` acquires each published source
before executing the approved QA matrix. The server resolves the repository,
branch and SHA from its immutable task input. The worker request contains only
the queue lease, live run ID and registered `workspace://` selector.

`POST /api/internal/automation/gateway/qa-source` requires both enrolled-instance
request authentication and the QA lane gateway token. PostgreSQL task authority
and active instance status are checked before external reads and after acquisition.
Replayed signed requests are rejected by the durable nonce table. An expired,
cancelled or mismatched task cannot receive a source response.

Before responding, the server seals an immutable `AutomationQASourceReceipt`
under row locks for the current task and active instance. It records task, run,
workspace, instance, matrix, repository, branch, original SHA, pack digest, byte
count and acquisition timestamp. A repeated identical acquisition is idempotent;
a different package cannot replace the original receipt. This proves server
acquisition provenance, not completion of the worker's QA commands.

New published QA tasks freeze `QASourceReceiptRequired=true` when created.
Their completed callback must cover exactly the source receipts for the same
matrix, workspace branches and enrolled instance. Recovery verifies receipts
against the original run ID. Authorized retries preserve this requirement.
Historical sealed tasks default to false and are not retroactively changed.

The response contains a Git pack and bounded metadata binding task, run, revision
matrix, workspace, repository, branch, original commit SHA and pack SHA-256. The
client compares these fields with the frozen input and registered workspace before
publishing a checkout. Import uses a shallow boundary preserving the original
commit object, including unchanged tree files, without creating a replacement
commit. A dirty, substituted or symlinked existing checkout is rejected.

## Server requirements

- Git, `prlimit`, and the dedicated contents-read GitHub Source App configuration
  (`ITBEM_GITHUB_SOURCE_*`) must be available on the server. There is no publication
  App, PAT, SSH or public-repository fallback. Workers receive no GitHub credential.
- `ITBEM_QA_SOURCE_SCRATCH_ROOT` must name a real private directory owned by the
  runtime UID on a `noexec,nosuid,nodev` tmpfs of at most 256 MiB. Acquisition fails
  closed otherwise. The deployment script supplies `/qa-source` on a 128 MiB
  tmpfs owned by image UID/GID 1000.
- Acquisition commands inherit limits of 512 MiB address space, 30 CPU seconds,
  64 MiB per file, 128 descriptors and no core dumps. Fetch has a 60-second wall
  deadline; the endpoint has a 90-second deadline. These are per-process limits,
  not a proof of aggregate process-tree CPU or memory isolation.
- HTTPS Git transport verifies certificates, refuses redirects and recursively
  fetched submodules, and keeps its repository token out of argv and stored Git
  config. Parent tracing, credential helpers and Git configuration are excluded.

Pack size is at most 64 MiB. A source tree has at most 20,000 files, 128 MiB total
expanded bytes and 32 MiB per file. The importer validates tree entries before
checkout. Symlinks and gitlinks are currently unsupported and fail closed.

The version 2 client supports a bounded `QASB` binary envelope with a canonical
JSON manifest and separately hashed root and child Git packs. Authenticated
metadata binds the entire envelope through `bundle_sha256`; the root digest
must also match `pack_sha256`. Operator-owned workspace configuration may set
`qa_source_dependencies` to a map of relative gitlink paths to GitHub
`owner/repository` names. Empty policy grants no dependency. Every child must
match this policy and the original parent's frozen gitlink and `.gitmodules`
blob before atomic checkout publication. Nested gitlinks remain denied.

The server acquisition function can prepare approved bundles using a separate
repository-scoped token for each child, sharing expanded-tree and combined-pack
budgets. The current HTTP endpoint still serves version 1 single packs: enabling
version 2 also requires operator-owned server policy and immutable dependency
provenance in receipts. Client support alone does not enable backend gitlinks.

## Evidence and remaining qualification

Required tests cover real verified TLS Git transport, cancellation of a pending
fetch, real tmpfs and process limits, original-SHA import, signed client response
substitution, PostgreSQL authority/replay/revocation, and worker execution of a
synthetic Go test followed by recovery without another fetch or synthetic model
call. The PostgreSQL fixture injects a synthetic Git pack supplier; TLS Git
acquisition is independently tested. Worker flow fixtures use a synthetic
provider and callback, and create no production invoice or release authority.

These fixtures do not certify the entire production worker and ledger chain.
Current production Source App readiness, full independent worker callback/ledger
recovery, and pinned submodule source
delivery remain required work. In particular, repositories with gitlinks such as
the backend's `.contracts` are not yet supported by this acquisition path.

Legacy sealed local handoffs retain their original execution behavior. New paid
inference, historical requeues and live worker activation require their existing
explicit authorization; running source qualification does not grant them.
