# Local Development Guide

## Architecture Overview

Three services work together:

| Project | Repo | Port | Start command |
|---------|------|------|---------------|
| **Backend** (Go/Echo) | `itbem-events-backend` | 8080 | `go run server.go` |
| **Dashboard** (Next.js) | `dashboard-ts` | 3000 | `npm run dev` |
| **Public site** (Astro) | `itbem-landing-frontend` | 4321 | `yarn dev` |

Infrastructure (PostgreSQL + Redis) is managed by Docker Compose.

---

## Quick Start

### 1. Start infrastructure (one time)

```bash
# From the backend repo root
docker compose up -d
```

Wait for healthy status:
```bash
docker compose ps
```

### 2. Set up backend

```bash
cp .env.example .env
# Fill in your AWS/Cognito/Google credentials — DB and Redis can use defaults below

# Default values that match docker-compose.yml:
# DB_HOST=localhost  DB_USER=postgres  DB_PASSWORD=postgres  DB_NAME=events_db  DB_PORT=5432
# REDIS_HOST=localhost:6379  REDIS_PASSWORD=  REDIS_DB=0  REDIS_TLS=false

go run server.go
# → http://localhost:8080
```

### 3. Set up dashboard

```bash
cd /path/to/dashboard-ts
cp .env.example .env
# Set NEXT_PUBLIC_BACKEND_URL=http://localhost:8080
# Fill in COGNITO_* credentials

npm install
npm run dev
# → http://localhost:3000
```

### 4. Set up public site

```bash
cd /path/to/itbem-landing-frontend
cp .env.example .env
# PUBLIC_BACKEND_URL=http://localhost:8080  (only needed if Astro makes API calls)

yarn install
yarn dev
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
