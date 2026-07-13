# Local Development Guide

## Architecture Overview

Three services work together:

| Project | Repo | Port | Start command |
|---------|------|------|---------------|
| **Backend** (Go/Echo) | `itbem-events-backend` | 8080 | `go run ./cmd/api` |
| **Dashboard** (Next.js) | `dashboard-ts` | 3000 | `npm run dev` |
| **Public site** (Astro) | `cafetton-casero` | 4321 | `npm run dev` |

Infrastructure (PostgreSQL + Redis) is managed by Docker Compose.

---

## Quick Start

### 1. Start infrastructure (one time)

```bash
# From the backend repo root
docker compose up -d --wait
```

Wait for healthy status:
```bash
docker compose ps
```

### 2. Set up backend

```bash
cp .env.example .env
# Set the Cognito region/user pool and S3 bucket for cloud-backed features.

# Local defaults that match docker-compose.yml:
# DB_HOST=localhost  DB_USER=postgres  DB_PASSWORD=postgres  DB_NAME=events_db  DB_PORT=5432
# REDIS_HOST=localhost:6379  REDIS_PASSWORD=  REDIS_DB=0  REDIS_TLS=false
# AWS authentication uses the standard SDK credential chain (profile, SSO,
# environment credentials, workload identity, or instance/container role).

go run ./cmd/api
# → http://localhost:8080
```

The backend does not need a Cognito App Client ID or secret to validate JWTs.
Those App Client values belong in `dashboard-ts/.env.local`. The backend only
needs `COGNITO_AWS_REGION` and `COGNITO_USER_POOL_ID`, plus AWS IAM credentials
from the standard SDK chain when it performs Cognito Admin API operations.

`COGNITO_CLIENT_ID`/`COGNITO_CLIENT_SECRET` and
`S3_CLIENT_ID`/`S3_CLIENT_SECRET` are deprecated static AWS credential aliases.
Leave them empty in new environments. A legacy pair is used only when both
members are defined; a partial pair falls back to the standard SDK chain.

### 3. Set up dashboard

```bash
cd /path/to/dashboard-ts
cp .env.example .env.local
# Set NEXT_PUBLIC_BACKEND_URL=http://localhost:8080
# Fill in the Cognito User Pool, App Client, hosted UI, and callback values.

npm install
npm run dev
# → http://localhost:3000
```

### 4. Set up public site

```bash
cd /path/to/cafetton-casero
cp .env.example .env
# PUBLIC_EVENTS_URL=http://localhost:8080/

npm install
npm run dev
# → http://localhost:4321
```

---

## Port Reference

| Service | Local URL |
|---------|-----------|
| Backend API | http://localhost:8080 |
| Dashboard | http://localhost:3000 |
| Public site | http://localhost:4321 |
| PostgreSQL | localhost:5432 |
| Redis | localhost:6379 |

### CORS (already configured)

The backend reads `CORS_ALLOW_ORIGINS` from `.env` to allow local dashboard and site origins:

```env
# .env (backend)
CORS_ALLOW_ORIGINS=http://localhost:3000,http://localhost:4321
```

---

## Stopping / Resetting

```bash
# Stop containers (keep data)
docker compose down

# Stop + wipe all data (fresh DB)
docker compose down -v

# View logs
docker compose logs -f postgres
docker compose logs -f redis
```

---

## Production URLs

| Service | URL |
|---------|-----|
| Backend API | https://api.eventiapp.com.mx |
| Dashboard | https://dashboard.eventiapp.com.mx |
| Public site | https://eventiapp.com.mx |
