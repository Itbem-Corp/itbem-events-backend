# Code Index — Fast File Lookup

> Check this file BEFORE using Glob/Grep. Update when adding new files.
> **Module name**: `events-stocks` (see go.mod)

## Directory Structure

```
itbem-events-backend/
├── cmd/api/              # API entrypoint
├── internal/app/         # Composition root, server setup, dependency wiring
├── internal/authz/       # Shared authorization helpers for protected handlers
├── configuration/        # DB, Redis, CORS, env setup
├── controllers/          # HTTP handlers (one dir per domain)
├── middleware/           # Echo middleware (token, redis)
├── models/              # GORM models + Config struct
├── repositories/        # Data access (one dir per domain)
├── routes/              # Route registration
├── seeds/               # DB seed data
├── services/            # Business logic (one dir per domain)
├── utils/               # Helpers, response, redis keys
├── docs/                # Documentation
└── go.mod               # Dependencies (module: events-stocks)
```

## Core Files

| File | Purpose |
|------|---------|
| `cmd/api/main.go` | Small executable entrypoint |
| `internal/app/app.go` | startup, middleware, dependency wiring, graceful shutdown |
| `internal/authz/authz.go` | Shared user/event/client/resource authorization helpers |
| `routes/routes.go` | All API routes — `ConfigurarRutas` |
| `configuration/gorm.go` | GORM setup, auto-migrate, model registration |
| `configuration/environmentVariables.go` | Env var loading into Config |
| `configuration/cors.go` | CORS settings |
| `configuration/redis.go` | Redis connection |

## Models (`models/*.go`)

| Model | File |
|-------|------|
| Event | `models/Event.go` |
| EventType | `models/EventType.go` |
| EventConfig | `models/EventConfig.go` |
| EventSection | `models/EventSection.go` |
| EventAnalytics | `models/EventAnalytics.go` |
| EventMember | `models/EventMember.go` |
| Guest | `models/Guest.go` |
| GuestStatus | `models/GuestStatus.go` |
| Invitation | `models/Invitation.go` |
| InvitationLog | `models/InvitationLog.go` |
| InvitationAccessToken | `models/InvitationAccessToken.go` |
| Client | `models/Client.go` |
| ClientType | `models/ClientType.go` |
| ClientRole | `models/ClientRole.go` |
| User | `models/User.go` |
| Resource | `models/Resource.go` |
| ResourceType | `models/ResourceType.go` |
| DesignTemplate | `models/DesignTemplate.go` |
| DeliveryProject / work-item workflow | `models/DeliveryWorkflow.go` |
| AutomationTask | `models/AutomationTask.go` |
| Moment | `models/Moment.go` |
| MomentType | `models/MomentType.go` |
| Color | `models/Color.go` |
| ColorPalette | `models/ColorPalette.go` |
| ColorPalettePattern | `models/ColorPalettePattern.go` |
| Font | `models/Font.go` |
| FontSet | `models/FontSet.go` |
| FontSetPattern | `models/FontSetPattern.go` |
| Config (env, not DB) | `models/environmentVariables.go` |

## Controllers (`controllers/<domain>/<domain>.go`)

| Domain | File | Routes |
|--------|------|--------|
| events | `controllers/events/events.go` | `/api/events*` |
| guests | `controllers/guests/guests.go` | `/api/guests*` |
| invitations | `controllers/invitations/invitations.go` | `/api/invitations*` |
| resources | `controllers/resources/resources.go` | `/api/resources*` |
| fonts | `controllers/fonts/fonts.go` | `/api/fonts*` |
| clients | `controllers/clients/clients.go` | `/api/clients*` |
| users | `controllers/users/users.go` | `/api/users*` |
| clienttypes | `controllers/clienttypes/clientTypes.go` | `/api/catalogs/client-types` |
| clientroles | `controllers/clientroles/clientRoles.go` | `/api/catalogs/roles` |
| cache | `controllers/cache/cache.go` | `/api/cache*` |
| delivery | `controllers/delivery/delivery.go`, `controllers/delivery/agent_runs.go` | `/api/automation/projects*`, `/api/automation/work-items*` |

## Services (`services/<domain>/`)

| File | Purpose |
|------|---------|
| `services/events/EventService.go` | Event CRUD |
| `services/events/EventTypeService.go` | EventType catalog |
| `services/events/EventConfigService.go` | Event config |
| `services/events/EventSectionService.go` | Event sections |
| `services/events/EventAnalyticsService.go` | Analytics |
| `services/events/PageSpecService.go` | Public SDUI page specs |
| `services/events/RepairService.go` | Event integrity repair |
| `services/guests/GuestService.go` | Guest CRUD |
| `services/guests/GuestStatusService.go` | GuestStatus catalog |
| `services/invitations/InvitationService.go` | Invitation + RSVP |
| `services/clients/clientService.go` | Client CRUD + hierarchy |
| `services/clientroles/clientRoleService.go` | ClientRole catalog |
| `services/clienttypes/clientTypeService.go` | ClientType catalog |
| `services/users/UserService.go` | User profile |
| `services/users/AdminUserService.go` | Admin user ops |
| `services/resources/Resources.go` | Resource CRUD, upload orchestration, and cache invalidation |
| `services/resources/ResourceObjectStorage.go` | Presigned URLs and object deletion boundary |
| `services/resources/ResourceTypes.go` | Resource types |
| `services/resources/ImageOptimizer.go` | libvips pipeline |
| `services/resources/ImageOptimizer_fallback.go` | Windows/no-cgo optimizer fallback |
| `services/resources/ResourcesUsers.go` | User resources (avatar) |
| `services/resources/ResourcesClients.go` | Client resources (logo) |
| `services/templates/DesignTemplateService.go` | Design templates |
| `services/moments/MomentService.go` | Moment CRUD |
| `services/moments/MomentTypeService.go` | MomentType catalog |
| `services/colors/ColorService.go` | Colors |
| `services/colors/ColorPaletteService.go` | Color palettes |
| `services/colors/ColorPalettePatternService.go` | Palette patterns |
| `services/fonts/FontService.go` | Fonts |
| `services/fonts/FontSetService.go` | Font sets |
| `services/fonts/FontSetPatternService.go` | FontSet patterns |
| `services/cacheutil/json_cache.go` | Generic JSON cache-aside helper |
| `services/deliveryworkflow/workflow.go` | Pure, human-gated delivery state machine |
| `services/notifications/SlackNotificationService.go` | Durable producer for `notification.slack` worker jobs |
| `services/validations/Validations.go` | Shared validators |

## Repositories (`repositories/<domain>repository/`)

| Path | Purpose |
|------|---------|
| `repositories/gormrepository/GormRepository.go` | Generic CRUD |
| `repositories/gormrepository/QueryOptions.go` | Query helpers |
| `repositories/redisrepository/RedisRepository.go` | Cache ops |
| `repositories/awsrepository/S3Repository.go` | S3 ops |
| `repositories/awsrepository/CognitoRepository.go` | Cognito admin |
| `repositories/bucketrepository/BucketRepository.go` | Bucket/URL helpers |
| `repositories/sqsrepository/SQSRepository.go` | Async media job publisher |
| `repositories/authproviderrepository/AuthProviderRepository.go` | Token validation |
| `repositories/eventsrepository/EventsRepository.go` | Events DB |
| `repositories/eventconfigrepository/EventConfigRepository.go` | EventConfig DB |
| `repositories/eventsectionrepository/EventSectionRepository.go` | EventSection DB |
| `repositories/eventtyperepository/EventTypeRepository.go` | EventType DB |
| `repositories/eventanalyticsrepository/EventAnalyticsRepository.go` | Analytics DB |
| `repositories/guestrepository/GuestRepository.go` | Guest DB |
| `repositories/gueststatusrepository/GuestStatusRepository.go` | GuestStatus DB |
| `repositories/invitationrepository/InvitationRepository.go` | Invitation DB |
| `repositories/invitationlogrepository/InvitationLogRepository.go` | Invitation log DB |
| `repositories/invitationaccesstokenrepository/InvitationAccessTokenRepository.go` | Access token DB |
| `repositories/clientrepository/clientRepository.go` | Client DB |
| `repositories/clienttyperepository/clientTypeRepository.go` | ClientType DB |
| `repositories/clientrolerepository/clientRoleRepository.go` | ClientRole DB |
| `repositories/userrepository/UserRepository.go` | User DB |
| `repositories/userrepository/AdminUserRepository.go` | Admin user DB |
| `repositories/resourcerepository/ResourceRepository.go` | Resource DB |
| `repositories/resourcerepository/ResourceTypeRepository.go` | ResourceType DB |
| `repositories/templatesrepository/TemplateRepository.go` | DesignTemplate DB |
| `repositories/momentrepository/MomentRepository.go` | Moment DB |
| `repositories/momenttyperepository/MomentTypeRepository.go` | MomentType DB |
| `repositories/colorrepository/ColorRepository.go` | Color DB |
| `repositories/colorrepository/PaletteRepository.go` | ColorPalette DB |
| `repositories/colorrepository/PatternRepository.go` | ColorPalettePattern DB |
| `repositories/fontrepository/FontRepository.go` | Font DB |
| `repositories/fontrepository/FontSetRepository.go` | FontSet DB |
| `repositories/fontrepository/PatternRepository.go` | FontSetPattern DB |

## Middleware

| File | Purpose |
|------|---------|
| `middleware/token/token_middleware.go` | JWT validation (Cognito) |
| `middleware/redis/redis_middleware.go` | Auto cache loading |

## Seeds (`seeds/`)

| File | Seeds |
|------|-------|
| `seeds/SeedEventType.go` | EventType |
| `seeds/SeedGuestStatus.go` | GuestStatus |
| `seeds/SeedMomentType.go` | MomentType |
| `seeds/SeedSourceType.go` | ResourceType |
| `seeds/ClientTypeSeed.go` | ClientType |
| `seeds/ClientRoleSeed.go` | ClientRole |
| `seeds/SeedClientEventiAppSeed.go` | Default client org |

## Utils (`utils/`)

| File | Purpose |
|------|---------|
| `utils/response.go` | `Success()`, `Error()` helpers |
| `utils/redisKeys.go` | Cache key generation |
| `utils/helpers.go` | Common utility functions |
| `utils/marshall.go` | JSON marshal/unmarshal helpers |

## DTOs

| File | Purpose |
|------|---------|
| `dtos/AuthUser.go` | Auth provider user DTO |
| `dtos/PageSpec.go` | SDUI page spec response |
| `dtos/Resource.go` | Resource response DTOs |
| `dtos/MediaProcessMessage.go` | Async media processing job payload |

## Config Files

| File | Purpose |
|------|---------|
| `go.mod` | Module: `events-stocks` |
| `.env` | Local env vars (gitignored) |
| `Dockerfile` | Container image |
| `.github/workflows/deploy-backend.yml` | CI/CD pipeline |
| `CLAUDE.md` | Claude Code instructions |

## Quick Lookup

| Question | Answer |
|----------|--------|
| Event model? | `models/Event.go` |
| EventService? | `services/events/EventService.go` |
| Routes? | `routes/routes.go` |
| JWT auth? | `middleware/token/token_middleware.go` |
| Redis? | `repositories/redisrepository/RedisRepository.go` |
| DB config? | `configuration/gorm.go` |
| S3 upload? | `repositories/awsrepository/S3Repository.go` |
| Image resize? | `services/resources/ImageOptimizer.go` |

---
**Last Updated**: 2026-07-04
