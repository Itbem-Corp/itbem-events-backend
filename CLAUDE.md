# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 🚨 CRITICAL: Documentation-First Workflow (Token Optimization)

**MANDATORY WORKFLOW - YOU MUST FOLLOW THIS EVERY TIME:**

### Before ANY Task:
1. **ALWAYS read `docs/` files FIRST** before exploring code
2. Consult the relevant documentation based on your task:
   - Need to understand architecture? → Read `docs/ARCHITECTURE.md`
   - Looking for a model? → Read `docs/MODELS.md`
   - Looking for a route/endpoint? → Read `docs/ROUTES.md`
   - Looking for a service? → Read `docs/SERVICES.md`
   - Looking for a repository? → Read `docs/REPOSITORIES.md`
   - Need to add a new entity/feature? → Read `docs/COMMON_TASKS.md`
   - Need environment variables? → Read `docs/ENVIRONMENT.md`
3. **Only explore actual code files if the documentation doesn't answer your question**
4. If documentation is outdated or missing information, note it and update after completing the task

### After EVERY Code Change:
1. **MANDATORY**: Check if any `docs/` files need updating
2. Update the relevant documentation files to reflect your changes:
   - Added a model? → Update `docs/MODELS.md`
   - Added a route? → Update `docs/ROUTES.md`
   - Added a service? → Update `docs/SERVICES.md`
   - Added a repository? → Update `docs/REPOSITORIES.md`
   - Added environment variable? → Update `docs/ENVIRONMENT.md`
   - Changed architecture? → Update `docs/ARCHITECTURE.md`
   - Discovered a new pattern? → Update `docs/COMMON_TASKS.md`
3. **NEVER skip documentation updates** - this is critical for token efficiency

### Why This Matters:
- Reading a 2KB markdown file costs ~500 tokens
- Exploring code with Glob/Grep/Read can cost 5000-20000 tokens
- **Following docs-first = 10-40x token savings**
- Updated docs = faster development for future tasks

### Example Workflow:
```
❌ BAD: "Let me search for the Event model in the codebase..."
   → Uses Glob, Grep, Read multiple files = 10,000+ tokens

✅ GOOD: "Let me check docs/MODELS.md first..."
   → Read one markdown file = 500 tokens
   → Find exact file path immediately
   → Only read actual code if needed
```

**REMEMBER: Docs first, code second. Update docs always.**

## 🔌 Context7 MCP — Always Use for Library Documentation

**MANDATORY: Use Context7 MCP whenever you need documentation for any library or framework.**

Context7 provides up-to-date, version-specific docs directly in your context window — no web searches needed.

### When to use Context7
- Need to look up a Go library (Echo, GORM, uuid, aws-sdk-go, etc.)
- Need to check function signatures, middleware patterns, or breaking changes
- Any uncertainty about a third-party package's API

### How to use
```
# 1. Resolve the library ID
mcp__context7__resolve-library-id libraryName:"echo labstack"

# 2. Fetch relevant docs
mcp__context7__get-library-docs libraryId:"/labstack/echo" topic:"middleware" tokens:5000
```

**Common library IDs for this project:**
- Echo v4: `/labstack/echo`
- GORM: `/go-gorm/gorm`
- AWS SDK Go v2: `/aws/aws-sdk-go-v2`
- Redis (go-redis): `/redis/go-redis`
- JWT (golang-jwt): `/golang-jwt/jwt`
- UUID (gofrs): `/gofrs/uuid`

**Why this matters:** Reading Context7 docs costs ~1,000 tokens vs 15,000+ for web searches or exploring source code. Always resolve+fetch before writing code that uses an unfamiliar API.

---

## 🤖 Use Specialized Agents (Advanced Token Optimization)

This project has **12 custom Claude Code agents** for common tasks. Using agents can save 10,000-25,000 tokens per task!

### Quick Agent Reference

| Task | Use This Agent | Token Savings |
|------|---------------|---------------|
| Generate full CRUD entity | `scaffold-generator` | ~25,000 tokens |
| Security audit | `security-auditor` | ~20,000 tokens |
| Write tests | `test-writer` | ~15,000 tokens |
| Optimize queries | `performance-optimizer` | ~15,000 tokens |
| Explore models | `model-explorer` | ~10,000 tokens |
| Map routes | `route-mapper` | ~8,000 tokens |

**Full agent documentation**: See `docs/AGENTS.md`

### Example Usage
```bash
# Instead of manually creating CRUD:
/task scaffold-generator "Create full CRUD for Notification entity"

# Instead of manually exploring:
/task model-explorer "Document all models with ClientID"

# Run multiple agents in parallel:
/task security-auditor "Audit recent changes" &
/task doc-updater "Update all documentation"
```

## 📋 Use Code Templates (10x Faster Development)

Pre-built templates for all common patterns. See `docs/TEMPLATES.md`.

**Quick start**: Copy template → Replace placeholders → Done!

- Model template (with UUID, timestamps, relationships)
- Repository template (with CRUD operations)
- Service template (with validation, cache invalidation)
- Controller template (with error handling)
- Routes registration template

**Time savings**: 30-45 min → 5-10 min per entity (75% faster)

## 📁 Use Code Index (Instant File Lookup)

**ALWAYS check `docs/CODE_INDEX.md` BEFORE using Glob/Grep!**

The code index has exact file paths for:
- All models → `models/*.go`
- All controllers → `controllers/<domain>/<domain>.go`
- All services → `services/<domain>/<Domain>Service.go`
- All repositories → `repositories/<domain>repository/<Domain>Repository.go`

**Token savings**: 5,000 tokens per file lookup!

## Project Overview

Go-based events management backend built with Echo framework. Handles event creation, guest management, invitations, and multi-tenant client organizations. Uses PostgreSQL for persistence, Redis for caching, AWS Cognito for authentication, and AWS S3 for file storage.

## Development Commands

### Local Development
```bash
# Install dependencies
go mod download

# Run the server (requires .env file)
go run server.go

# Build binary
go build -o main ./server.go

# Build with optimizations (production-like)
go build -v -trimpath -ldflags="-s -w" -o main ./server.go
```

### Docker
```bash
# Build image
docker build -t itbem-events-backend .

# Run container (requires all env vars from deploy workflow)
docker run -d --name itbem-events-backend \
  -p 8080:8080 \
  -e PORT=8080 \
  -e DB_HOST=localhost \
  # ... (see .github/workflows/deploy-backend.yml for full env list)
  itbem-events-backend
```

### Testing
```bash
# Run all tests
go test ./... -timeout 60s

# Run a specific package
go test ./controllers/guests/... -v
```

Tests use Go's standard `testing` package + `testify/assert`. Test files use the `_test.go` suffix.
Controllers that need testing use a **testable handler pattern** (see "Testable Handler Pattern" in Common Development Tasks).

## Architecture

### Layer Structure
The codebase follows a clean three-layer architecture:

1. **Controllers** (`controllers/`) - HTTP handlers that parse requests and return responses
   - Bind request data via `c.Bind()`
   - Call service layer functions
   - Return standardized responses using `utils.Success()` or `utils.Error()`
   - Example: `controllers/events/events.go`

2. **Services** (`services/`) - Business logic and orchestration
   - Coordinate between repositories
   - Handle cache invalidation
   - Implement complex business rules
   - Example: `services/events/EventService.go`

3. **Repositories** (`repositories/`) - Data access layer
   - Direct database operations via GORM
   - Generic CRUD via `gormrepository` package
   - Redis cache management
   - Example: `repositories/eventsrepository/EventsRepository.go`

### Key Patterns

**Standard Response Format:**
```go
// Success
return utils.Success(c, http.StatusOK, "Message", dataObject)

// Error
return utils.Error(c, http.StatusBadRequest, "Message", err.Error())
```

**Cache Invalidation:**
Services invalidate Redis cache after mutations:
```go
func CreateEvent(obj *models.Event) error {
    if err := eventsrepository.CreateEvent(obj); err != nil {
        return err
    }
    return redisrepository.Invalidate("events", "all")
}
```

**Controller Dependency Injection (`InitXxxController`):**
Controllers that hold references to services use package-level init functions:
```go
var guestSvc *guestService.GuestService
func InitGuestsController(svc *guestService.GuestService) { guestSvc = svc }
```
Called in `server.go` after wiring repos → services.

**GuestRepo Struct Pattern (interface implementation):**
When a controller/service needs DI via an interface, wrap package-level functions in a struct:
```go
type GuestRepo struct{}
func NewGuestRepo() *GuestRepo { return &GuestRepo{} }
func (r *GuestRepo) CreateGuest(m *models.Guest) error { return CreateGuest(m) }
// ... all interface methods delegate to package-level functions
```

**Testable Handler Pattern (for unit-testing Echo handlers):**
Use a `deps` struct with function pointers so tests can inject mock implementations:
```go
type attendeeDeps struct {
    getSection   func(id uuid.UUID) (*models.EventSection, error)
    getAttendees func(eventID uuid.UUID) ([]models.Guest, error)
}
// Testable core (unexported):
func handleGetAttendees(deps attendeeDeps, c echo.Context) error { ... }
// Public handler wires real repos:
func GetAttendees(c echo.Context) error {
    return handleGetAttendees(attendeeDeps{
        getSection:   eventsectionrepository.GetEventSectionByID,
        getAttendees: guestrepository.ListAttendeesByEventID,
    }, c)
}
```
Tests call `handleGetAttendees(mockDeps, c)` directly. See `controllers/guests/guests_test.go`.

**Authentication Context:**
Protected routes inject authenticated user info into Echo context:
```go
cognitoSub := c.Get("cognito_sub").(string)  // AWS Cognito user ID
userEmail := c.Get("user_email").(string)    // User's email
cfg := c.Get("config").(*models.Config)      // App configuration
```

### Module Organization

Each domain (events, guests, invitations, clients, users, etc.) has:
- `controllers/<domain>/` - HTTP handlers
- `services/<domain>/` - Business logic
- `repositories/<domain>repository/` - Data access
- `models/<Domain>.go` - GORM model

Example for events:
```
controllers/events/events.go          → HTTP handlers
services/events/EventService.go       → Business logic
repositories/eventsrepository/EventsRepository.go → Data access
models/Event.go                       → Data model
```

### Middleware

**Token Authentication** (`middleware/token/token_middleware.go`):
- Validates AWS Cognito JWT tokens
- Extracts user identity from token claims
- Injects `cognito_sub`, `user_email`, `config` into context
- Applied to protected routes in `routes/routes.go`

**Redis Cache Retrieval** (`middleware/redis/redis_middleware.go`):
- Automatically loads cached data based on URL parameters
- Stores data in Echo context for controller access
- Applied to both public and protected route groups

### Database Migrations

**Auto-migration on startup** (`configuration/gorm.go`):
- All models are auto-migrated when the server starts
- Models are defined in `configuration/gorm.go` (see `modelsWithoutSeed` and `modelSeedList`)
- To add a new model: add it to the appropriate slice in `configuration/gorm.go`

**Seed data** (`seeds/`):
- Catalog/reference data is seeded automatically on startup
- Only runs if tables are empty
- Add new seeds to `modelSeedList` in `configuration/gorm.go`

### Configuration

Environment variables are loaded via `configuration/environmentVariables.go`:
- Reads from `.env` file in local dev (if ENV is not set)
- Uses system environment variables in production
- Field names in `models.Config` are automatically converted to UPPER_SNAKE_CASE
- Required variables cause fatal error if missing

### Multi-Tenant Client Hierarchy

The system supports hierarchical client organizations:
- `Client` model has `ParentClientID` for nesting
- Users belong to clients via `ClientMember` junction table
- Roles (`ClientRole`) define permissions within a client
- Routes enforce client-scoped access control

### File Uploads

Files are uploaded to AWS S3:
- Upload logic in `repositories/awsrepository/`
- Resources tracked in `Resource` model
- Image processing via `libvips` (see Dockerfile dependencies)
- S3 credentials configured via environment variables

## Common Development Tasks

### Adding a New Entity

1. **Create model** in `models/<Entity>.go`:
   ```go
   type Entity struct {
       ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
       Name      string    `gorm:"type:varchar(255);not null"`
       CreatedAt time.Time
       UpdatedAt time.Time
   }
   ```

2. **Register for migration** in `configuration/gorm.go`:
   ```go
   var modelsWithoutSeed = []interface{}{
       // ...
       &models.Entity{},
   }
   ```

3. **Create repository** in `repositories/entityrepository/EntityRepository.go`:
   ```go
   func CreateEntity(entity *models.Entity) error {
       return gormrepository.Insert(entity)
   }
   ```

4. **Create service** in `services/entity/EntityService.go`:
   ```go
   func CreateEntity(obj *models.Entity) error {
       if err := entityrepository.CreateEntity(obj); err != nil {
           return err
       }
       return redisrepository.Invalidate("entity", "all")
   }
   ```

5. **Create controller** in `controllers/entity/entity.go`:
   ```go
   func CreateEntity(c echo.Context) error {
       var entity models.Entity
       if err := c.Bind(&entity); err != nil {
           return utils.Error(c, http.StatusBadRequest, "Invalid request", err.Error())
       }
       if err := entityService.CreateEntity(&entity); err != nil {
           return utils.Error(c, http.StatusInternalServerError, "Error creating", err.Error())
       }
       return utils.Success(c, http.StatusCreated, "Created", entity)
   }
   ```

6. **Register routes** in `routes/routes.go`:
   ```go
   protected.POST("/entities", entity.CreateEntity)
   protected.GET("/entities/:id", entity.GetEntity)
   ```

### Testable Handler Pattern

For handlers that call multiple repositories (hard to test with a global singleton), use function-pointer deps:

1. Define a `deps` struct with function-pointer fields matching the repo signatures.
2. Write the handler core as an unexported function accepting `(deps, c echo.Context)`.
3. Write the public handler that wires the real repos and delegates.
4. In tests, pass a struct with mock functions — no interface needed.

See `controllers/guests/guests.go` (`handleGetAttendees`) and `controllers/guests/guests_test.go` for a complete example.

### Adding a Public Attendees Endpoint

Pattern used for `GET /api/events/section/:sectionId/attendees`:
1. Add query to repository: `ListAttendeesByEventID(eventID uuid.UUID) ([]models.Guest, error)`.
2. Add public route in `routes/routes.go` under `public` group.
3. Handler validates UUID, resolves `sectionId → eventID` via section lookup, then calls attendees repo.
4. Return `{ "data": [...AttendeeDTO] }` (only expose public fields: FirstName, LastName, Nickname, Role, Order).

### Adding a Protected Route

All protected routes must use the authentication middleware which is already applied to the `protected` route group in `routes/routes.go`. Simply add your route to that group:

```go
protected.POST("/your-endpoint", yourController.YourHandler)
```

Access authenticated user in handler:
```go
func YourHandler(c echo.Context) error {
    cognitoSub := c.Get("cognito_sub").(string)
    // Use cognitoSub to query User model or enforce access control
}
```

### Working with Cache

Cache invalidation happens automatically in services after mutations. Use the pattern:
```go
return redisrepository.Invalidate("resourceType", "all")
```

For custom cache patterns, see `repositories/cacheloaderrepository/` and `utils/redisKeys.go`.

## Deployment

Deployment is automated via GitHub Actions (`.github/workflows/deploy-backend.yml`):
- Triggers on push to `main` branch
- Builds Docker image on EC2 instance
- Runs container with environment variables from GitHub Secrets
- Server runs on port configured via `BACKEND_PORT` secret

## Documentation Structure

This repository uses a modular documentation system for optimal token efficiency:

### Documentation Files (`docs/`)

| File | When to Consult | What It Contains |
|------|----------------|------------------|
| `docs/ARCHITECTURE.md` | Starting new work, understanding system | Tech stack, layer structure, request flow, design patterns |
| `docs/MODELS.md` | Looking for data models | Complete list of all models, fields, relationships |
| `docs/ROUTES.md` | Looking for API endpoints | All routes, HTTP methods, controller mapping |
| `docs/SERVICES.md` | Looking for business logic | All services, key functions, cache patterns |
| `docs/REPOSITORIES.md` | Looking for data access code | All repositories, CRUD operations, custom queries |
| `docs/ENVIRONMENT.md` | Configuration questions | All env vars, setup instructions, security notes |
| `docs/COMMON_TASKS.md` | How to add/change features | Step-by-step guides for common development tasks |

### Quick Start for Common Questions

- "Where is model X?" → `docs/MODELS.md`
- "What routes exist?" → `docs/ROUTES.md`
- "How do I add a new entity?" → `docs/COMMON_TASKS.md`
- "What env vars are needed?" → `docs/ENVIRONMENT.md`
- "How does authentication work?" → `docs/ARCHITECTURE.md`
- "Where is the service for X?" → `docs/SERVICES.md`

### Keeping Documentation Updated

**Every time you make a code change, check this table:**

| You Changed... | Then Update... |
|---------------|---------------|
| Added/modified a model | `docs/MODELS.md` |
| Added/modified a route | `docs/ROUTES.md` |
| Added/modified a service | `docs/SERVICES.md` |
| Added/modified a repository | `docs/REPOSITORIES.md` |
| Added/modified env variable | `docs/ENVIRONMENT.md` |
| Changed architecture/patterns | `docs/ARCHITECTURE.md` |
| Discovered new workflow | `docs/COMMON_TASKS.md` |

**Outdated documentation = wasted tokens. Always keep it current.**

## Important Notes

- Server runs on port 8080 by default (configurable via PORT env var)
- All UUIDs use `github.com/gofrs/uuid` package (not `github.com/google/uuid` for GORM models)
- Healthcheck endpoint: `GET /health`
- Public routes do NOT require authentication (see `/api` public group in routes)
  - `GET /api/events/page-spec?token=` — SDUI page spec for frontend
  - `GET /api/events/section/:sectionId/attendees` — list public attendees (guests) for a section
  - `GET /api/invitations/ByToken/:token` — invitation data for RSVP (Echo v4 is case-sensitive: **capital B and T**)
- Protected routes require `Authorization: Bearer <token>` header with valid Cognito JWT
- Cache is automatically populated by middleware and stored in Echo context
- Redis test write happens on startup (`server.go:31-34`) - can be removed in production
