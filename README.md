# EventiApp Backend

The EventiApp backend owns domain state, authorization, and the HTTP API for
the EventiApp product family. It is implemented in Go with Echo, PostgreSQL,
Valkey/Redis, AWS Cognito, S3, SNS, and SQS.

## Architecture

HTTP controllers adapt requests to services; services hold use cases and
depend on ports; repositories and AWS clients are adapters. Controllers must
not access repositories directly, services must not depend on HTTP framework
types, and internal Lambda packages must not depend on executable commands.
The product contract is pinned as `.contracts/itbem-product-contract`; it is
validated in CI and is never fetched at request time.

Read the detailed architecture and ownership map before changing a layer:

- [Architecture](docs/ARCHITECTURE.md)
- [Authorization model](docs/AUTHORIZATION_MODEL.md)
- [Product boundaries](docs/PRODUCT_BOUNDARIES.md)
- [Routes](docs/ROUTES.md)
- [Operations and observability](docs/OPERATIONS.md)

## Local development

Requirements: Go `1.25.12`, Docker Desktop, and a copy of `.env.example` as
`.env`. The environment template contains only placeholders; do not commit
credentials.

```powershell
git submodule update --init --recursive
docker compose up -d
go run ./cmd/api
```

`docker compose` starts PostgreSQL and Valkey only. The API process remains
local so its logs and debugger stay attached to the developer session. See
[local development](docs/LOCAL_DEV.md) and [environment variables](docs/ENVIRONMENT.md)
for complete setup and AWS-local emulator options.

## Verification

```powershell
go test ./utils/... ./internal/authz/... ./internal/products/... ./internal/requestcontext/...
go test ./... -timeout 180s
go vet ./...
```

CI also runs `go test -race ./...` on Linux to detect unsynchronized access.
The race detector needs CGO, so use a local Go installation with a C compiler
when reproducing that gate outside CI.

For a coordinated cross-repository change, run the relevant commands from
`eventiapp-platform`; service ownership and release governance remain in this
repository's CI.

## Security and releases

Review [SECURITY.md](SECURITY.md) for reporting guidance. Production releases
use immutable artifacts, GitHub OIDC, and protected environments; infrastructure
changes are owned by `itbem-events-infrastructure`, not by this repository.
