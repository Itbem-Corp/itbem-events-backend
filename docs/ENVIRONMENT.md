# Environment Variables

> **IMPORTANT**: Update this file when adding, removing, or changing environment variables in `models/environmentVariables.go`.

## Configuration Loading

Variables are loaded via `configuration/environmentVariables.go` using reflection:
- **Local dev**: reads `.env` if `ENV` is not set in the system environment
- **Production**: uses system environment variables (injected by Docker / GitHub Actions)
- Field names in `models.Config` → `UPPER_SNAKE_CASE` (e.g. `CognitoAwsRegion` → `COGNITO_AWS_REGION`)
- Missing `required:"true"` variables cause `slog.Error + os.Exit(1)` at startup
- AWS SDK variables and profiles are consumed directly by the AWS SDK default
  credential chain; they do not need fields in `models.Config`.

## Quick start

```bash
cp .env.example .env
# fill in the values, then:
go run ./cmd/api
```

## All variables

| Variable | Required | Description |
|----------|----------|-------------|
| `PORT` | no | HTTP port (default `8080`) |
| `ENV` | no | Leave empty locally so `.env` is loaded. Any non-empty value skips `.env` and is intended for runtime-injected production configuration. |
| `AWS_REGION` | **yes** | Default AWS region, including SQS (e.g. `us-east-1`) |
| `S3_REGION` | no | Preferred S3 bucket region when it differs from `AWS_REGION`. For native AWS S3, startup verifies it against the bucket and corrects stale values; if verification is forbidden, an explicit value remains the safe fallback. |
| `S3_ENDPOINT` | no | Custom S3-compatible endpoint for LocalStack/MinIO. Leave empty for AWS. |
| `S3_USE_PATH_STYLE` | no | Use path-style addressing for a custom S3-compatible endpoint. Defaults to `false`. |
| `CDN_BASE_URL` | no | Identity of the legacy CloudFront base URL. The backend uses it to normalize previously stored CDN URLs back to S3 keys before presigning or publishing worker jobs. It does **not** authorize private media and the API does not emit unsigned CDN URLs for protected content. |
| `AWS_PROFILE` | no | Standard AWS SDK profile selector. Useful locally with shared config/SSO. Read directly by the SDK. |
| `AWS_ACCESS_KEY_ID` | no | Standard AWS SDK access key for local development only. Production deploys use GitHub OIDC and must not store this value. |
| `AWS_SECRET_ACCESS_KEY` | no | Standard AWS SDK secret key for local development only. Production deploys use GitHub OIDC and must not store this value. |
| `AWS_SESSION_TOKEN` | no | Standard temporary-session token when the selected credentials require one. Read directly by the SDK. |
| `COGNITO_AWS_REGION` | **yes** | AWS region for Cognito (often same as `AWS_REGION`) |
| `COGNITO_USER_POOL_ID` | **yes** | Cognito User Pool ID (`us-east-1_XXXXXXXXX`) |
| `COGNITO_CLIENT_ID` | no | **Deprecated backend alias** for a static AWS IAM access key. Used only when `COGNITO_CLIENT_SECRET` is also present. It is not the dashboard Cognito App Client ID. |
| `COGNITO_CLIENT_SECRET` | no | **Deprecated backend alias** for a static AWS IAM secret key. Used only as a complete pair with `COGNITO_CLIENT_ID`. |
| `COGNITO_ALLOWED_CLIENT_IDS` | production | Comma-separated Cognito App Client IDs whose signed ID tokens the API accepts. |
| `COGNITO_TENANT_CLIENT_MAP` | production | Audience-to-tenant binding, for example `clientA=eventiapp,clientB=itbem`. |
| `S3_CLIENT_ID` | no | **Deprecated backend alias** for a static AWS IAM access key used by S3/SQS. Used only with `S3_CLIENT_SECRET`. |
| `S3_CLIENT_SECRET` | no | **Deprecated backend alias** for a static AWS IAM secret key used by S3/SQS. Used only as a complete pair with `S3_CLIENT_ID`. |
| `AWS_BUCKET_NAME` | **yes** | S3 bucket name for file storage |
| `DB_HOST` | **yes** | PostgreSQL host |
| `DB_USER` | **yes** | PostgreSQL username |
| `DB_PASSWORD` | **yes** | PostgreSQL password |
| `DB_NAME` | **yes** | PostgreSQL database name |
| `DB_PORT` | **yes** | PostgreSQL port (usually `5432`) |
| `DB_TIMEZONE` | **yes** | DB timezone (e.g. `UTC`) |
| `DB_LOG_LEVEL` | no | GORM SQL logging: `silent`, `error`, `warn`, or `info`. Defaults to `info` locally and `warn` when `ENV` is set, avoiding per-query log overhead in production while retaining slow-query/error diagnostics. |
| `REDIS_HOST` | **yes** | Redis/Valkey host + port (e.g. `localhost:6379`) |
| `REDIS_PASSWORD` | no | Redis password. Leave empty only while the matching cluster has no AUTH; set it atomically with an AUTH migration. |
| `REDIS_DB` | **yes** | Redis database index (usually `0`) |
| `REDIS_TLS` | **yes** | Enable Redis TLS: `true` or `false` |
| `GOOGLE_CLIENT_ID` | **yes** | Google OAuth 2.0 client ID |
| `GOOGLE_CLIENT_SECRET` | **yes** | Google OAuth 2.0 client secret |
| `CORS_ALLOW_ORIGINS` | no | Comma-separated extra CORS origins (local dev only). Production domains are hardcoded. Example: `http://localhost:4321,http://localhost:3000` |
| `SQS_IMAGE_QUEUE_URL` | no | AWS SQS queue URL for async image processing. Leave empty to disable. |
| `SQS_VIDEO_QUEUE_URL` | no | AWS SQS queue URL for async video processing. Leave empty to disable. |
| `SQS_WORKER_QUEUE_URL` | no | AWS SQS queue URL for derived business/data jobs handled by `itbem-events-workers`. Leave empty to keep the live analytics fallback and disable publishing. |
| `EVENT_PREVIEW_SECRET` | deployed | Dedicated HMAC secret for signed dashboard preview URLs. It never falls back to another credential. Production/staging require at least 32 bytes. Generate with `openssl rand -hex 32`. |
| `EVENT_ACCESS_SECRET` | deployed | Dedicated HMAC secret for password-gate access proofs (`X-Event-Access-Token`). It never falls back to another credential. Production/staging require at least 32 bytes. Generate with `openssl rand -hex 32`. |
| `INTERNAL_API_SECRET` | deployed | Dedicated shared secret for Lambda → backend callbacks (`PUT /api/moments/:id/content`). Production/staging require at least 32 bytes. Generate with `openssl rand -hex 32`. |
| `INTERNAL_API_SECRET_PREVIOUS` | no | Temporary second callback secret used only during rotation. Remove it after all old callbacks and retries have drained. |

`EVENT_PREVIEW_SECRET`, `EVENT_ACCESS_SECRET`, `INTERNAL_API_SECRET`, and any
temporary `INTERNAL_API_SECRET_PREVIOUS` value must be generated independently.
The production workflow runs `cmd/security-preflight` before taking a database
snapshot or opening an EC2 deployment session.

## AWS credential resolution

S3, SQS, and the Cognito Admin API use the AWS SDK default credential chain.
This supports standard environment credentials, shared profiles and SSO,
web/workload identity, ECS task roles, and EC2 instance roles without embedding
credentials in application-specific variables.

For backward compatibility, each deprecated pair can still supply static IAM
credentials to its historical client:

- `S3_CLIENT_ID` + `S3_CLIENT_SECRET` for S3 and SQS.
- `COGNITO_CLIENT_ID` + `COGNITO_CLIENT_SECRET` for the Cognito Admin API.

Both members must be non-empty. A missing or partial legacy pair is ignored and
the standard chain remains active. Do not place a Cognito App Client ID or App
Client secret in the backend aliases. Dashboard OAuth/App Client configuration
belongs in `dashboard-ts/.env.local`.

## GitHub Actions secrets mapping

The deploy workflow (`deploy-backend.yml`) maps GitHub Secrets → container env vars:

| Secret name | Env var injected |
|-------------|-----------------|
| `BACKEND_AWS_REGION` | `AWS_REGION` |
| `BACKEND_S3_REGION` | `S3_REGION` (optional; startup verifies the native AWS bucket region and discovers it when omitted) |
| `BACKEND_COGNITO_AWS_REGION` | `COGNITO_AWS_REGION` |
| `BACKEND_COGNITO_USER_POOL_ID` | `COGNITO_USER_POOL_ID` |
| `BACKEND_COGNITO_CLIENT_ID` | `COGNITO_CLIENT_ID` (deprecated legacy IAM alias) |
| `BACKEND_COGNITO_CLIENT_SECRET` | `COGNITO_CLIENT_SECRET` (deprecated legacy IAM alias) |
| `S3_CLIENT_ID` | `S3_CLIENT_ID` (deprecated legacy IAM alias) |
| `S3_CLIENT_SECRET` | `S3_CLIENT_SECRET` (deprecated legacy IAM alias) |
| `AWS_BUCKET_NAME` | `AWS_BUCKET_NAME` |
| `CDN_BASE_URL` | `CDN_BASE_URL` (optional legacy URL normalization) |
| `BACKEND_DB_HOST` | `DB_HOST` |
| `BACKEND_DB_USER` | `DB_USER` |
| `BACKEND_DB_PASSWORD` | `DB_PASSWORD` |
| `BACKEND_DB_NAME` | `DB_NAME` |
| `BACKEND_DB_PORT` | `DB_PORT` |
| `BACKEND_DB_TIMEZONE` | `DB_TIMEZONE` |
| `BACKEND_REDIS_HOST` | `REDIS_HOST` |
| `BACKEND_REDIS_PASSWORD` | `REDIS_PASSWORD` |
| `BACKEND_REDIS_DB` | `REDIS_DB` |
| `BACKEND_REDIS_TLS` | `REDIS_TLS` |
| `BACKEND_GOOGLE_CLIENT_ID` | `GOOGLE_CLIENT_ID` |
| `BACKEND_GOOGLE_CLIENT_SECRET` | `GOOGLE_CLIENT_SECRET` |
| `CORS_ALLOW_ORIGINS` | `CORS_ALLOW_ORIGINS` |
| `INTERNAL_API_SECRET` | `INTERNAL_API_SECRET` |
| `INTERNAL_API_SECRET_PREVIOUS` | `INTERNAL_API_SECRET_PREVIOUS` |
| `EVENT_PREVIEW_SECRET` | `EVENT_PREVIEW_SECRET` |
| `EVENT_ACCESS_SECRET` | `EVENT_ACCESS_SECRET` |
| `SQS_IMAGE_QUEUE_URL` | `SQS_IMAGE_QUEUE_URL` |
| `SQS_VIDEO_QUEUE_URL` | `SQS_VIDEO_QUEUE_URL` |
| `SQS_WORKER_QUEUE_URL` | `SQS_WORKER_QUEUE_URL` |

> `CORS_ALLOW_ORIGINS` is typically empty in production — production only allows the hardcoded domains in `configuration/cors.go`.

`CDN_BASE_URL` must not be treated as a privacy control. A public CloudFront
distribution in front of the entire media bucket can serve a known object key
without consulting event visibility, invitation, password, or access-window
rules. Protected media therefore continues to use short-lived S3 view URLs.
Before unsigned CDN delivery is enabled, CloudFront must enforce viewer
authorization (signed URLs/cookies) or public assets must be isolated in a
separate distribution/prefix that cannot address private objects.

The current deploy workflow still maps the deprecated aliases for compatibility.
New deployment environments should inject standard AWS variables or, preferably,
attach a workload/instance role and omit all four legacy aliases.

### Backend deployment-only settings

These values configure GitHub Actions and are not application environment
variables passed to the container:

| GitHub setting | Type | Required | Purpose |
|---|---|---:|---|
| `EC2_HOST_KEY` | environment secret | yes | Pinned OpenSSH `known_hosts` line for `EC2_HOST`; prevents accepting a substituted SSH host key. |
| `BACKEND_PORT` | environment variable | yes | Validated production container/host port, injected as `PORT`. |
| `BACKEND_CANDIDATE_PORT` | environment variable | no | Loopback-only host port used to health-check the candidate container before promotion. Defaults to `18080` and must differ from `BACKEND_PORT`. |
| `BACKEND_DB_INSTANCE_ID` | environment variable | yes | RDS instance identifier snapshotted before the candidate can run startup migrations; must match the OIDC role stack. |
| `AWS_DEPLOY_ROLE_ARN` | environment variable | yes | GitHub OIDC deployment role output from `infra/github-oidc-backend-role.yml`. Static AWS access-key secrets are intentionally unsupported. |
| `EC2_INSTANCE_ID` | environment variable | yes | Exact EC2 target authorized in the OIDC role stack. |
| `EC2_USER` | environment variable | yes | Exact OS user authorized by the OIDC role stack. |
| `EC2_HOST` | environment variable | yes | SSH target whose pinned key is stored in `EC2_HOST_KEY`. |

Pushes to `main` run validation but never deploy production. Production requires
a manual workflow dispatch with `confirm_production=true` and should use a
required reviewer on the GitHub `production` environment.
Bootstrap, protection rules, least-privilege policy ownership, and emergency
revocation are documented in `docs/GITHUB_OIDC_BOOTSTRAP.md`.

The production workflow builds the validated revision on the GitHub runner,
streams the immutable Docker image over EC2 Instance Connect, starts a candidate
on the loopback-only candidate port, and promotes it only after `/health`
reports `data.status`, `data.db`, and `data.redis` as `ok` and
`data.environment` as `production`.
The previous container remains available until the promoted container
passes its own health gate and is restored automatically on failure. The EC2
host no longer clones the repository or stores `GH_DEPLOY_TOKEN` credentials.
Before transfer, the workflow creates and waits for an RDS snapshot tagged with
the exact revision. Startup DDL runs in one PostgreSQL transaction under an
advisory lock with bounded lock and statement timeouts; failed migrations roll
back atomically and fail the candidate. Successful additive schema changes are
not removed by an application rollback, so restoring the snapshot remains a
separate, explicit disaster-recovery action.

Application secrets are exposed to the runner only through a step-level `env`
mapping. `scripts/deploy/render_docker_env.py` writes a mode-`0600` Docker env
file atomically, rejects missing required values and CR/LF/NUL values, and never
places a secret in shell source or command arguments. The file is streamed over
the verified SSH control connection into the deploy user's mode-`0700`
directory and removed locally and remotely in the unconditional cleanup step.
Its unit test also fails CI if a `${{ secrets.* }}` expression is ever placed
inside a backend workflow `run` block.

## Production TLS renewal

The EC2 nginx virtual hosts for `eventiapp.com.mx` and
`api.eventiapp.com.mx` use the Certbot lineage
`/etc/letsencrypt/live/api.eventiapp.com.mx`. That certificate intentionally
covers the apex and API names; `www.eventiapp.com.mx` terminates TLS at
Cloudflare and is not renewed on the EC2 host.

Keep `/etc/sysconfig/certbot` scoped to the live lineage so obsolete historical
lineages containing `www` cannot make the scheduled renewal fail:

```bash
CERTBOT_ARGS="--cert-name api.eventiapp.com.mx"
```

The timer must remain enabled and active:

```bash
sudo systemctl enable --now certbot-renew.timer
sudo certbot renew --cert-name api.eventiapp.com.mx --dry-run --no-random-sleep-on-renew
sudo nginx -t
systemctl list-timers certbot-renew.timer
```

After renewal, verify from outside the instance with normal certificate
validation and require `GET https://api.eventiapp.com.mx/health` to return 200.

## Accessing config in code

```go
// In HTTP handlers (protected routes — config injected by token middleware):
cfg := c.Get("config").(*models.Config)
bucket := cfg.AwsBucketName

// Outside handlers (pass cfg down from internal/app/app.go):
cfg := configuration.LoadConfig()
```

## Security checklist

- Never commit `.env` (it is in `.gitignore`)
- Prefer AWS profiles/SSO locally and workload or instance roles in deployments.
- Keep Cognito App Client values in the dashboard environment, separate from backend IAM credentials.
- All secrets live in GitHub Secrets → never in code or workflow files
- Rotate AWS credentials if accidentally exposed
- `REDIS_TLS=true` is required for any Redis not on localhost
