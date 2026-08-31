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
validated a Cognito ID token, and only for the selected email. It is ignored in
deployed environments. Do not use it as a production role-management path;
use the global administrator module instead.

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
