# Multi-agent platform qualification

This is the release checklist for the generic multi-agent platform. It
separates repeatable local qualification from live GitHub, staging and
production evidence. A local pass is necessary but never grants merge or
release authority.

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
when the command exits. The script needs only the Docker CLI and reads the
image pinned by version and digest from
`deploy/staging/aws-emulator.compose.yml`; it is test infrastructure only and
does not replace S3 or SQS in production.

The live suite creates isolated queues and fake encrypted objects, then proves
both the normal transport and an actual SQS redelivery after a failed terminal
callback. The second delivery must reuse the persisted provider result, bind a
new lease to the original run, emit exactly one accepted terminal effect, make
no second provider call, and delete the message only after success. The older
`ITBEM_LOCALSTACK_E2E` and `ITBEM_LOCALSTACK_ENDPOINT` variables remain accepted
as compatibility aliases; new automation should use
`ITBEM_AWS_EMULATOR_E2E` and `ITBEM_AWS_EMULATOR_ENDPOINT`.

The same live suite also provisions five separate SQS queues and runs one
worker identity for each configured lane: orchestration, engineering, review,
QA and release. Every task is first offered to a different role and must be
rejected before any callback or model call; only the assigned worker may
consume and delete it. Orchestration, engineering and review must persist real
encrypted requests/results, QA must execute its exact-SHA onboarding probe
without a model, and the providerless release worker must fail closed on a
candidate that lacks authoritative release evidence. This proves transport
isolation and deterministic rejection, but it does not replace the complete
single-repository and multi-repository staging workflow below.

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
   `-OIDCJWKSURL`, and the same audience. Use a fresh database name and an
   alternate loopback API port. Pass the isolated Valkey address through
   `-RedisHost`; this path does not read Cognito IDs from the dashboard. When
   the disposable database runs in WSL, pass its exact container name with
   `-DatabaseProbeContainer` and use `-DatabaseProbeInWSL`; the probe remains
   a read-only `SELECT 1`.
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

## Live staging qualification

Use one onboarded test project whose configuration is not embedded in platform
code. Freeze and record every repository branch and SHA before execution.

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
5. Publish branches and PRs only after the human publication grant. A separate
   reviewer must review the final head SHA; any new commit invalidates approval.
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
6. Exercise or prove the configured rollback, roll-forward or expand/contract
   path. Never claim success from a green build alone.

Every item must be represented by immutable control-plane evidence and an
ordered ledger event. Missing, stale, malformed or contradictory evidence is a
block, never a warning that an agent may override.
