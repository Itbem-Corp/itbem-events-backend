# Repositories Index

> Update this file whenever you add, modify, or remove repositories.
> For code patterns, see `docs/TEMPLATES.md`.

## Repository Layer Purpose

- Direct database operations via GORM
- Redis cache operations
- AWS S3 / Cognito operations
- Generic CRUD via `gormrepository`

## Generic / Infrastructure Repositories

| Path | Purpose |
|------|---------|
| `repositories/gormrepository/GormRepository.go` | Generic CRUD: `Insert`, `Update`, `Delete`, `FindByID`, `FindAll` |
| `repositories/gormrepository/QueryOptions.go` | Query builder helpers (filters, pagination, preloads) |
| `repositories/redisrepository/RedisRepository.go` | `Get`, `Set`, `Invalidate`, `FlushAll` |
| `repositories/awsrepository/S3Repository.go` | S3 upload, delete, presigned URLs |
| `repositories/awsrepository/CognitoRepository.go` | Cognito user admin operations |
| `repositories/bucketrepository/BucketRepository.go` | Bucket-level operations, URL helpers |
| `repositories/authproviderrepository/AuthProviderRepository.go` | Auth provider token validation |
| `repositories/automationqueuerepository/AutomationQueueRepository.go` | Fail-closed local-agent SQS publication, atomic role-lane routing, and bounded queue/DLQ health |

## Domain Repositories

### Events

| Path | Purpose |
|------|---------|
| `repositories/eventsrepository/EventsRepository.go` | Event CRUD and dashboard lists; `dashboardEventsScope` selects one organization, the caller-access portfolio (`callerAccessEventPredicate`, mirrors `SessionService.organizationAccess`) or all events for roots |
| `repositories/eventsrepository/errorHandler.go` | Domain-specific error mapping |
| `repositories/eventconfigrepository/EventConfigRepository.go` | EventConfig CRUD |
| `repositories/eventsectionrepository/EventSectionRepository.go` | EventSection CRUD |
| `repositories/eventtyperepository/EventTypeRepository.go` | EventType catalog queries |
| `repositories/eventanalyticsrepository/EventAnalyticsRepository.go` | Analytics upsert/aggregate |
| `repositories/eventsrepository/event_client_scope.go` | `ConfigureEventClientScope` and `withEventClientGrants`: ORs active event-client grants into user-scoped event lists while `EVENT_CLIENT_ACCESS` is on (same predicates as authorization) |
| `repositories/eventclientrepository/EventClientRepository.go` | Event-client grants: `HasActiveGrant`, counts, `ActiveGrantedEventIDs`, per-event and per-organization lists, candidates, assignments page, `FindLiveUsersByEmail`, `HasMembershipInOrganizationTree`, `Grant` (revokes stale rows of a previous organization, then `ON CONFLICT DO NOTHING` on the active index), `Revoke`, `RevokeAllForEvent`, `RevokeAllForOrganizationUser`. Active = not revoked, event not deleted and still in the grant organization, organization active, holder without any `client_members` row there |
| `repositories/eventmemberrepository/EventMemberRepository.go` | Event assignments; `RolesForUser` batches roles for many events |
| `repositories/eventtablerepository/EventTableRepository.go` | Seating tables (physical table `tables`): CRUD, `AssignGuestsToTables`, `SaveSeatingPlan`. `CountEventTablesInEvent(eventID, ids)` counts the live (not soft-deleted) tables of the event among `ids` in one query; guest create/update compare it with the number of distinct ids. |

### Guests & Invitations

| Path | Purpose |
|------|---------|
| `repositories/guestrepository/GuestRepository.go` | Guest CRUD + bulk ops. `TransitionGuestStatuses(eventID, ids, apply)` locks the event's guests (`FOR UPDATE`, id order), fails when an id is not a live guest of the event, attaches their catalog status, lets `apply` pick new values and writes back only the RSVP and door check-in columns, in one transaction. |
| `repositories/gueststatusrepository/GuestStatusRepository.go` | GuestStatus catalog |
| `repositories/invitationrepository/InvitationRepository.go` | Invitation CRUD |
| `repositories/invitationlogrepository/InvitationLogRepository.go` | Log insertion |
| `repositories/invitationaccesstokenrepository/InvitationAccessTokenRepository.go` | Token create/validate |

### Clients & Users

| Path | Purpose |
|------|---------|
| `repositories/clientrepository/clientRepository.go` | Client CRUD + hierarchy |
| `repositories/clientrepository/access_lookup.go` | `ResolveClientProduct` (nearest branded ancestor's application code) |
| `repositories/clientrepository/access_checks.go` | Authorization lookups: `CheckAccessRecursiveE` (error-returning organization role lookup, 32-level bound; `CheckAccessRecursive` wraps it and swallows errors for legacy service callers) and `CheckApplicationAccessRecursive` (same lookup plus the product entitlement in one CTE, nearest-branded-ancestor rule, `Reason` for the first failed condition). Opt-in Postgres tests: `CLIENT_ACCESS_TEST_DSN`. |
| `repositories/clienttyperepository/clientTypeRepository.go` | ClientType catalog |
| `repositories/clientrolerepository/clientRoleRepository.go` | ClientRole catalog |
| `repositories/userrepository/UserRepository.go` | User CRUD, Cognito sync |
| `repositories/userrepository/AdminUserRepository.go` | Admin-level user queries |

### Resources & Files

| Path | Purpose |
|------|---------|
| `repositories/resourcerepository/ResourceRepository.go` | Resource CRUD |
| `repositories/resourcerepository/ResourceTypeRepository.go` | ResourceType catalog |
| `repositories/resourcerepository/errorResourceHandler.go` | Resource error mapping |

### Design & Themes

| Path | Purpose |
|------|---------|
| `repositories/templatesrepository/TemplateRepository.go` | DesignTemplate CRUD |
| `repositories/momentrepository/MomentRepository.go` | Moment CRUD |
| `repositories/momenttyperepository/MomentTypeRepository.go` | MomentType catalog |

### Colors

| Path | Purpose |
|------|---------|
| `repositories/colorrepository/ColorRepository.go` | Color CRUD |
| `repositories/colorrepository/PaletteRepository.go` | ColorPalette CRUD |
| `repositories/colorrepository/PatternRepository.go` | ColorPalettePattern CRUD |
| `repositories/colorrepository/errorColorHandler.go` | Color error mapping |

### Fonts

| Path | Purpose |
|------|---------|
| `repositories/fontrepository/FontRepository.go` | Font CRUD |
| `repositories/fontrepository/FontSetRepository.go` | FontSet CRUD |
| `repositories/fontrepository/PatternRepository.go` | FontSetPattern CRUD |
| `repositories/fontrepository/errorFontHandler.go` | Font error mapping |

## Key Repository Functions (Generic)

```go
// gormrepository
gormrepository.Insert(obj)
gormrepository.Update(id, obj)
gormrepository.Delete(id, &models.Model{})
gormrepository.GetByID(&obj, id)
gormrepository.GetList(&objs, opts)
gormrepository.InsertMany(objs)
```

## Custom Query Pattern

```go
func GetByClientID(clientID uuid.UUID) ([]models.Entity, error) {
    var items []models.Entity
    err := configuration.DB.Where("client_id = ?", clientID).
        Order("created_at DESC").
        Find(&items).Error
    return items, err
}
```

## Transaction Pattern

Use `configuration.DB.Transaction()` when multiple inserts must be atomic (all succeed or all rollback):

```go
err := configuration.DB.Transaction(func(tx *gorm.DB) error {
    if err := tx.Create(&invitations).Error; err != nil {
        return err
    }
    if err := tx.Create(&guests).Error; err != nil {
        return err
    }
    return nil
})
```

See `services/guests/GuestService.go → CreateGuestsWithInvitations` for a complete example.

## Thread-Safe Package-Level Caching

Use `sync.Once` for package-level values that should be loaded from DB once:

```go
var (
    cachedID   uuid.UUID
    cachedOnce sync.Once
)

func GetCachedID() uuid.UUID {
    cachedOnce.Do(func() {
        record, err := someRepo.GetByCode("code")
        if err != nil || record == nil { return }
        cachedID = record.ID
    })
    return cachedID
}
```

See `repositories/guestrepository/GuestRepository.go → GetPendingStatusID`.

## File Location Pattern

```
repositories/<domain>repository/<Domain>Repository.go
```
