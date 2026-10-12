# Services Index

> Update this file whenever you add, modify, or remove services.
> For code patterns, see `docs/TEMPLATES.md`.

## Service Layer Purpose

- Business logic and orchestration
- Coordinate between repositories
- Cache invalidation after mutations
- Input validation (business rules)

## Available Services

### Project Vault onboarding (`internal/projectvault/`)

| File | Purpose |
|---|---|
| `proposal.go` | Pure deterministic static onboarding: strict GitHub URL canonicalization, safe inventory normalization, evidence-backed stack/command proposals, complete capability matrix and immutable Vault manifest digest. Repository content is untrusted and commands are never executed here. |

The Delivery onboarding controller supplies an immutable GitHub snapshot and
persists the proposal. Human approval repeats the SHA and publishes repository
context plus a new append-only Vault revision transactionally. See
`docs/agent-platform/DOMAIN_AND_THREAT_MODEL.md`.

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

### Event clients (`services/eventclients/`, EventiApp)

| File | Purpose |
|------|---------|
| `EventClientService.go` | Event-client grants behind `EVENT_CLIENT_ACCESS`: hardened identity resolution (one live user with a matching, verified, enabled Cognito email; otherwise a tenant-branded invitation; `UsernameExists` resolves the Cognito account by email and mirrors it with `SyncUser`), eligibility (not self, not a root, active, no membership in the organization tree, EventiApp organization), grant/revoke in transactions, assignments, organization oversight and invitation resend. Collapses every enumeration-prone cause into `event_client_unavailable`. Invalidates the target's cached session after each change. |

`services/applications/SessionService.go` gains `ConfigureEventClientAccess`
(grant-only sessions, `event_access`) and `InvalidateCognitoSub`.

### Guests Domain (`services/guests/`)

| File | Purpose |
|------|---------|
| `GuestService.go` | CRUD + bulk operations for guests. `CreateGuestsWithInvitations` inserts guests with `Omit(clause.Associations)` (a nested table or status object never creates rows) and owns both references: each guest gets a new invitation and `rsvp_token_id` is cleared (the RSVP flow links the token). `UpdateGuest` syncs `max_guests` to the guest's stored invitation only, never one named by the caller, and only when that invitation's `event_id` is the guest's event (otherwise nothing is written and a warning with the guest and event ids is logged), and clears the door check-in provenance when the RSVP status, method or status changes. Bulk status goes through `transitionGuestStatuses` (locked `TransitionGuestStatuses`, then per-guest RSVP analytics and cache invalidation): `CheckInGuests` (door check-in, records the previous RSVP), `UndoGuestCheckins` (restores it; `ErrCheckinNotUndoable` for a guest confirmed any other way) and `BulkUpdateGuestStatus` (guest:manage status change, the organizer's plain confirmation included; clears the provenance). `rsvp_at` is stamped only when the RSVP status changes. |
| `GuestStatusService.go` | Catalog CRUD for guest statuses |

### Invitations Domain (`services/invitations/`)

| File | Purpose |
|------|---------|
| `InvitationService.go` | Send/track invitations, RSVP logic. `ConfirmRSVPWithResult` stores `rsvp_method` only as `web` or `app` (anything else, `host` included, becomes `web`) and clears the guest's door check-in provenance. |

### Clients Domain (`services/clients/`)

| File | Purpose |
|------|---------|
| `clientService.go` | Multi-tenant client CRUD, hierarchy, member management and member-target ranking (`AuthorizeMemberTarget`) |

### Client Roles/Types (`services/clientroles/`, `services/clienttypes/`)

| File | Purpose |
|------|---------|
| `clientRoleService.go` | Role catalog lookup |
| `clientTypeService.go` | Client type catalog lookup |

### Users Domain (`services/users/`)

| File | Purpose |
|------|---------|
| `UserService.go` | User profile, Cognito sync, avatar, root-level delegation (`SetUserRootLevel`) |
| `AdminUserService.go` | Admin user management (list, activate, deactivate, delete); refuses to delete or deactivate primary roots |

### Resources Domain (`services/resources/`)

| File | Purpose |
|------|---------|
| `Resources.go` | Resource CRUD, upload orchestration, and cache invalidation |
| `ResourceObjectStorage.go` | Presigned URLs and object deletion |
| `ResourceTypes.go` | Resource type catalog |
| `ImageOptimizer.go` | libvips image compression pipeline |
| `ResourcesUsers.go` | User-specific resource operations (avatar) |
| `ResourcesClients.go` | Client-specific resource operations (logo) |

### Design/Themes Domain

| File | Purpose |
|------|---------|
| `services/templates/DesignTemplateService.go` | Design template CRUD |
| `services/moments/MomentService.go` | Event moment CRUD + async media job enqueue. `GetMomentsByIDs` loads a bulk request's moments in one query so the bulk handlers authorize once per event. `IsEventMomentKey(eventID, key)` accepts only `moments/<eventID>/...`, bare or under `organizations/<uuid>/` (no absolute URLs, `..` or backslashes); `POST /moments` uses it before storing a key the admin response will presign. |
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

> **`UserService.SetUserRootLevel(userID, level)`** - accepts only `0` or `2` (anything else is an error mapped to `400`), writes `root_level=level` and clears the legacy `is_root` flag. It returns `gorm.ErrRecordNotFound` for an unknown user (`404`) and the sentinel `users.ErrProtectedRootAccount` when the target is a primary root (`IsPrimaryRoot()`: level 1 or a legacy `is_root` account), which the controller maps to `409 Protected account`. Level 1 cannot be restored through the API, so this also blocks self-demotion and losing the last primary root. Callers are gated by `authz.RequirePrimaryRoot`.

> **`AdminUserService.DeleteUser(userID)` / `AdminUserService.SetUserActive(userID, false)` / `UserService.DeleteFullAccount(cognitoSub)`** - return the sentinel `users.ErrProtectedRootAccount` (wrapped) when the stored account is a primary root (`IsPrimaryRoot()`), before touching Cognito or the database; the controllers map it to `409 Protected account`. `SetUserActive(userID, true)` stays allowed for primary roots so a disabled one can be recovered.

> **`ClientService.AuthorizeMemberTarget(requester, clientID, targetUserID, lookupUser)`** (package entry `clients.AuthorizeMemberTarget`) - delegation check for actions that change another member's access without changing their organization role (member application toggles in `controllers/sessions`, event assignments in `controllers/eventmembers`) and for adding an existing account (`controllers/clients` `InviteUser`, since the new direct membership takes precedence over an inherited Owner/Admin role). Callers check `members:manage` first. A primary root may act on anyone; nobody else on a platform-root account; Root 2 on anyone except an Owner (effective role hierarchy 1); an Owner/Admin only on members whose effective (direct or `INHERITED_`) role ranks strictly below its own, never itself. A target with no user row or no access to `clientID` has no rank. Denials wrap `clients.ErrMemberTargetDenied` (`403`); lookup or role-resolution failures are returned unwrapped (`500`). `lookupUser` is `users.GetUserByID` in production.

> **`ClientService.CanAssignClientRoleAsRoot(requester, roleID)` / `ClientService.UpdateClientMemberRoleAsRoot(requester, clientID, targetUserID, newRoleID)`** - platform-root member path (`controllers/clients` `InviteUser`, `CreateClientMember`, `UpdateMemberRole`). Never assigns Owner (hierarchy 1) and never re-roles an Owner. The role must also pass `authz.PlatformRootMayGrantRole(requester, role.Code)`: the requester's root level must hold every capability the role carries, so Root 1 may grant any role below Owner and Root 2 only Check-in, Analyst, Member or Viewer (never Admin, Event manager or Editor). A requester that is not a platform root is denied. `GET /api/catalogs/roles` filters the platform-root catalog with the same predicate.

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
