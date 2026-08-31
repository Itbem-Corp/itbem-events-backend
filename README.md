# EventiApp Backend

The Go API owns canonical event state, authorization, tenancy, RSVP, guests,
sections, resources and media metadata. Dashboard and public frontend behavior
must consume its documented API rather than duplicating domain rules.

## Local development

The supported workspace path runs the backend in Docker to match the Linux
`libvips` environment:

```powershell
.\eventiapp.ps1 doctor
.\eventiapp.ps1 check -Target backend -Fast
.\eventiapp.ps1 up
```

For direct Go work, use Go 1.25.12 and run:

```bash
go test ./...
go vet ./...
```

Use `./eventiapp.ps1 check -Target services` before a cross-service change:
it validates the API, media processors, and durable workers. Use
`-Target platform` only when the same change also touches CDK infrastructure.

### AI control plane local

For the ITBEM-only delivery flow, start the loopback AWS emulator, Postgres and Valkey first,
then run the isolated control plane. It creates only local S3 buckets and a
local SQS queue, uses `events_ai_local` by default, and reads Cognito *IDs*
from the dashboard's existing `.env.local` without writing a new environment
file:

```powershell
docker compose -f deploy/staging/aws-emulator.compose.yml up -d --wait
.\scripts\Start-LocalAIControlPlane.ps1
```

For authenticated destructive E2E without calling Cognito or production, run
the disposable loopback issuer in a separate process and pass its ready-file
endpoints to the control plane. It creates a short-lived signed ID token in a
private temporary file, never exposes a token endpoint, and removes its files
on graceful shutdown:

```powershell
$fixture = Join-Path ([IO.Path]::GetTempPath()) 'itbem-local-oidc'
New-Item -ItemType Directory -Force -Path $fixture | Out-Null
$env:ENV = 'local'
go run ./cmd/itbem-local-oidc --listen 127.0.0.1:19090 `
  --token-file (Join-Path $fixture 'id-token') `
  --ready-file (Join-Path $fixture 'ready.json')
```

In another terminal, read only the non-secret issuer/JWKS metadata from the
ready file and start `Start-LocalAIControlPlane.ps1` with `-OIDCIssuerURL`,
`-OIDCJWKSURL`, and the fixture audience. This path uses local placeholder
Cognito IDs and does not read the dashboard environment file. Alternate
isolated PostgreSQL and Valkey ports can be selected with `-DatabasePort` and
`-RedisHost`. The dashboard test process reads the token file into
`E2E_ID_TOKEN`; the token must not be copied to `.env`, logs, Vault, CI
artifacts, shell history, or production configuration. See
`docs/agent-platform/QUALIFICATION.md` for the exact lifecycle.

The emulator is free, test-only Moto pinned to an immutable image digest and
binds only to loopback. Production continues to use native AWS S3 and SQS.
`-AwsEmulatorEndpoint` selects another loopback port when needed; the older
`-LocalStackEndpoint` name remains a compatibility alias.

For a brand-new isolated database, the first authenticated platform
administrator can be provisioned without storing an IAM credential or a
password in the repository. Pass their already-known Cognito email explicitly:

```powershell
.\scripts\Start-LocalAIControlPlane.ps1 -DatabaseName events_ai_sandbox -BootstrapRootEmails 'admin@your-company.com'
```

This allow-list is honored only when `ENV=local`, only after the API has
validated a signed Cognito token or the loopback-only qualification token, and
only for the selected email. It is ignored in deployed environments. Do not
use it as a production role-management path; use the global administrator
module instead.

The command deliberately does not configure a model-provider key. Start the
local agent separately with its selected provider credentials in its own
process; never add provider keys to this repository or to dashboard files.

For local development, place the provider configuration in the ignored
`.env.ai.local` file and start the worker with:

```powershell
.\scripts\Start-LocalAIAgent.ps1
```

That local script deliberately gives `.env.ai.local` priority over inherited
Windows User variables, preventing an older credential from being used by
mistake. Server deployments do not invoke this script: they continue to use
their explicitly supplied environment variables. Pass
`-PreferProcessEnvironment` only when a local caller intentionally needs its
process environment to override the file.

## Boundaries

- This repository owns authorization, multi-tenant rules and persisted domain
  state.
- `itbem-product-contract` owns language-neutral product identity and request
  header definitions; this repository keeps a tested local projection.
- Media transformation belongs to `itbem-media-processor`; durable derived
  jobs belong to `itbem-events-workers`.
- Infrastructure topology belongs to `itbem-events-infrastructure`.

See `docs/ARCHITECTURE.md` for implementation details,
`docs/ASYNC_RUNTIME_ROUTING.md` for Go/Rust/Lambda and delivery-agent ownership,
and `../DEVELOPER_WORKFLOW.md` for coordinated validation.
