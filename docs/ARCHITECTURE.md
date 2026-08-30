# Architecture Overview

## Frontend integration contract

Language-neutral request contracts live in `itbem-product-contract`. Backend runtime constants are projected into `internal/requestcontext`, while middleware and CORS depend on that package instead of repeating frontend-facing strings. A contract test compares the projection with the pinned `.contracts` revision, so integration drift fails before deployment.

Controllers own HTTP translation, services own use cases, repositories own persistence, and `internal/requestcontext` owns only transport vocabulary and invariants. Product authorization remains in application access and product capability layers; request headers never grant access.

Request-context resolution is a pure core policy with no Echo, database, or service dependency. HTTP middleware only adapts headers and the resolved session into that policy, then stores the validated result in the request context. New applications reuse the policy and extend product capabilities instead of copying middleware branches. The organization check is callback-based to avoid allocating a temporary membership map on every request.

Clients exchange an authenticated application session for a five-minute organization-context token at `POST /api/session/organization-context`. The token supplements Cognito rather than replacing it and is cryptographically bound to the Cognito subject, application code, and organization ID. Business requests selecting an organization through `X-Organization-ID` must also send `X-Organization-Context`; middleware fails closed when the credential is missing, tampered, expired, or bound to another subject, application, or organization. `GET /api/session` and the credential-issuance endpoint permit an absent credential to avoid a bootstrap or renewal cycle, but still require Cognito, application access, and organization membership. A credential explicitly presented to either endpoint is always validated. Platform workspaces do not use an organization token.

## System Architecture

This is a Go-based events management backend following **clean architecture** principles with clear separation of concerns.

## Tech Stack

- **Language**: Go 1.25.12
- **Module**: `events-stocks` (see go.mod)
- **Web Framework**: Echo (high-performance HTTP router)
- **ORM**: GORM (PostgreSQL)
- **Cache**: Redis
- **Authentication**: AWS Cognito (JWT tokens)
- **File Storage**: AWS S3
- **Image Processing**: libvips
- **Containerization**: Docker

## Three-Layer Architecture

```
┌─────────────────────────────────────┐
│         Controllers Layer           │
│   (HTTP handlers, request/response) │
└─────────────┬───────────────────────┘
              │
              ▼
┌─────────────────────────────────────┐
│          Services Layer             │
│  (Business logic, orchestration)    │
└─────────────┬───────────────────────┘
              │
              ▼
┌─────────────────────────────────────┐
│        Repositories Layer           │
│   (Data access, GORM, Redis, AWS)   │
└─────────────────────────────────────┘
```

### 1. Controllers (`controllers/`)
**Responsibility**: Handle HTTP requests and responses

- Parse and validate request data (`c.Bind()`)
- Call service layer functions
- Return standardized JSON responses
- No business logic - only request/response handling

**File Pattern**: `controllers/<domain>/<domain>.go`

**Example**:
```go
func CreateEvent(c echo.Context) error {
    var event models.Event
    if err := c.Bind(&event); err != nil {
        return utils.Error(c, http.StatusBadRequest, "Invalid request", err.Error())
    }

    if err := eventService.CreateEvent(&event); err != nil {
        return utils.Error(c, http.StatusInternalServerError, "Error creating", err.Error())
    }

    return utils.Success(c, http.StatusCreated, "Created", event)
}
```

### 2. Services (`services/`)
**Responsibility**: Implement business logic and orchestration

- Coordinate between multiple repositories
- Implement complex business rules
- Handle cache invalidation
- Validate business constraints
- No direct database access - use repositories. Operational repair code is the exception and receives `*gorm.DB` explicitly from the composition root.

**File Pattern**: `services/<domain>/<Domain>Service.go`

**Example**:
```go
func CreateEvent(obj *models.Event) error {
    // Validate business rules
    if obj.Date.Before(time.Now()) {
        return errors.New("event date cannot be in the past")
    }

    // Save through injected repository
    if err := s.repo.CreateEvent(obj); err != nil {
        return err
    }

    // Invalidate cache
    return s.cache.Invalidate("events", "all")
}
```

### 3. Repositories (`repositories/`)
**Responsibility**: Handle all data access

- Direct database operations via GORM
- Redis cache operations
- AWS S3 file operations
- Generic CRUD via `gormrepository`
- No business logic - pure data access

**File Pattern**: `repositories/<domain>repository/<Domain>Repository.go`

**Example**:
```go
func CreateEvent(event *models.Event) error {
    return gormrepository.Insert(event)
}

func GetEventByID(id uuid.UUID) (*models.Event, error) {
    var event models.Event
    err := gormrepository.FindByID(id, &event)
    return &event, err
}
```

## Request Flow

```
HTTP Request
    │
    ▼
[Middleware: Token Auth] ──> Validates JWT, injects user context
    │
    ▼
[Middleware: Redis Cache] ──> Loads cached data into context
    │
    ▼
[Controller] ──> Parses request, calls service
    │
    ▼
[Service] ──> Business logic, calls repository
    │
    ▼
[Repository] ──> Database/Cache/AWS operations
    │
    ▼
[Service] ──> Invalidates cache if mutation
    │
    ▼
[Controller] ──> Returns JSON response
    │
    ▼
HTTP Response
```

## Module Organization

Each business domain follows consistent structure:

```
<domain>/
├── controllers/<domain>/<domain>.go         ── HTTP handlers
├── services/<domain>/<Domain>Service.go     ── Business logic
├── repositories/<domain>repository/         ── Data access
│   └── <Domain>Repository.go
└── models/<Domain>.go                       ── Data model
```

**Example for "events" domain**:
```
controllers/events/events.go
services/events/EventService.go
repositories/eventsrepository/EventsRepository.go
models/Event.go
```

## Dependency Injection (DI) Pattern

All services and repositories now follow an **interface-based DI pattern** that enables unit testing without a real DB or Redis.

### Interface definitions — `services/ports/ports.go`
All repository and integration contracts live here as Go interfaces:
`CacheRepository`, `MediaJobPublisher`, `ObjectStorageRepository`, `Transactor`, `EventsRepository`, `EventConfigRepository`, `EventAnalyticsRepository`, `EventSectionRepository`, `GuestRepository`, `InvitationRepository`, `AccessTokenRepository`, `InvitationLogRepository`, `MomentRepository`, `ResourceRepository`, `ColorRepository`, `FontRepository`, `DesignTemplateRepository`, `EventTypeRepository`, `GuestStatusRepository`, `MomentTypeRepository`, `UserRepository`, `ClientRepository`, `ClientRoleRepository`, `ClientTypeRepository`, `AuthProviderRepository`.

### Concrete repository structs
Each repository file now exports a struct that implements its interface:
```go
type EventsRepo struct{}
func NewEventsRepo() *EventsRepo { return &EventsRepo{} }
func (r *EventsRepo) CreateEvent(event *models.Event) error { return CreateEvent(event) }
```
Old package-level repository functions remain untouched — zero breakage. The struct methods delegate to them.

### Service structs (injectable)
Each service file exports a struct with a constructor that accepts interface dependencies:
```go
type EventService struct {
    repo  ports.EventsRepository
    cache ports.CacheRepository
}
func NewEventService(repo ports.EventsRepository, cache ports.CacheRepository) *EventService {
    return &EventService{repo: repo, cache: cache}
}
```

> **Singleton delegation pattern**: Each service package exposes `var _svc *XxxService` + `SetDefaultXxx(svc)`. The package-level functions (`CreateEvent()`, `DeleteMoment()`, etc.) delegate to this singleton. `internal/app/app.go` calls `SetDefaultXxx` after each `Init` call, so cross-domain callers (e.g. `users.SyncUser()` from the clients controller) use the fully-injected DI instance.

### Controller Init pattern
Each controller exposes a package-level var + `Init` function wired by `internal/app/app.go`:
```go
var (
    eventSvc       *eventsService.EventService
    eventConfigSvc *eventsService.EventConfigService
)
func InitEventsController(svc *eventsService.EventService, cfgSvc *eventsService.EventConfigService) {
    eventSvc = svc
    eventConfigSvc = cfgSvc
}
```

### Composition root — `internal/app/app.go`
`internal/app/app.go` instantiates all repos, integration adapters, and services; configures `internal/authz`; calls all controller `Init` functions; then calls all `SetDefaultXxx` functions before starting the server. This is the ONLY place where concrete types are wired together.

### Authorization boundary — `internal/authz`
Protected handlers should use `internal/authz` for tenant checks instead of duplicating repository calls. The standard path is user → event → client access, with helpers for event sections, guests, invitations, moments, and resources.
Production dependencies are provided by `authz.Configure(...)` from `internal/app/app.go`; tests can replace specific hooks with `authz.ReplaceHooksForTest(...)`.

### Unit testing pattern (for QA agent)
```go
// No DB needed — inject mocks
svc := invitations.NewInvitationService(mockInvRepo, mockGuestRepo, mockTokenRepo, mockLogRepo, mockCache)
_, err := svc.ConfirmRSVP("ABC123", "confirmed", "web", 5, "")
```

## Key Design Patterns

### Standard Response Format
All controllers return consistent JSON:
```go
// Success
return utils.Success(c, http.StatusOK, "Message", dataObject)

// Error
return utils.Error(c, http.StatusBadRequest, "Message", errorDetails)
```

### Cache Invalidation Pattern
Services invalidate cache after mutations:
```go
// After CREATE/UPDATE/DELETE
return s.cache.Invalidate("resourceType", "all")
```

### Authentication Context Pattern
Protected routes have access to user context:
```go
cognitoSub := c.Get("cognito_sub").(string)  // AWS Cognito user ID
userEmail := c.Get("user_email").(string)    // User's email
cfg := c.Get("config").(*models.Config)      // App configuration
```

## Middleware Chain

### Public Routes (`/api`)
1. Body limit and public rate limiter
2. Service-level Redis cache only inside handlers/services that explicitly opt in

### Protected Routes (`/api`)
1. Token authentication middleware (JWT validation)
2. Application-access middleware resolves the product session and validates
   optional dashboard context headers against the signed-token tenant.
3. Dashboard body/rate limits
4. Service-level Redis cache only inside handlers/services that explicitly opt in

The dashboard context headers are `X-Application-Code`, `X-Workspace-Mode`,
and `X-Organization-ID`. They cannot grant access: platform mode requires root
authority enabled by the application, and organization mode is checked against
the resolved application membership before resource-level authorization runs.

## Database Strategy

### Auto-Migration
All models are auto-migrated on server startup:
- Defined in `configuration/gorm.go`
- `modelsWithoutSeed` - Regular models
- `modelSeedList` - Models with seed data

### Seed Data
Reference/catalog data is seeded automatically:
- Only runs if tables are empty
- Defined in `seeds/` directory
- Registered in `configuration/gorm.go`

## Configuration Management

Environment variables loaded via `configuration/environmentVariables.go`:
- Local dev: reads `.env` file
- Production: uses system environment variables
- Field names auto-converted to UPPER_SNAKE_CASE
- Missing required variables cause fatal error

## Multi-Tenancy

Client hierarchy support:
- `Client.ParentClientID` for nested organizations
- `ClientMember` junction table (User ↔ Client)
- `ClientRole` for permissions
- Routes enforce client-scoped access

## File Upload Strategy

AWS S3 integration:
- Upload logic in `repositories/awsrepository/`
- `Resource` model tracks uploaded files
- Image processing via `libvips` on Linux+cgo; Windows/no-cgo builds use a no-op optimizer fallback so local tests compile without native libvips.
- S3 credentials from environment variables
- Async media processing is published through `ports.MediaJobPublisher`; SQS is the current concrete adapter in `repositories/sqsrepository`.

## Validation Pattern

### Request validation in controllers

All handlers that accept user input call both `c.Bind()` and `c.Validate()`:

```go
var guest models.Guest
if err := c.Bind(&guest); err != nil {
    return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
}
if err := c.Validate(&guest); err != nil {
    return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
}
```

### Nested struct validation fix (`validate:"-"`)

`go-playground/validator` descends into **non-pointer** nested struct fields and validates their tags recursively. This causes false validation failures when a model contains `validate:"required"` on fields inside a relationship struct (e.g. `Event.Name`).

**Fix**: add `validate:"-"` to every non-pointer relationship field in models:

```go
// models/Guest.go
Event       Event       `gorm:"foreignKey:EventID" json:"-" validate:"-"`
GuestStatus GuestStatus `gorm:"foreignKey:GuestStatusID" json:"-" validate:"-"`
```

Applied to: `Guest.Event`, `Guest.GuestStatus`, `Moment.Invitation`, `Moment.MomentType`, `Invitation.Event`, `Event.EventType`, `Event.EventConfig`.

### Error message standard

All HTTP error/success messages must be in **English**. This applies to strings passed to `utils.Error(c, ..., "Message", ...)` and `utils.Success(c, ..., "Message", ...)`. Do not use Spanish strings in controller responses.

## Dependencies

See `go.mod` for full list. Key dependencies:
- `github.com/labstack/echo/v4` - HTTP framework
- `gorm.io/gorm` - ORM
- `github.com/redis/go-redis/v9` - Redis client
- `github.com/aws/aws-sdk-go-v2` - AWS SDK
- `github.com/gofrs/uuid` - UUID generation (CRITICAL: use this, not google/uuid)

## Deployment Architecture

- GitHub Actions CI/CD (`.github/workflows/deploy-backend.yml`)
- Docker containerization
- Runs on EC2 instance
- Port 8080 (configurable via PORT env var)
- Environment secrets stored in GitHub Secrets

## Performance Considerations

- Redis caching on read-heavy endpoints
- Automatic cache invalidation on writes
- GORM connection pool: `SetMaxOpenConns(50)`, `SetMaxIdleConns(15)`, `SetConnMaxLifetime(5m)` — configured in `configuration/gorm.go`
- Middleware-based caching to reduce DB queries
- S3 presigned URLs generated only for resources with non-empty `Path` (no N+1 HeadObject calls)
- Gzip compression on responses ≥ 1 KB (level 5) — reduce ancho de banda 60-80%
- 5m global HTTP request timeout; graceful shutdown uses a separate 10s timeout.
- Body limits by route group: public 2 MB, protected dashboard 25 MB, public media uploads 225 MB, internal callbacks 2 MB.

## Security

- AWS Cognito JWT token validation
- Per-route authentication middleware
- Client-scoped data access
- Parameterized queries (GORM prevents SQL injection)
- CORS configuration in Echo
- Cache flush endpoints (`/cache/flush/*`) require root access
- `middleware.Secure()` — X-Frame-Options, X-XSS-Protection, X-Content-Type-Options headers
- Rate limiting por IP: público 20 req/s (burst 40), protegido 60 req/s (burst 100)
- Input validation via `go-playground/validator/v10` — `required`, `email`, `oneof` en campos clave
- Admin-only endpoints check `user.IsRoot` via `requireRoot(c echo.Context) bool` helper (see Auth Guard Pattern below)
- `InviteUser` (clients) validates OWNER/ADMIN role on target client via `CheckAccessRecursive` before adding members

### Auth Guard Pattern (boolean helper)

Admin-only handlers use a boolean helper that writes the response AND returns false on failure. This avoids the nil-error trap where `utils.Error` returns `nil` (not an error) because `c.JSON()` succeeds.

```go
// controllers/users/users.go
func requireRoot(c echo.Context) bool {
    cognitoSub, ok := c.Get("cognito_sub").(string)
    if !ok {
        _ = utils.Error(c, http.StatusUnauthorized, "No autorizado", "Token inválido")
        return false
    }
    requester, err := userSvc.SyncUser(cognitoSub)
    if err != nil || !requester.IsRoot {
        _ = utils.Error(c, http.StatusForbidden, "Forbidden", "Admin only")
        return false
    }
    return true
}

// Usage in handler:
func ListAllUsers(c echo.Context) error {
    if !requireRoot(c) {
        return nil  // response already written
    }
    // ... admin logic
}
```

> **IMPORTANT**: Do NOT use `if err := requireRoot(c); err != nil { return err }` — `utils.Error` returns `nil`, making the guard always fall through.

### Config nil-safe extraction

Never use bare type assertion for config — it panics if middleware is bypassed:

```go
// Safe pattern:
cfg, ok := c.Get("config").(*models.Config)
if !ok || cfg == nil {
    return utils.Error(c, http.StatusInternalServerError, "Config Error", "missing config")
}
```

## Monitoring & Health

- Local engineering automation is handed off through a durable outbox. The
  publisher supports a backward-compatible combined SQS queue or an atomic
  five-lane map (`orchestration`, `engineering`, `review`, `qa`, `release`).
  Routing is derived only from the fail-closed operation contract in
  `internal/agentwork`; callers cannot select a lane. A configured lane map
  must be complete and use distinct queues, and aggregate health includes a
  per-lane projection plus the role DLQ. Queue membership never grants source,
  merge, release, deployment, secret, or production authority.
  The Linux execution plane uses one hardened systemd template instance and
  unprivileged Unix account plus a private `0700` workspace root per lane.
  Cross-lane checkout reuse is prohibited. A separate read-only oneshot doctor
  validates configuration without polling SQS; the Release doctor also requires
  its GitHub App identity. On-premises lanes obtain scoped temporary AWS
  sessions through separate IAM Roles Anywhere credential-process profiles;
  static IAM access keys and metadata fallback are disabled. Global/per-lane filesystem kill switches prevent
  restart, and installation never implies activation.

- Merge and release policy is evaluated by `internal/releasegate` as a pure,
  deterministic, fail-closed decision over structured exact-SHA evidence. The
  revision matrix and resolved policy are digest-bound; model output cannot
  waive a reason code. An allowed decision grants no side effect by itself.
  See `docs/agent-platform/RELEASE_GATEKEEPER.md`.

- Application notifications use `services/notifications` with the durable
  outbox and worker queue. `itbem-events-workers` owns Block Kit,
  SSM webhook access, HTTP delivery, retry, and DLQ behavior. The worker is
  assigned one immutable destination; callers cannot select another channel.
  Encrypted webhook URLs are resolved from SSM and never enter business-layer code.

- Health check endpoint: `GET /health`
- Startup validation: database connection, Redis connection
- Graceful shutdown on `SIGINT`/`SIGTERM` — 10s timeout before force-stop (`internal/app/app.go`)

## When to Update This File

- ✅ Changed overall architecture patterns
- ✅ Added new layers or components
- ✅ Changed request flow
- ✅ Added new middleware
- ✅ Changed deployment strategy
- ✅ Added new external dependencies (AWS services, databases, etc.)
