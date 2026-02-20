# Environment Variables

> **IMPORTANT**: Update this file when adding, removing, or changing environment variables in `models/environmentVariables.go`.

## Configuration Loading

Variables are loaded via `configuration/environmentVariables.go` using reflection:
- **Local dev**: reads `.env` if `ENV` is not set in the system environment
- **Production**: uses system environment variables (injected by Docker / GitHub Actions)
- Field names in `models.Config` → `UPPER_SNAKE_CASE` (e.g. `CognitoAwsRegion` → `COGNITO_AWS_REGION`)
- Missing `required:"true"` variables cause `slog.Error + os.Exit(1)` at startup

## Quick start

```bash
cp .env.example .env
# fill in the values, then:
go run server.go
```

## All variables

| Variable | Required | Description |
|----------|----------|-------------|
| `PORT` | no | HTTP port (default `8080`) |
| `ENV` | no | Any non-empty value skips loading `.env` (set in production) |
| `AWS_REGION` | **yes** | AWS region for S3 (e.g. `us-east-1`) |
| `COGNITO_AWS_REGION` | **yes** | AWS region for Cognito (often same as `AWS_REGION`) |
| `COGNITO_USER_POOL_ID` | **yes** | Cognito User Pool ID (`us-east-1_XXXXXXXXX`) |
| `COGNITO_CLIENT_ID` | **yes** | Cognito App Client ID |
| `COGNITO_CLIENT_SECRET` | **yes** | Cognito App Client Secret |
| `S3_CLIENT_ID` | **yes** | IAM access key ID for S3 uploads |
| `S3_CLIENT_SECRET` | **yes** | IAM secret access key for S3 uploads |
| `AWS_BUCKET_NAME` | **yes** | S3 bucket name for file storage |
| `DB_HOST` | **yes** | PostgreSQL host |
| `DB_USER` | **yes** | PostgreSQL username |
| `DB_PASSWORD` | **yes** | PostgreSQL password |
| `DB_NAME` | **yes** | PostgreSQL database name |
| `DB_PORT` | **yes** | PostgreSQL port (usually `5432`) |
| `DB_TIMEZONE` | **yes** | DB timezone (e.g. `UTC`) |
| `REDIS_HOST` | **yes** | Redis/Valkey host + port (e.g. `localhost:6379`) |
| `REDIS_PASSWORD` | **yes** | Redis password (empty string if none) |
| `REDIS_DB` | **yes** | Redis database index (usually `0`) |
| `REDIS_TLS` | **yes** | Enable Redis TLS: `true` or `false` |
| `GOOGLE_CLIENT_ID` | **yes** | Google OAuth 2.0 client ID |
| `GOOGLE_CLIENT_SECRET` | **yes** | Google OAuth 2.0 client secret |
| `CORS_ALLOW_ORIGINS` | no | Comma-separated extra CORS origins (local dev only). Production domains are hardcoded. Example: `http://localhost:4321,http://localhost:3000` |

## GitHub Actions secrets mapping

The deploy workflow (`deploy-backend.yml`) maps GitHub Secrets → container env vars:

| Secret name | Env var injected |
|-------------|-----------------|
| `BACKEND_PORT` | `PORT` |
| `ENV` | `ENV` |
| `BACKEND_AWS_REGION` | `AWS_REGION` |
| `BACKEND_COGNITO_AWS_REGION` | `COGNITO_AWS_REGION` |
| `BACKEND_COGNITO_USER_POOL_ID` | `COGNITO_USER_POOL_ID` |
| `BACKEND_COGNITO_CLIENT_ID` | `COGNITO_CLIENT_ID` |
| `BACKEND_COGNITO_CLIENT_SECRET` | `COGNITO_CLIENT_SECRET` |
| `S3_CLIENT_ID` | `S3_CLIENT_ID` |
| `S3_CLIENT_SECRET` | `S3_CLIENT_SECRET` |
| `AWS_BUCKET_NAME` | `AWS_BUCKET_NAME` |
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

> `CORS_ALLOW_ORIGINS` is intentionally absent from the deploy workflow — production only allows the hardcoded domains in `configuration/cors.go`.

## Accessing config in code

```go
// In HTTP handlers (protected routes — config injected by token middleware):
cfg := c.Get("config").(*models.Config)
bucket := cfg.AwsBucketName

// Outside handlers (pass cfg down from server.go):
cfg := configuration.LoadConfig()
```

## Security checklist

- Never commit `.env` (it is in `.gitignore`)
- All secrets live in GitHub Secrets → never in code or workflow files
- Rotate AWS credentials if accidentally exposed
- `REDIS_TLS=true` is required for any Redis not on localhost
