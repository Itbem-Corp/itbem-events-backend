# Services Index

> Update this file whenever you add, modify, or remove services.
> For code patterns, see `docs/TEMPLATES.md`.

## Service Layer Purpose

- Business logic and orchestration
- Coordinate between repositories
- Cache invalidation after mutations
- Input validation (business rules)

## Available Services

### Delivery workflow (`services/deliveryworkflow/`)

| File | Purpose |
|---|---|
| `workflow.go` | Pure state machine for private ITBEM delivery work. Plan, code review, QA review, and release require matching human gate decisions. Code approval authorizes only a preview; `preview_ready` is required before QA begins. |

The delivery controller queues a phase only from its matching lifecycle state
and writes a short, explicitly encrypted JSON input object. Submitting a plan,
code review, or QA review requires a completed linked agent task; its private
output is persisted as report evidence, never copied into Postgres. `preview_ready`
requires an explicit HTTP(S) preview URL before QA can start.

### Notifications (`services/notifications/`)

| File | Purpose |
|---|---|
| `SlackNotificationService.go` | Persists Slack notification jobs for asynchronous Block Kit delivery by `itbem-events-workers`. See `docs/SLACK_NOTIFICATIONS.md`. |

### Events Domain (`services/events/`)

| File | Purpose |
|------|---------|
| `EventService.go` | CRUD + business rules for events. Auto-generates `Identifier` slug from `Name` on create if empty (uses `utils.Slugify`, ensures uniqueness). |
| `EventTypeService.go` | Catalog CRUD for event types |
| `EventConfigService.go` | Per-event configuration management |
| `EventSectionService.go` | Event section ordering and management |
| `EventAnalyticsService.go` | Analytics tracking and aggregation |
| `PageSpecService.go` | Public SDUI page spec assembly by token or event identifier |
| `RepairService.go` | Event integrity repair workflow |

### Guests Domain (`services/guests/`)

| File | Purpose |
|------|---------|
| `GuestService.go` | CRUD + bulk operations for guests |
| `GuestStatusService.go` | Catalog CRUD for guest statuses |

### Invitations Domain (`services/invitations/`)

| File | Purpose |
|------|---------|
| `InvitationService.go` | Send/track invitations, RSVP logic |

### Clients Domain (`services/clients/`)

| File | Purpose |
|------|---------|
| `clientService.go` | Multi-tenant client CRUD, hierarchy |

### Client Roles/Types (`services/clientroles/`, `services/clienttypes/`)

| File | Purpose |
|------|---------|
| `clientRoleService.go` | Role catalog lookup |
| `clientTypeService.go` | Client type catalog lookup |

### Users Domain (`services/users/`)

| File | Purpose |
|------|---------|
| `UserService.go` | User profile, Cognito sync, avatar |
| `AdminUserService.go` | Admin user management (list, activate, deactivate) |

### Resources Domain (`services/resources/`)

| File | Purpose |
|------|---------|
| `Resources.go` | Core resource CRUD + S3 upload |
| `ResourceTypes.go` | Resource type catalog |
| `ImageOptimizer.go` | libvips image compression pipeline |
| `ResourcesUsers.go` | User-specific resource operations (avatar) |
| `ResourcesClients.go` | Client-specific resource operations (logo) |

### Design/Themes Domain

| File | Purpose |
|------|---------|
| `services/templates/DesignTemplateService.go` | Design template CRUD |
| `services/moments/MomentService.go` | Event moment CRUD + async media job enqueue |
| `services/moments/MomentTypeService.go` | Moment type catalog |

### Colors Domain (`services/colors/`)

| File | Purpose |
|------|---------|
| `ColorService.go` | Color CRUD |
| `ColorPaletteService.go` | Palette CRUD |
| `ColorPalettePatternService.go` | Palette-color assignment |

### Fonts Domain (`services/fonts/`)

| File | Purpose |
|------|---------|
| `FontService.go` | Font CRUD |
| `FontSetService.go` | Font set CRUD |
| `FontSetPatternService.go` | FontSet-font assignment |

### Cross-Cutting

| File | Purpose |
|------|---------|
| `services/validations/Validations.go` | Shared validation helpers |
| `services/cacheutil/json_cache.go` | Generic JSON cache-aside helper with concurrent miss coalescing for catalog/read models |

## Cache Pattern (All Mutation Services)

```go
// After CREATE / UPDATE / DELETE:
return s.cache.Invalidate("resourceType", "all")
```

### Per-User Cache (`ClientService.GetMyClients`)

`GetMyClients` uses a per-user Redis key instead of a global key:
- Key: `{userID}:myclients`
- TTL: `constants.ShortTimeTTL` (1 hour)
- Invalidated on: `CreateClient`, `AddUserToClient`, `RemoveClientMember`, `UpdateClientDetails`
- Pattern-deleted (`*:myclients`) on: `DeleteClient` (affects all users)

```go
func (s *ClientService) invalidateMyClients(userID uuid.UUID) {
    _ = s.cache.Invalidate("myclients", userID.String())
}
func (s *ClientService) invalidateAllMyClients() {
    _ = s.cache.DeleteKeysByPattern(ctx, "*:myclients")
}
```

## Injectable Struct Pattern (All Services)

Every service has an injectable struct with a constructor. The structs are used in production via `internal/app/app.go` and in tests via mock injection.

> **Singleton delegation pattern**: Every service package exposes `var _svc *XxxService` + `SetDefaultXxx(svc *XxxService)`. The package-level functions (`CreateEvent()`, `DeleteMoment()`, etc.) delegate to this singleton. `internal/app/app.go` calls all `SetDefaultXxx` functions immediately after the controller `Init` calls. This makes cross-domain calls like `users.SyncUser()` from the clients controller use the fully-injected DI instance instead of raw repo packages.

| Service | Constructor | Interface Dependencies |
|---------|-------------|----------------------|
| `EventService` | `NewEventService(repo, cache)` | `ports.EventsRepository`, `ports.CacheRepository` |
| `EventConfigService` | `NewEventConfigService(repo, cache)` | `ports.EventConfigRepository`, `ports.CacheRepository` |
| `EventAnalyticsService` | `NewEventAnalyticsService(repo, cache)` | `ports.EventAnalyticsRepository`, `ports.CacheRepository` |
| `EventSectionService` | `NewEventSectionService(repo, cache)` | `ports.EventSectionRepository`, `ports.CacheRepository` |
| `PageSpecService` | `NewPageSpecService(tokenRepo, invitationRepo, eventRepo, sectionRepo, configRepo)` | `ports.AccessTokenRepository`, `ports.InvitationRepository`, `ports.EventsRepository`, `ports.EventSectionRepository`, `ports.EventConfigRepository` |
| `RepairService` | `NewRepairService(db, eventRepo)` | `*gorm.DB`, `ports.EventsRepository` |
| `GuestService` | `NewGuestService(repo, tokenRepo, cache, tx)` | `ports.GuestRepository`, `ports.AccessTokenRepository`, `ports.CacheRepository`, `ports.Transactor` |
| `GuestStatusService` | `NewGuestStatusService(repo, cache)` | `ports.GuestStatusRepository`, `ports.CacheRepository` |
| `InvitationService` | `NewInvitationService(repo, guestRepo, tokenRepo, logRepo, cache)` | 5 interfaces |
| `MomentService` | `NewMomentService(repo, cache, mediaPublisher...)` | `ports.MomentRepository`, `ports.CacheRepository`, optional `ports.MediaJobPublisher` |
| `MomentTypeService` | `NewMomentTypeService(repo, cache)` | `ports.MomentTypeRepository`, `ports.CacheRepository` |
| `UserService` | `NewUserService(userRepo, authRepo, cfg, objectDeleter...)` | `ports.UserRepository`, `ports.AuthProviderRepository`, `*models.Config`, optional object deleter |
| `AdminUserService` | `NewAdminUserService(userRepo, clientRepo, authRepo)` | `ports.UserRepository`, `ports.ClientRepository`, `ports.AuthProviderRepository` |
| `ClientService` | `NewClientService(clientRepo, roleRepo, typeRepo, rs, cache, tx)` | `ports.ClientRepository`, `ports.ClientRoleRepository`, `ports.ClientTypeRepository`, `*ResourceService`, `ports.CacheRepository`, `ports.Transactor` |
| `ClientRoleService` | `NewClientRoleService(roleRepo)` | `ports.ClientRoleRepository` |
| `ClientTypeService` | `NewClientTypeService(typeRepo)` | `ports.ClientTypeRepository` |
| `ColorService` | `NewColorService(repo, cache)` | `ports.ColorRepository`, `ports.CacheRepository` |
| `FontService` | `NewFontService(resourceSvc, deps...)` | `ports.FontRepository`, `ports.CacheRepository`, `*ResourceService` |
| `DesignTemplateService` | `NewDesignTemplateService(repo, cache)` | `ports.DesignTemplateRepository`, `ports.CacheRepository` |
| `EventTypeService` | `NewEventTypeService(repo, cache)` | `ports.EventTypeRepository`, `ports.CacheRepository` |
| `ResourceService` | `NewResourceService(cfg, deps...)` | `ports.ResourceRepository`, `ports.ObjectStorageRepository`, `ports.CacheRepository` |

> **`AdminUserService.ListAllUsers(query dtos.AdminUsersListQuery)`** - accepts pagination plus optional `search` and `status` filters. Returns typed rows with `data`, `total`, `page`, `page_size`, `total_pages`. Page defaults to 1, page_size defaults to 50, max 200, and filtering/pagination happens in the repository query.

All interfaces are defined in `services/ports/ports.go`.

## File Location Pattern

```
services/<domain>/<Domain>Service.go
services/ports/ports.go    ← all repository interfaces
```
# Delivery workflow service

- **File**: `services/deliveryworkflow/workflow.go`
- **Purpose**: Enforces the ITBEM private delivery state machine without a
  database dependency. `plan`, `code_review`, `qa_review`, and `release`
  transitions require a matching human `DeliveryGate`; an agent or controller
  cannot skip directly to QA or production release.
