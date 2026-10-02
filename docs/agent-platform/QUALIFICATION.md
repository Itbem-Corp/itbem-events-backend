# Multi-agent platform qualification

This is the release checklist for the generic multi-agent platform. It
separates repeatable local qualification from live GitHub, staging and
production evidence. A local pass is necessary but never grants merge or
release authority.

Release observations use the enrolled instance signature together with the
release lane gateway token. The server loads the sealed task input and collects
GitHub and environment evidence; the worker sends only its queue lease and run
ID. Before returning evidence, the server stores its canonical payload digest
bound to the task, run, enrolled instance and exact revision matrix. Completion
must match that server record, including when recovering a prior run. A worker
cannot replace this record by reporting its own successful observations.

For a gateway release observer, narrow `ITBEM_AI_CAPABILITIES` to
`delivery.release_gate`. This profile needs no local GitHub publication key.
General release profiles and profiles including `delivery.publish` retain their
publication credential requirement; the observer exception is gateway-only.

The deterministic worker persists an immutable `request.json` before observing
release state and an immutable `result.json` before its terminal callback.
Recovery preserves both original run references and makes no model call. The
PostgreSQL integration fixtures cover signed admission, revocation during
observation, missing or mutated server evidence, exact recovery references and
the retained Vault blocker. A further fixture connects the actual release
worker, signed HTTP callback, gateway handlers, S3 SDK and PostgreSQL ledger.
It forces the first terminal callback to fail, expires the run lease, then
proves immutable recovery with one server observation, two zero-call attempt
snapshots, no inference receipt or cost execution, and no duplicate events on
terminal redelivery. This proves the release transport chain against synthetic
approved policy/publication/QA evidence; it does not qualify the full five-role
workflow, actual QA tool execution or live production release.

The local qualifier first initializes only the repository's declared Git
submodules at their committed gitlinks. This makes a clean worktree test the
same pinned source graph as CI without naming a product or repository in the
platform. The initial bootstrap can reach the submodule remotes; once those
exact revisions are present, the remaining qualification is network-free.
Local-file submodule transport remains disabled.

## Repeatable local qualification

From the backend repository root on Linux:

```sh
sh scripts/qualify-agent-platform.sh
```

The command is network-free after dependencies are present and validates the
mandatory scenarios against named tests rather than documentation claims:

| Scenario | Executable evidence |
| --- | --- |
| Generic authorized repository onboarding | Deterministic Vault proposal and lifecycle reconciliation from immutable GitHub identity and exact SHA, including path-only API/schema/dependency/test/runbook evidence plus name-only environment declarations whose values never persist |
| Single repository | Isolated clean worktree, bounded patch and immutable reviewed diff |
| Heterogeneous multi-repository change | Go/Node discovery, dependency-first DAG and independent worktrees |
| Monorepo | Module-local Go, Node and Rust command proposals without executing repository prose |
| Default branch other than `main` | Clone and fast-forward of configured `trunk`, preserving dirty-checkout refusal |
| Review-only project | Resolved review policy cannot acquire merge authority or invented test capability |
| Production-capable project | Complete release policy, current human approval and exact environment references |
| Exact multi-repository release | Gatekeeper binds every repository, branch and SHA to QA, security, dependency and recovery evidence |
| Safe restart/redelivery | Persisted result reuse, one cost identity, retry retention and renewable SQS visibility lease |
| Prompt injection | Repository text remains untrusted data and cannot create a command or release capability |
| Linux 24/7 execution | Distinct role identities/lanes, lane-private writable roots, providerless release worker and non-consuming doctor |

The script also executes the full Go regression suite, `go vet` and the
security preflight. Any failure blocks qualification.

### Operator-owned repository scanners

The QA lane must map a real, locally installed scanner command to every
reserved security identity. For the secret floor, this deployment qualifies
the pinned open-source `gitleaks` v8.30.1 binary with the repository-scoped
command below:

```sh
gitleaks dir --redact=100 --no-banner --no-color --timeout 300 .
```

Register that command as `security:secrets`; never map a test identity before
the command passes in every exact reviewed worktree. A known synthetic fixture
may use a same-line `gitleaks:allow` annotation only after a reviewer verifies
that the value is inert and explains the fixture inline. Do not suppress an
entire path, rule or repository, and never persist an unredacted report. The
non-consuming doctor fails closed when any configured validation, QA,
screenshot or semantic-QA executable is unavailable on the host.

## Live AWS transport qualification without production access

Before using a physical worker host or AWS resources, exercise the real AWS SDK
S3/SQS adapters against the pinned, free Moto emulator:

```sh
sh scripts/qualify-agent-platform-live-aws.sh
```

This temporary staging fixture binds only to loopback, has no Docker socket,
drops Linux capabilities, uses a read-only image filesystem, and is removed
when the command exits. The Linux script requires the Docker CLI and GNU
coreutils `timeout` for its bounded engine probe, and reads the image pinned
by version and digest from
`deploy/staging/aws-emulator.compose.yml`; it is test infrastructure only and
does not replace S3 or SQS in production.

The live suite creates isolated queues and fake encrypted objects, then proves
both the normal transport and an actual SQS redelivery after a failed terminal
callback. The second delivery must reuse the persisted provider result, bind a
new lease to the original run, emit exactly one accepted terminal effect, make
no second provider call, and delete the message only after success. The current
Go fixtures use `ITBEM_LOCALSTACK_E2E` and `ITBEM_LOCALSTACK_ENDPOINT`; the script
sets them against Moto. Their historical names do not require LocalStack.
The suite also runs `TestLocalGatewayFiveLaneTransportRoundTrip` in its own Go
process against the real gateway handlers over verified HTTPS. Five separate
queues and role tokens must pass readiness, lease acquisition, exact-task
private input reads, encrypted result writes, actual SQS redelivery and
fresh-client checkpoint recovery under a new sealed lease,
visibility extension and acknowledgement. Probes must leave queued work
unconsumed. Twenty attempts to use another role's token with forged identity
headers, twenty cross-role input reads and twenty cross-role acknowledgements
must return 401; same-role
cross-task reads and input mutation must return 403. Execution clients have no
AWS credentials. The runner validates JSON events for all four root tests and
all five lane subtests in both gateway transport and worker role admission,
refusing missing execution, skips or failures. The worker admission fixture
proves cross-role SQS deliveries remain unclaimed without callback or provider
calls; it does not prove successful execution of engineering operations.

Full five-role engineering qualification remains pending: execute the actual
worker operations, reject wrong-role deliveries before a task callback or model
call, verify lane-specific task results and receipts, perform a model-free QA
probe, and require fail-closed release through exact-SHA human grants. Gateway
transport fixtures do not run inference, modify a repository or publish a PR;
they do not prove the complete single/multi-repository staging workflow below.

`TestFiveRoleWorkersCarryReviewedSourceAndRefuseUngrantableRelease` additionally
executes a cost-free worker chain: an orchestrator response feeds engineering,
engineering fixes an initially failing Go unit test in an isolated worktree,
the reviewer assesses its frozen handoff without changing source, QA executes
the unit test and retains the same source manifest, and release refuses to
publish without a human grant while making no model call. This uses synthetic
provider responses and in-memory callbacks/storage. It does not qualify signed
gateway admission, database transitions, ledger receipts, independent code
review, authorized publication or production execution across the five roles.

Published QA targets additionally require the exact clean Git commit, no
untracked source, and a repository origin, target branch and SHA matching the
frozen Gatekeeper revision matrix. The worker fixture covers both published
and local reviewed targets, rejects new source before inference, and verifies
that the bounded schema-2 observation survives private-result storage,
terminal callback delivery and recovery without another provider call.
The observation records command outcomes independently of model summaries.
These synthetic callback tests do not prove PostgreSQL ledger projection or
source provisioning on an independent QA host; those remain required.

This check uses only disposable `test` credentials. It must never receive a
production AWS profile, provider API key, GitHub token, repository checkout or
secret value.

## Cost-free authenticated local staging

The destructive dashboard flow must never target production. Qualify it with
the API, PostgreSQL, Valkey and Moto bound to loopback plus the disposable
signed identity fixture:

1. Start `deploy/staging/control-plane.compose.yml` with a unique Compose
   project name. Its digest-pinned PostgreSQL, Valkey and Moto services use
   tmpfs only and bind the alternate loopback ports `15432`, `16379` and
   `14568`; `docker compose down` removes the containers and their data.
2. Create a private temporary directory outside every repository.
3. Set `ENV=local` only in the issuer process and start
   `go run ./cmd/itbem-local-oidc --listen 127.0.0.1:<port> --token-file
   <private-file> --ready-file <metadata-file>`. Do not print or source the
   token file.
4. Read the non-secret issuer and JWKS URLs from the metadata file. Start
   `scripts/Start-LocalAIControlPlane.ps1` with `-OIDCIssuerURL`,
   `-OIDCJWKSURL`, the same audience, and
   `-BootstrapRootEmails qa@local.invalid` matching the issuer's allow-listed
   fixture email. Use a fresh database name and an
   alternate loopback API port. Pass the isolated Valkey address through
   `-RedisHost`; this path does not read Cognito IDs from the dashboard. When
   the disposable database runs in WSL, pass its exact container name with
   `-DatabaseProbeContainer` and use `-DatabaseProbeInWSL`; the probe remains
   a read-only `SELECT 1`. Add `-RoleLanes` when qualifying the team topology;
   this creates five distinct input queues plus a shared role DLQ and switches
   the API routing map atomically. Omitting the switch deliberately preserves
   the legacy combined queue for migration compatibility.
5. Read the token into the dashboard test process as `E2E_ID_TOKEN`, set
   `PLAYWRIGHT_BASE_URL` and `E2E_BACKEND_URL` to the isolated loopback
   services, and run the authenticated single-repository and heterogeneous
   multi-repository Playwright projects.
6. Stop the issuer gracefully, stop the isolated services and remove the
   temporary directory. Verify the token and metadata files no longer exist.

The fixture token is process-only secret material: never persist it in `.env`,
Vault, a task, a GitHub secret, an artifact, a screenshot, or logs. Both
dashboard and backend reject non-loopback fixture endpoints, and the backend
rejects the override for any non-local environment. A pass records only the
fixture type, exact code SHA, commands, timestamps and redacted outcomes.

For an explicit local team run, start one worker process per queue with the
matching immutable pair. The launcher rejects partial or crossed pairs before
starting the Go runtime:

```powershell
./scripts/Start-LocalAIAgent.ps1 -Role orchestrator -Lane orchestration
./scripts/Start-LocalAIAgent.ps1 -Role principal_engineer -Lane engineering
./scripts/Start-LocalAIAgent.ps1 -Role reviewer -Lane review
./scripts/Start-LocalAIAgent.ps1 -Role qa -Lane qa
./scripts/Start-LocalAIAgent.ps1 -Role release_manager -Lane release
```

Use separate processes and OS identities for sustained operation. The release
pair is providerless; the launcher must not require or load a model credential
for it. These PowerShell commands are a disposable local qualification path,
not a substitute for the isolated Linux systemd identities.

Each explicit lane owns a separate session-local worker lock. All five role
workers can therefore run concurrently, while a second consumer for the same
lane fails closed. The migration-compatible combined worker has its own lock
and must not consume the same workload alongside explicit role workers.

## Dashboard qualification

Run these commands at the exact dashboard PR SHA:

```sh
npm ci
npm run contract:check
npm run lint
npm run typecheck
npm run test:unit:serial
npm run build
```

The dashboard evidence must cover the resumable Delivery snapshot, ordered
event/timeline projection, execution DAG, worker heartbeats, exact gate reason
codes, Vault and policy diffs, environment reference names, and recovery state.
The UI renders backend state; agent prose cannot synthesize a terminal status.

## Live Docker execution qualification

Run the opted-in integration test as the unprivileged worker account on a host
with Docker available. The pinned toolchain image must be available; the first
pull needs registry access, but repository commands have no network interface.

```sh
ITBEM_DOCKER_SANDBOX_E2E=1 go test -json -race -count=1 \
  -run '^TestDockerSandbox' ./internal/automationagent > docker-sandbox.jsonl
python3 scripts/verify_go_test_evidence.py docker-sandbox.jsonl \
  TestDockerSandboxRoundTrip \
  TestDockerSandboxUserPreservesUnprivilegedOwnership \
  TestDockerSandboxConfigurationIsExplicitAndResourceBounded
```

The round-trip must actually execute a repository Go test, write its isolated
worktree, and verify non-root identity, no effective capabilities,
no-new-privileges, no external network interface, read-only root filesystem,
no Docker socket, and no inherited host-only environment canary. An explicit
qualification request fails if Docker is missing.

On Unix, the container UID matches the unprivileged worker that owns the
worktree; UID or GID zero is never selected. Root or unsupported host identities
use the unprivileged numeric fallback and do not receive permission changes to
the host checkout. `/tmp` remains noexec; Go test binaries execute only from
the separate bounded, container-only `/sandbox-tmp` mount. This Docker check
does not certify Firecracker isolation or the five-role delivery workflow.

## Live staging qualification

Use one onboarded test project whose configuration is not embedded in platform
code. Freeze and record every repository branch and SHA before execution.

Before activating any long-lived role worker, run `--doctor`,
`--runtime-auth-probe` and then `--provider-auth-probe`. The runtime probe
authenticates the exact role/lane token to the backend HTTPS gateway and checks
server-owned queue/storage readiness; it never receives or mutates a task and
requires no AWS identity on the execution host. The provider
probe uses only a read-only provider metadata or
quota endpoint, never creates a completion, and emits no credential, balance or
quota value. A `configured_unverified`, `rejected`, `region_mismatch`,
`unreachable` or `inconclusive` provider state blocks worker activation. For a
MiniMax region mismatch, apply the reported regional completion endpoint to the
host secret configuration and repeat both checks.

1. Approve the proposed Vault and effective policy diff.
2. Execute one single-repository task and one heterogeneous multi-repository
   task through the role lanes. Verify separate Engineer, Reviewer, QA and
   Release identities and exact-SHA handoffs.
3. Restart each systemd worker after it has accepted a safe fixture task.
   Confirm SQS redelivery resumes from immutable evidence without a duplicate
   provider charge, PR, review, merge or deployment.
4. Submit repository documentation containing an explicit instruction to
   bypass tests or reveal credentials. Confirm it is displayed only as
   untrusted evidence and no capability changes.
5. Publish branches and PRs only after the human publication grant. Record the
   GitHub actor that publishes the final reviewable change and require a
   separate reviewer to approve that exact head SHA after publication; any new
   reviewable commit invalidates approval. An empty commit cannot transfer
   last-push responsibility and must never be used to satisfy protected-branch
   reviewer independence.
6. Run configured unit, integration, contract and E2E checks over the exact
   matrix. Confirm one repository failure blocks the whole change set.
7. Exercise the configured staging workflow/environment first. Record exact
   deployed SHA, health, non-destructive smoke/canary and recovery evidence.
8. Verify the dashboard reconnects from snapshot plus ordered event sequence
   without duplicates or impossible state transitions.

No production action is allowed until this staging record passes and the
deterministic Gatekeeper returns `allowed` for the same subject digest.

## Production and recovery qualification

Production remains repository/project policy, not platform convention. The
Release worker may execute or observe only the configured workflow and
environment, with value-free secret/variable reference names. GitHub Actions
uses OIDC and protected environments; the physical Linux host must not hold
long-lived production AWS access keys.

For the exact approved subject:

1. Re-read branch protection, rulesets, checks, decisive reviews, Vault,
   environment references, dependencies and security evidence.
2. Require the configured human approval, plus a separate approval for an
   irreversible recovery classification.
3. Merge without force only if the reviewed and final head SHAs match.
4. Observe the configured deployment workflow and record its resulting SHA.
5. Verify required health checks and non-destructive smoke/canary checks.
   For the review ingress, the production workflow must authenticate as the
   configured GitHub App, redeliver its latest `ping` and record HTTP `200`.
   If the App has no historical `ping`, redeliver its newest previously
   accepted event and require a `2xx` response; ingestion deduplication must
   prove this cannot create duplicate work. A backend-only synthetic signature
   is insufficient because it cannot detect App/environment secret drift.
6. Exercise or prove the configured rollback, roll-forward or expand/contract
   path. Never claim success from a green build alone.

Every item must be represented by immutable control-plane evidence and an
ordered ledger event. Missing, stale, malformed or contradictory evidence is a
block, never a warning that an agent may override.

Sandbox source binding uses the versioned `itbem-sandbox-source-v1` manifest,
not a hash of the host path. It binds each sorted relative filename, executable
bit, byte length and SHA-256 content digest. Git metadata and installed
dependencies (`node_modules`, `.next`, `.venv`, `venv`) are excluded; dependency
qualification remains an independent gate. Credential filenames are rejected
before reading, including reserved `.aws`, `.ssh`, `.local`, `.codex` and
`.config` authority directories, apart from explicit `.env.example`, `.env.sample` and
`.env.template` templates. Symlinks, non-regular files and oversized transfers
fail closed. The guest supervisor verifies the content digest again over the
actual staged snapshot before building its read-only worktree image. This
manifest check does not prove a running VM or qualify an arbitrary guest
toolchain; those require the live lifecycle and engineering workflow evidence.
