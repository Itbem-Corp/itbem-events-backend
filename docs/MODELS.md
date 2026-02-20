# Models Index

> Update this file whenever you add, modify, or remove a model.

## JSON Tag Convention

All models use **snake_case** JSON tags on every field.  Sensitive/internal fields use `json:"-"` (e.g. `CognitoSub`, `IsRoot`, `Token` in InvitationAccessToken, all `DeletedAt`).  Relation fields use `json:"...,omitempty"`.

```go
// Example: correct tag pattern
ID        uuid.UUID      `gorm:"..." json:"id"`
CreatedAt time.Time      `json:"created_at"`
DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
Event     Event          `gorm:"foreignKey:EventID" json:"event,omitempty"`
```

## Core Domain Models

### Event
- **File**: `models/Event.go`
- **Purpose**: Main event entity
- **Relationships**: HasMany Guests, HasMany Invitations, HasMany Resources, BelongsTo Client, BelongsTo EventType

### EventType
- **File**: `models/EventType.go`
- **Purpose**: Catalog – type of event (wedding, corporate, etc.)
- **Relationships**: HasMany Events (seed data)

### EventConfig
- **File**: `models/EventConfig.go`
- **Purpose**: Per-event configuration (design settings, sections order, etc.)
- **Relationships**: BelongsTo Event

### EventSection
- **File**: `models/EventSection.go`
- **Purpose**: Configurable sections inside an event (header, gallery, schedule, etc.)
- **Relationships**: BelongsTo Event, HasMany Resources
- **SDUI fields** (Phase 2):
  - `component_type VARCHAR(50)` — tipo de sección React, ej: "GraduationHero", "CountdownHeader"
  - `config JSONB` — config específica de la sección (default: `{}`)

### EventAnalytics
- **File**: `models/EventAnalytics.go`
- **Purpose**: Tracks views, clicks, RSVP metrics per event
- **Relationships**: BelongsTo Event

### EventMember
- **File**: `models/EventMember.go`
- **Purpose**: Staff/collaborators assigned to an event
- **Relationships**: BelongsTo Event, BelongsTo User

---

### Guest
- **File**: `models/Guest.go`
- **Purpose**: Event invitees
- **Relationships**: BelongsTo Event, HasMany Invitations, BelongsTo GuestStatus

### GuestStatus
- **File**: `models/GuestStatus.go`
- **Purpose**: Catalog – status of a guest (pending, confirmed, declined, etc.)
- **Relationships**: HasMany Guests (seed data)

### Invitation
- **File**: `models/Invitation.go`
- **Purpose**: Tracks invitation sent to each guest
- **Relationships**: BelongsTo Event, BelongsTo Guest, HasMany InvitationLogs

### InvitationLog
- **File**: `models/InvitationLog.go`
- **Purpose**: Audit log of invitation status changes
- **Relationships**: BelongsTo Invitation

### InvitationAccessToken
- **File**: `models/InvitationAccessToken.go`
- **Purpose**: Public token allowing guests to view/RSVP without login
- **Relationships**: BelongsTo Invitation

---

### Client
- **File**: `models/Client.go`
- **Purpose**: Multi-tenant organization
- **Key Fields**: ParentClientID (hierarchy)
- **Relationships**: HasMany Events, BelongsTo ParentClient, BelongsTo ClientType

### ClientType
- **File**: `models/ClientType.go`
- **Purpose**: Catalog – type of client (agency, direct brand, etc.)
- **Relationships**: HasMany Clients (seed data)

### ClientRole
- **File**: `models/ClientRole.go`
- **Purpose**: Roles within a client (admin, editor, viewer, etc.)
- **Relationships**: HasMany ClientMembers (seed data)

---

### User
- **File**: `models/User.go`
- **Purpose**: System users, linked to AWS Cognito
- **Key Fields**: CognitoSub, Email
- **Relationships**: HasMany ClientMembers

---

### Resource
- **File**: `models/Resource.go`
- **Purpose**: Tracks files uploaded to S3
- **Relationships**: BelongsTo ResourceType, polymorphic owner

### ResourceType
- **File**: `models/ResourceType.go`
- **Purpose**: Catalog – type of resource (image, video, font, pdf)
- **Relationships**: HasMany Resources (seed data)

---

### DesignTemplate
- **File**: `models/DesignTemplate.go`
- **Purpose**: Pre-built design templates for events
- **Relationships**: BelongsTo ColorPalette, BelongsTo FontSet

### Moment
- **File**: `models/Moment.go`
- **Purpose**: Timeline moments/segments within an event section
- **Relationships**: BelongsTo EventSection, BelongsTo MomentType

### MomentType
- **File**: `models/MomentType.go`
- **Purpose**: Catalog – type of moment (ceremony, reception, dinner, etc.)
- **Relationships**: HasMany Moments (seed data)

---

### Color
- **File**: `models/Color.go`
- **Purpose**: Individual color definitions (hex/rgb)

### ColorPalette
- **File**: `models/ColorPalette.go`
- **Purpose**: Curated color palettes for design templates
- **Relationships**: HasMany ColorPalettePatterns

### ColorPalettePattern
- **File**: `models/ColorPalettePattern.go`
- **Purpose**: Junction – assigns Colors to a ColorPalette with role (primary, secondary, accent)
- **Relationships**: BelongsTo ColorPalette, BelongsTo Color

---

### Font
- **File**: `models/Font.go`
- **Purpose**: Individual font definitions
- **Relationships**: HasMany FontSetPatterns

### FontSet
- **File**: `models/FontSet.go`
- **Purpose**: Curated font pairings for design templates
- **Relationships**: HasMany FontSetPatterns

### FontSetPattern
- **File**: `models/FontSetPattern.go`
- **Purpose**: Junction – assigns Fonts to a FontSet with role (heading, body, accent)
- **Relationships**: BelongsTo FontSet, BelongsTo Font

---

### Config
- **File**: `models/environmentVariables.go`
- **Purpose**: Environment configuration struct (NOT a DB model)
- **Note**: Loaded from env vars, injected into Echo context on protected routes

---

## Model Conventions

- UUID: `github.com/gofrs/uuid` — **NOT google/uuid**
- PK: `ID uuid.UUID \`gorm:"type:uuid;primaryKey;default:gen_random_uuid()"\``
- Timestamps: `CreatedAt time.Time`, `UpdatedAt time.Time`
- Soft delete: add `DeletedAt gorm.DeletedAt` only if needed
- FK pattern: `<ModelName>ID uuid.UUID`

## Registration (`configuration/gorm.go`)

- `modelsWithoutSeed` — regular models
- `modelSeedList` — models with seed data (EventType, GuestStatus, MomentType, ResourceType, ClientType, ClientRole)

## Seeds (`seeds/`)

| Seed File | Model |
|-----------|-------|
| `SeedEventType.go` | EventType |
| `SeedGuestStatus.go` | GuestStatus |
| `SeedMomentType.go` | MomentType |
| `SeedSourceType.go` | ResourceType |
| `ClientTypeSeed.go` | ClientType |
| `ClientRoleSeed.go` | ClientRole |
| `SeedClientEventiAppSeed.go` | Client (default org) |
