# Models Index

> Update this file whenever you add, modify, or remove a model.

## JSON Tag Convention

All models use **snake_case** JSON tags on every field.  Sensitive/internal fields use `json:"-"` (e.g. `CognitoSub`, `Token` in InvitationAccessToken, all `DeletedAt`).  Relation fields use `json:"...,omitempty"`.

**Note:** `IsRoot` on User is **intentionally exposed** as `json:"is_root"` — the dashboard needs it to control sidebar visibility and super-admin access.

```go
// Example: correct tag pattern
ID        uuid.UUID      `gorm:"..." json:"id"`
CreatedAt time.Time      `json:"created_at"`
DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
Event     Event          `gorm:"foreignKey:EventID" json:"event,omitempty"`
```

## Async cover and public experience aggregates

- `Event` keeps the last completed `cover_image_url` and `cover_variants` public while a replacement is represented by `cover_pending_url`, `cover_processing_status`, `cover_processing_job_id`, and a monotonic `cover_processing_generation`.
- Only the matching job/generation can advance `pending -> processing -> done|failed`; failure never removes the last completed cover.
- `PublicPerformanceWindowBucket` is a global five-minute histogram keyed by window, route, metric, and fixed bucket. It contains no event, visitor, credential, IP, user-agent, or URL identifier and is pruned after 48 hours.

## Core Domain Models

### Event
- **File**: `models/Event.go`
- **Purpose**: Main event entity
- **Key fields**: `ClientID *uuid.UUID` (nullable, FK to Client — added Phase 1), `client_id` JSON tag, `Identifier string` (unique slug, auto-generated from Name if empty on create)
- **Relationships**: HasMany Guests, HasMany Invitations, HasMany Resources, BelongsTo Client (optional), BelongsTo EventType

### EventType
- **File**: `models/EventType.go`
- **Purpose**: Catalog – type of event (wedding, corporate, etc.)
- **Relationships**: HasMany Events (seed data)

### EventConfig
- **File**: `models/EventConfig.go`
- **Purpose**: Per-event configuration (design settings, sections order, MomentWall gating)
- **Key fields**: `show_moment_wall bool` (gates public visibility and closes guest uploads when published), `visibility_configured bool` (marks section toggles as explicit so all-false visibility is preserved while legacy all-false configs still receive defaults), `allow_uploads bool` (global uploads gate), `share_uploads_enabled bool` (enables QR-code upload page without personal token when `allow_uploads` is also true and `show_moment_wall` is false), `allow_messages bool`, `active_from time.Time` and `active_until *time.Time` (public page availability window; `active_until` must be after `active_from` when both are set), `max_uploads_per_guest int` (per-IP upload limit, default 30; set to 0 for global default), `auto_approve_uploads bool` (auto-approves all incoming moments, default false), `design_template_id *uuid.UUID` (nullable FK to DesignTemplate)
- **Relationships**: BelongsTo Event, BelongsTo DesignTemplate (optional — nullable FK)
- **Auto-create**: `GetEventConfigByID` auto-creates a default config if none exists for the event

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

### Delivery workflow (ITBEM only)

- **Files**: `models/DeliveryWorkflow.go`, `models/AutomationTask.go`
- **Purpose**: Private, human-gated software delivery orchestration. It is not
  an EventiApp feature and remains behind the ITBEM automation surface.
- **DeliveryProject**: Belongs to one Client and owns context sources and work
  items. It stores only references to private repositories, documents,
  conversations and decisions.
- **DeliveryContextSource**: Versioned source descriptor (`kind`, `reference`,
  `revision`, freshness state). A work item records immutable snapshots of the
  source identity (`kind`, `name`) and exact reference/revision used by the
  agent, along with bounded context metadata such as a client-conversation
  excerpt or decision. Later edits or removal cannot silently alter approved
  context.
- **DeliveryRepositoryOnboarding** (`models/DeliveryProjectVault.go`): Mutable,
  human-reviewed static onboarding proposal pinned to one GitHub default-branch
  SHA. It stores structured proposal/capability JSON, never credentials or raw
  repository prose, and is unique by project/repository/revision.
- **DeliveryProjectVaultRevision** (`models/DeliveryProjectVault.go`): Immutable,
  repository-scoped curated Vault manifest with monotonic version, source SHA,
  content digest, provenance and onboarding identity. Application hooks and a
  PostgreSQL trigger reject update/delete; reconciliation appends a revision.
- **DeliveryWorkItem**: Bounded request with outcome, included/excluded scope,
  acceptance criteria, generated plan, pull request and preview references.
  Its lifecycle is enforced by `services/deliveryworkflow`; human gates are
  required before implementation, QA and release transitions.
- **DeliveryGate**: Append-only human decision for `plan`, `code_review`,
  `qa_review` or `release`. Comments and evidence checklist are retained with
  the approver and timestamp.
- **DeliveryEvidence**: Reference to a private screenshot, test result, diff,
  report or other artifact. Large payloads never live in Postgres.
- **DeliveryMessage**: Human or agent message tied to the work item and the
  active phase, preserving change requests in context.
- **AutomationTask**: Existing local-agent queue record. It may link to a
  `DeliveryWorkItem`; standalone legacy automation tasks remain valid. A
  completed delivery task is recorded as immutable private report evidence
  before its plan, code, or QA review can be opened. Evidence-producing tasks
  carry an optional `EvidenceSubjectDigest` fixed at enqueue time; QA uses it
  to bind observations to the exact multi-repository release matrix without
  granting the task merge or release authority.
- **DeliveryEvent** QA observation (`delivery.qa.observed.v2`): Append-only,
  sequence-bearing result for one exact QA task and matrix digest. It stores
  only repository execution order plus operator-owned test identities and
  bounded validation/QA pass/fail facts; commands, output, URLs, screenshots
  and model prose remain private objects. The release resolver accepts only a
  completed matching task, exact workspace-to-publication/worktree mapping,
  and each repository's effective required-test policy.
- **DeliveryEvent** security observation (`delivery.security.observed.v1`):
  Append-only projection of operator-configured local scanner results from a
  completed exact-matrix QA task. It stores reviewed workspace/branch identity,
  bounded high/critical counts, and the secret-scan result; command text,
  output, findings, tokens, and model prose never enter the public ledger.
  Resolution maps that identity through the consumed publication grant to each
  exact remote repository/SHA immediately before Gatekeeper evaluation.
  Reserved `assurance:compatibility` and `assurance:migrations` QA identities
  are resolved from the same event into separate exact-matrix Gatekeeper
  fields; missing per-repository commands remain missing evidence.
  Dependency assurance combines its repository execution order with frozen
  `DeliveryContextSnapshot` dependency edges and current released states from
  `DeliveryWorkItemDependency`; no worker-provided dependency verdict is used.
  Recovery evidence is reconstructed from every effective repository policy;
  the most constrained classification becomes the exact-matrix composite and
  irreversible recovery still requires a distinct exact-subject human grant.
- **AutomationAgentHeartbeat** (`models/AutomationAgentHeartbeat.go`): Short-
  lived, anonymized execution-plane presence. It records the declared worker
  role/lane, provider/model, concurrency and bounded workspace readiness, but
  never a hostname, queue URL, prompt, result, repository path or credential.
  Empty role/lane identifies only the temporary combined worker during queue
  migration.

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
- **Key fields**: `color_palette_id *uuid.UUID` (nullable FK), `font_set_id *uuid.UUID` (nullable FK), `identifier string` (unique, e.g. "classic-elegant"), `is_active bool`, `is_premium bool`, `category string`
- **JSON tags**: All fields use snake_case (`preview_url`, `color_palette_id`, `animations_enabled`, etc.)
- **Relationships**: BelongsTo ColorPalette (optional), BelongsTo FontSet (optional)

### Moment
- **File**: `models/Moment.go`
- **Purpose**: Guest-uploaded photo/video/note for the event MomentWall
- **JSON tags**: All fields use snake_case. `DeletedAt` uses `json:"-"`. Nullable FKs and relations use `omitempty`.
- **Key fields**:
  - `event_id *uuid.UUID` — FK to Event (nullable)
  - `invitation_id *uuid.UUID` — FK to Invitation (nullable — NULL for shared QR uploads)
  - `content_url string` — S3 key of the file. Initially the raw path (`moments/{eventID}/raw/{uuid}.ext`); updated to optimized path (`moments/{eventID}/{uuid}.webp|.mp4`) once Lambda completes.
  - `content_type string` — original MIME type from the upload (e.g. `image/jpeg`, `video/mp4`). Used by RequeueMoment to re-publish correct content type to SQS.
  - `description string` — guest message/note left with the upload
  - `is_approved bool` — admin approve/reject flag (default false)
  - `processing_status string` — async processing state: `""` (legacy/no SQS), `"pending"` (queued), `"processing"` (Lambda working), `"done"` (optimized), `"failed"` (Lambda error). When SQS is not configured, status stays `""` so moments are visible immediately.
  - `processing_duration_ms int64` — how long Lambda took to process (ms). Zero until `done`.
  - `original_size_bytes int64` — raw file size in bytes. Zero until `done`.
  - `optimized_size_bytes int64` — optimized file size in bytes. Zero until `done`.
  - `error_message string` — populated by Lambda when processing fails; empty string means no error or not yet processed.
  - `order int` — display order on wall
- **Dashboard behavior**: `GET /api/moments?event_id=X` only returns moments with `processing_status NOT IN ('pending','processing')` — admins see items only after Lambda finishes.
- **Public wall behavior**: Only shows `is_approved=true AND processing_status IN ('','done')` moments. Results cached in Redis (key: `moments:wall:{eventID}:p{N}:l{N}`, TTL 5min). Cache is busted on approval changes (including bulk), or Lambda callback.
- **Upload limits**: Per-IP upload limit per event controlled by `EventConfig.MaxUploadsPerGuest` (default 30 if unset). Redis counter: `moments:upload_count:{eventID}:{ip}`, 30-day TTL. After limit reached: 429 with personalized thank-you message.
- **S3 structure**: `moments/{eventID}/raw/{uuid}.ext` (raw) → `moments/{eventID}/{uuid}.webp|.mp4` (optimized)
- **SQS behavior**: When SQS queues are not configured (local dev without `SQS_IMAGE_QUEUE_URL`/`SQS_VIDEO_QUEUE_URL`), public uploads set `processing_status=""` instead of `"pending"`, making moments visible immediately without Lambda processing.
- **Relationships**: BelongsTo Event, BelongsTo Invitation (optional)

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
