# Documentation Index

Modular docs designed to minimize token usage for Claude Code.

## Files

| File | Purpose | When to Read |
|------|---------|-------------|
| `ARCHITECTURE.md` | Tech stack, layers, request flow | Starting new work |
| `MODELS.md` | All 26 DB models with relationships | Before touching models |
| `ROUTES.md` | All API endpoints (actual routes) | Before adding/changing endpoints |
| `SERVICES.md` | All services by domain | Before adding business logic |
| `REPOSITORIES.md` | All repositories with file paths | Before adding data access |
| `ENVIRONMENT.md` | All env vars, local/prod setup | Config questions |
| `CODE_INDEX.md` | Exact file paths for every file | **Read this FIRST** before Glob/Grep |
| `COMMON_TASKS.md` | Step-by-step task guides | Adding entities, routes, middleware |
| `TEMPLATES.md` | Copy-paste code templates | Writing new code |
| `AGENTS.md` | Agent definitions for delegation | Repetitive tasks |
| `CODING_STANDARDS.md` | Mandatory code quality rules | Code review, new code |
| `PERFORMANCE.md` | DB, cache, and code optimization | Performance issues |
| `CHANGELOG_TEMPLATE.md` | Template for CHANGELOG.md entries | Documenting changes |

## Quick Reference

| Question | File |
|----------|------|
| Where is file X? | `CODE_INDEX.md` |
| What models exist? | `MODELS.md` |
| What endpoints exist? | `ROUTES.md` |
| How do I add a new entity? | `COMMON_TASKS.md` + `TEMPLATES.md` |
| What env vars do I need? | `ENVIRONMENT.md` |
| How is the system designed? | `ARCHITECTURE.md` |
| What service handles X? | `SERVICES.md` |
| How do I query the DB? | `REPOSITORIES.md` |

## Maintenance Rule

**After every code change, update the relevant doc file.**

| Changed | Update |
|---------|--------|
| Model | `MODELS.md` + `CODE_INDEX.md` |
| Route | `ROUTES.md` |
| Service | `SERVICES.md` + `CODE_INDEX.md` |
| Repository | `REPOSITORIES.md` + `CODE_INDEX.md` |
| Env var | `ENVIRONMENT.md` |
| Architecture | `ARCHITECTURE.md` |

## Token Cost Reference

| Action | Token Cost |
|--------|-----------|
| Read one doc file | ~500 tokens |
| Glob/Grep search | ~2,000–5,000 tokens |
| Read multiple code files to understand pattern | ~10,000–20,000 tokens |
| **Savings with docs-first** | **10–40x** |
