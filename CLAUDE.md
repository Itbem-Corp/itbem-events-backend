# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Documentation-First Workflow (Token Optimization)

**Always read `docs/` FIRST.** One markdown read (~500 tokens) vs code exploration (5,000–20,000 tokens).

| Looking for… | Read first |
|--------------|-----------| 
| Models, fields, relationships | `docs/MODELS.md` |
| API routes, HTTP methods | `docs/ROUTES.md` |
| Services, business logic | `docs/SERVICES.md` |
| Repositories, data access | `docs/REPOSITORIES.md` |
| Environment variables | `docs/ENVIRONMENT.md` |
| Architecture, patterns, layer structure | `docs/ARCHITECTURE.md` |
| How to add X (entity, route, test) | `docs/COMMON_TASKS.md` |
| All agents and when to use them | `docs/AGENTS.md` |
| Interrupted session | `docs/session-state.md` |
| Sprint state, active tasks | `docs/orchestrator-memory.md` |

**After every code change: update the relevant `docs/` file.**

---

## Context7 MCP — Library Documentation

**Use Context7 MCP whenever you need docs for any library. Never web-search.**

```bash
mcp__context7__resolve-library-id libraryName:"echo labstack"
mcp__context7__get-library-docs libraryId:"/labstack/echo" topic:"middleware" tokens:5000
```

Common IDs: Echo v4 `/labstack/echo` · GORM `/go-gorm/gorm` · AWS SDK Go v2 `/aws/aws-sdk-go-v2` · Redis `/redis/go-redis` · JWT `/golang-jwt/jwt` · UUID `/gofrs/uuid`

Context7 ≈ 1,000 tokens. Web search ≈ 15,000+ tokens.

---

## Project Ecosystem

This project is part of a three-project system. **Every feature or change must be evaluated for cross-project impact.**

| Project | Stack | Local Path | Purpose |
|---------|-------|-----------|---------| 
| **Backend** (this project) | Go + Echo + PostgreSQL + Redis | `\\wsl.localhost\Ubuntu\var\www\itbem-events-backend` | API, business logic, auth (Cognito), S3 uploads, event management |
| **Dashboard** | Next.js 15 + TypeScript | `C:\Users\AndBe\Desktop\Projects\dashboard-ts` | Admin UI: manage events, approve moments, analytics, guest lists, QR codes |
| **Public Frontend** | Astro 5 + React islands | `C:\Users\AndBe\Desktop\Projects\cafetton-casero` | Guest-facing: event pages, photo/video wall, RSVP, QR upload flow |

Never implement a backend feature in isolation. Trace: Backend → Dashboard and/or Public Frontend.

---

## Superpowers Plugin

This project uses the **Claude Code Superpowers plugin**. Before any action, check if a relevant skill applies — invoke it via the `Skill` tool **before doing anything else**. Even a 1% chance of relevance means you must invoke it.

| When... | Use skill |
|---------|-----------|
| Starting a new feature, endpoint, or service | `superpowers:brainstorming` |
| About to write implementation code | `superpowers:test-driven-development` |
| Debugging a bug, test failure, or unexpected behavior | `superpowers:systematic-debugging` |
| Planning a multi-step task | `superpowers:writing-plans` |
| Executing a written plan | `superpowers:executing-plans` |
| 2+ independent tasks that can run in parallel | `superpowers:dispatching-parallel-agents` |
| About to claim work is done or tests pass | `superpowers:verification-before-completion` |
| Completing a feature branch | `superpowers:finishing-a-development-branch` |
| Received code review feedback | `superpowers:receiving-code-review` |
| Completed a task, want a review | `superpowers:requesting-code-review` |

**Rule:** Skills define HOW to approach tasks. Never skip them to save time — they prevent rework.

---

## Definition of Done

A task is **not complete** until ALL of the following pass:

### Every task
- [ ] `go build ./...` — zero compile errors
- [ ] `go test ./... -short` — all unit tests pass
- [ ] Relevant `docs/` file updated (ROUTES.md, MODELS.md, SERVICES.md as needed)

### New endpoint
- [ ] Handler tested: valid request → correct 2xx + `success: true`
- [ ] Handler tested: invalid body → 400 + `success: false`
- [ ] Auth group correct: `public` vs `protected` vs internal
- [ ] Route order: specific paths before parameterized (`/moments/bulk-approve` before `/moments/:id`)
- [ ] Cache invalidation fires after mutations

### Cross-project change
- [ ] `/task release-coordinator` run — backend must deploy and health-check before frontends

### Session interrupted mid-task
Write current state to `docs/session-state.md`. Check it at session start. Clear when done.

---

## Project Overview

Go + Echo + PostgreSQL + Redis events management backend. Event creation, guest management, invitations, multi-tenant client organizations. AWS Cognito auth, AWS S3 uploads, Redis caching.

## Development Commands

```bash
go mod download
go run server.go        # requires .env
go build -o main ./server.go
go test ./... -timeout 60s
go test ./controllers/guests/... -v  # specific package
```

## Architecture

Layer order: **model → repository → service → controller → routes** (see `docs/ARCHITECTURE.md` for full patterns and code examples).

- **Responses:** `utils.Success(c, http.StatusOK, "msg", data)` / `utils.Error(c, http.StatusBadRequest, "msg", err.Error())`
- **Cache:** `redisrepository.Invalidate("resourceType", "all")` after any mutation
- **Auth context:** `c.Get("cognito_sub")`, `c.Get("user_email")`, `c.Get("config")`
- **New entity:** model → `configuration/gorm.go` → repository → service → controller → routes → docs (see `docs/COMMON_TASKS.md`)

## Important Notes

- All UUIDs use `github.com/gofrs/uuid` (not `github.com/google/uuid`)
- **Route case-sensitive** — `GET /api/invitations/ByToken/:token` has capital B and T (Echo v4)
- **Public routes** (no auth): `GET /api/events/page-spec`, `/api/events/section/:sectionId/attendees`, `/api/invitations/ByToken/:token`
- **Health check:** `GET https://api.eventiapp.com.mx/health` → `{ "status": "ok", "db": "connected", "redis": "connected" }`
- **GitHub MCP:** `owner: "Itbem-Corp"`, `repo: "itbem-events-backend"`. Dashboard not configured.
- **Deployment:** push to `main` → GitHub Actions builds Docker image on EC2 (~8–12 min)
