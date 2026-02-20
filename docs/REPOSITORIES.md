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
| `repositories/cacheloaderrepository/CacheLoaderRepository.go` | Auto cache loading for Redis middleware |
| `repositories/awsrepository/S3Repository.go` | S3 upload, delete, presigned URLs |
| `repositories/awsrepository/CognitoRepository.go` | Cognito user admin operations |
| `repositories/bucketrepository/BucketRepository.go` | Bucket-level operations, URL helpers |
| `repositories/authproviderrepository/AuthProviderRepository.go` | Auth provider token validation |

## Domain Repositories

### Events

| Path | Purpose |
|------|---------|
| `repositories/eventsrepository/EventsRepository.go` | Event CRUD |
| `repositories/eventsrepository/errorHandler.go` | Domain-specific error mapping |
| `repositories/eventconfigrepository/EventConfigRepository.go` | EventConfig CRUD |
| `repositories/eventsectionrepository/EventSectionRepository.go` | EventSection CRUD |
| `repositories/eventtyperepository/EventTypeRepository.go` | EventType catalog queries |
| `repositories/eventanalyticsrepository/EventAnalyticsRepository.go` | Analytics upsert/aggregate |

### Guests & Invitations

| Path | Purpose |
|------|---------|
| `repositories/guestrepository/GuestRepository.go` | Guest CRUD + bulk ops |
| `repositories/gueststatusrepository/GuestStatusRepository.go` | GuestStatus catalog |
| `repositories/invitationrepository/InvitationRepository.go` | Invitation CRUD |
| `repositories/invitationlogrepository/InvitationLogRepository.go` | Log insertion |
| `repositories/invitationaccesstokenrepository/InvitationAccessTokenRepository.go` | Token create/validate |

### Clients & Users

| Path | Purpose |
|------|---------|
| `repositories/clientrepository/clientRepository.go` | Client CRUD + hierarchy |
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
