# Routes Index

> Update this file whenever you add, modify, or remove routes.
> Source of truth: `routes/routes.go` — function `ConfigurarRutas`

## Route Structure

- **Public** (`/api`) - no auth, public body/rate limits, explicit service-level cache where needed
- **Protected** (`/api`) - token auth, dashboard body/rate limits, explicit service-level cache where needed

## Health Check

- `GET /health` → `controllers/health.Check` — DB ping + Redis ping, returns `200 ok` or `503 degraded`; `data.environment` is normalized to `production`, `staging`, a local/dev label, or `unknown` so deployment gates can reject cross-environment routing.

---

## Public Routes (`/api`)

### Events
- `GET /api/events/page-spec?token=...` → `events.GetPageSpec` — SDUI: PageSpec por token de invitacion. Includes `access` object with `activeFrom`, `activeUntil`, `passwordProtected`, `accessVersion`.
- `GET /api/events/:identifier/page-spec` → `events.GetPageSpecByIdentifier` — SDUI: PageSpec por slug publico, invitacion (`?token=...`) o Studio preview (`?preview_token=...`). Query aliases accepted by backend: preview (`preview_token`, `previewToken`, `PreviewToken`), invitation (`token`, `Token`, `invitation_token`, `invitationToken`, `InvitationToken`, `pretty_token`, `prettyToken`, `PrettyToken`), and password proof (`event_access_token`, `eventAccessToken`, `EventAccessToken`). When the signed preview token is valid, `access.previewAuthorized=true`; Cafetton must only bypass date/password gates and tracking when this backend flag is true.
- `GET /api/events/:identifier/meta` → `events.GetEventMeta` — Safe metadata shape for OG/SSR, including the canonical `identifier`. Anonymous public-event requests are cacheable by default; requests scoped by preview, invitation, `event_access_token`, or `X-Event-Access-Token` are `no-store`. Password-protected events require the same access proof rules as other protected public content.
- `POST /api/events/:identifier/view` → `events.TrackView` — Fire-and-forget view increment (no auth). Uses sessionStorage in client to deduplicate per session.
- `POST /api/events/:identifier/verify-access` → `events.VerifyEventAccess` — Password gate verification. Body: `{password: string}`. Returns 200 on match, 401 on wrong password.
- `GET /api/events/:key` → `events.GetEvents` — public single-event read only; `all` is protected

### Resources
- `GET /api/resources/:id` → `resources.GetResource` - Public resource detail/signing. If the parent event has `EventConfig.AuthPasswordPreview`, callers must send `X-Event-Access-Token` from `POST /api/events/:identifier/verify-access` unless `preview_token` is valid.
- `GET /api/resources/section/:key` → `resources.GetResourcesBySectionID` - Public section resources/signing. Same password proof requirement as resource detail.

### Attendees
- `GET /api/events/section/:sectionId/attendees` → `guests.GetAttendees` - Public attendee list for attendee-backed sections. If the parent event has `EventConfig.AuthPasswordPreview`, callers must send `X-Event-Access-Token` unless `preview_token` is valid.

### Invitations / RSVP
- `GET /api/invitations/ByToken?token=...` → `invitations.GetInvitationByToken` — recommended for URL-sensitive tokens. Query aliases accepted: `token`, `Token`, `invitation_token`, `invitationToken`, `InvitationToken`, `pretty_token`, `prettyToken`, `PrettyToken`. `GET /api/invitations/ByToken/:token` is still registered for simple legacy tokens.
- `POST /api/invitations/rsvp` → `invitations.ConfirmRSVP`

### Public Moments (MomentWall)
- `GET /api/events/:identifier/moments` → `moments.ListPublicMoments` — Paginated wall. Query: `?page=1&limit=20` (max 50). Returns only `is_approved=true AND processing_status IN ('','done')`. Response: `{ items, total, page, limit, has_more, published, uploads_remaining, uploads_used }`. Cached in Redis (`moments:wall:{eventID}:p{n}:l{n}`, TTL 5min). If `EventConfig.AuthPasswordPreview` is set, callers must send `X-Event-Access-Token` from `POST /api/events/:identifier/verify-access` unless `preview_token` is valid.
- `POST /api/events/:identifier/moments` → `moments.CreatePublicMoment` — Upload with personal `pretty_token`. Rate limited (sensitiveRateLimiter). Per-IP limit: controlled by `EventConfig.MaxUploadsPerGuest` (default 30) per event (Redis counter, 30-day window). Body limit: 225MB. Stores raw file to S3 `moments/{eventID}/raw/{uuid}.ext`, sets `processing_status="pending"`, queues SQS job synchronously. If `EventConfig.AutoApproveUploads=true`, moment is auto-approved on creation. Password-protected events also require `X-Event-Access-Token`.
- `POST /api/events/:identifier/moments/shared` → `moments.CreateSharedMoment` — Upload via QR code (no personal token). Requires `EventConfig.AllowUploads=true`, `EventConfig.ShareUploadsEnabled=true`, and `EventConfig.ShowMomentWall=false` (publishing the wall closes uploads). Same configurable per-IP limit, and async processing as personal upload. Respects `AutoApproveUploads`. Password-protected events also require `X-Event-Access-Token` for shared upload presign/confirm/multipart routes.
- `POST /api/events/:identifier/moments/upload-url` → `moments.RequestPublicMomentUploadURL` — Personal presigned PUT URL. Response includes `upload_url`, `object_key`, `s3_key`, `content_type`, and fresh upload quota fields (`uploads_limit`, `uploads_used`, `uploads_remaining`).
- `POST /api/events/:identifier/moments/shared/upload-url` → `moments.RequestSharedUploadURL` — Shared single-file presigned PUT URL with the same quota metadata.
- `POST /api/events/:identifier/moments/shared/batch-upload-urls` → `moments.RequestBatchSharedUploadURLs` — Shared batch presign, max 10 files. Response: `{ urls, uploads_limit, uploads_used, uploads_remaining }`.
- `POST /api/events/:identifier/moments/shared/multipart/start` → `moments.StartSharedMultipartUpload` — Shared multipart start. Response includes `upload_id`, `object_key`, `s3_key`, `part_urls`, `content_type`, and quota metadata.

---

## Protected Routes (`/api`) — Require `Authorization: Bearer <token>`

### ITBEM delivery control plane

- `GET|POST /api/automation/projects` → `delivery.ListProjects` / `delivery.CreateProject`
- `GET /api/automation/projects/:id` → `delivery.GetProject`
- `POST /api/automation/projects/:id/context` → `delivery.CreateContext`
- `POST /api/automation/projects/:id/work-items` → `delivery.CreateWorkItem`
- `GET /api/automation/work-items/:id` → `delivery.GetWorkItem`
- `GET /api/automation/work-items/:id/stream` → `delivery.StreamWorkItem` — authenticated SSE invalidation feed for live execution maps. It emits a `snapshot` on connect, then `update` only when the database-backed work-item revision changes. Its bounded payload is `{ work_item_id, revision, state, active_tasks, last_activity_at, generated_at }`; it never includes prompts, private object references, provider payloads, or task output. Connections refresh authorization after 55 seconds and clients reconnect using the SSE retry directive.
- `POST /api/automation/work-items/:id/transitions` → `delivery.TransitionWorkItem`
- `POST /api/automation/work-items/:id/agent-runs` → `delivery.StartAgentRun`
- `POST /api/automation/work-items/:id/evidence` → `delivery.CreateEvidence`
- `POST /api/automation/work-items/:id/messages` → `delivery.CreateMessage`

`GET /api/automation/tasks/:id/artifacts/:name` is an owner-only, task-scoped
presigned download for private QA screenshots and other agent artifacts.

Delivery submissions are deliberately gated: `submit_plan`, `submit_code_review`,
`submit_qa` and the final `approve_release` decision require their completed
matching agent result. Code-review submission
also requires `pull_request_url` to be a valid HTTP(S) URL; `preview_ready`
requires a valid HTTP(S) `preview_url`. These inputs are recorded with the
append-only human decision and never authorize production release on their own.

These private ITBEM routes require a platform administrator. Human gate decisions
are append-only, and a code approval only authorizes controlled preview deployment;
QA and production release remain independent gates.

### Events
- `GET /api/events/all` → `events.ListEvents` — compatibility path for dashboard list; requires auth and returns root/all or user-scoped events
- `GET /api/events` → `events.ListEvents` — Protected. Query params: `?client_id=UUID` (optional). Root users see all events; regular users see events for their accessible clients; with `?client_id` returns events for that client (access-checked).
- `POST /api/events` → `events.CreateEvent`
- `PUT /api/events/:id` → `events.UpdateEvent`
- `DELETE /api/events/:id` → `events.DeleteEvent`
- `POST /api/events/:id/cover` → `events.UploadEventCover` — Stores a validated source, returns `202` plus a signed pending preview, and queues responsive processing. The previous public cover remains active until a generation-matched terminal callback succeeds. Local environments without SQS retain a synchronous fallback.
- `DELETE /api/events/:id/cover` → `events.DeleteEventCover` — Clears `cover_image_url` and best-effort deletes the old S3 object.
- `POST /api/events/covers/backfill` → `events.BackfillEventCovers` — Root-only bounded/idempotent backfill for legacy covers without responsive variants.
- `PUT /api/events/:id/cover/content` → `events.UpdateEventCoverContent` — Internal authenticated media callback; rejects stale generations with `409`.
- `POST /api/moments/backfill` → `moments.BackfillMomentVariants` — Root-only bounded/idempotent discovery and requeue of legacy image moments without variants.
- `POST /api/events/:identifier/performance` → `events.TrackPerformance` — Public aggregate-only RUM ingestion with anonymous five-minute operational histograms retained for 48 hours.
- `GET /api/events/:id/analytics` → `events.GetEventAnalytics` — GetEventAnalytics — returns EventAnalytics for the event

- `GET /api/events/:id/detail` -> `events.GetEvent` - protected event detail by UUID for dashboard pages

### Event Config (1:1 with Event, same ID)
- `GET /api/events/:id/config` → `eventconfig.GetEventConfig`
- `PUT /api/events/:id/config` → `eventconfig.UpdateEventConfig` — validates public availability: `active_until` must be strictly after `active_from` when both are set; blank `active_until` is allowed.

### Event Sections
- `GET /api/events/:id/sections` → `eventsection.ListSectionsByEvent`
- `POST /api/events/:id/sections` → `eventsection.CreateSection`
- `PATCH /api/events/:id/sections/reorder` → `eventsection.ReorderSections` — Bulk reorder event sections. Body: `{"sections": [{"id": "uuid", "order": 1}]}`.
- `PUT /api/sections/:id` → `eventsection.UpdateSection`
- `DELETE /api/sections/:id` → `eventsection.DeleteSection`

### Resources
- `POST /api/resources` → `resources.CreateResource`
- `POST /api/resources/multiple` → `resources.UploadMultipleResources`
- `PUT /api/resources/:id/content` → `resources.UpdateFileContent`
- `PUT /api/resources/:id/replace` → `resources.ReplaceFile`
- `DELETE /api/resources/:id` → `resources.DeleteResource`

### Fonts
- `POST /api/fonts/upload` → `fonts.UploadFonts` — root only

### Guests
- `GET /api/guests/:key` → `guests.GetGuests` — protected; supports `all:<eventID>` or a guest UUID, access-checked before returning cached data
- `POST /api/guests` → `guests.CreateGuest`
- `POST /api/guests/batch` → `guests.CreateGuests` — atomic batch: creates guests + invitations + tokens in one transaction
- `PUT /api/guests/:id` → `guests.UpdateGuest`
- `DELETE /api/guests/bulk` → `guests.BulkDeleteGuests` — body: `{"ids": ["uuid1", "uuid2"]}`. Soft-deletes multiple guests and invalidates per-event Redis cache.
- `DELETE /api/guests/:id` → `guests.DeleteGuest`

### Moments
- `GET /api/moments` → `moments.ListMoments` — supports `?event_id=<uuid>` to filter by event. When `event_id` is provided, only returns moments with `processing_status NOT IN ('pending','processing')` — i.e., fully optimized by Lambda or legacy uploads. S3 media keys in `content_url` / `thumbnail_url` are returned as presigned URLs for the dashboard.
- `POST /api/moments/bulk-approve` → `moments.BulkApproveRejectMoments` — Bulk approve or reject moments. Body: `{"ids": ["uuid1", "uuid2"], "is_approved": true}`. Returns 200 OK. Invalidates Redis cache.
- `DELETE /api/moments/bulk` → `moments.BulkDeleteMoments` — Bulk delete moments. Body: `{"ids": ["uuid1", "uuid2"]}`. Returns 200 OK. Invalidates Redis cache and per-event wall cache.
- `GET /api/moments/:id` → `moments.GetMoment`
- `POST /api/moments` → `moments.CreateMoment`
- `PUT /api/moments/:id/requeue` → `moments.RequeueMoment` — Admin action to retry failed/stuck Lambda processing. Resets `processing_status` to `"pending"` and re-publishes the SQS job. Returns 200 OK with updated moment. Requires raw S3 key to still be present in `content_url` (i.e. moment not yet fully optimized).
- `PUT /api/moments/:id` → `moments.UpdateMoment` — Invalidates Redis wall cache for the event.
- `DELETE /api/moments/:id` → `moments.DeleteMoment` — Invalidates Redis wall cache for the event.

### Clients / Organizations
- `POST /api/clients` → `clients.CreateNewClient`
- `GET /api/clients` → `clients.ListMyClients`
- `GET /api/clients/children` → `clients.GetMySubClients` (?parent_id=...)
- `GET /api/clients/:id` → `clients.GetClient` (recursive)
- `PUT /api/clients/:id` → `clients.UpdateClient`
- `DELETE /api/clients/:id` → `clients.DeleteClient` (recursive)
- `POST /api/clients/invite` → `clients.InviteUser`
- `POST /api/clients/members` → `clients.CreateClientMember`
- `GET /api/clients/members` → `clients.ListClientMembers`
- `PUT /api/clients/members/:user_id` → `clients.UpdateMemberRole` (?client_id=...)
- `DELETE /api/clients/members/:user_id` → `clients.RemoveMember` (?client_id=...)

### Users (self + admin)
- `GET /api/users` → `users.GetUser` (my profile)
- `PUT /api/users` → `users.UpdateUser`
- `DELETE /api/users` → `users.DeleteUser`
- `GET /api/users/all` → `users.ListAllUsers` — root only; paginated and filterable; query params: `?page=1&page_size=50&search=ana&status=active` (status: `active`, `inactive`, `root`; default page=1, page_size=50, max page_size=200)
- `GET /api/users/:id` → `users.GetUserDetail` — root only
- `GET /api/users/:id/clients` → `users.ListUserClients` — root only
- `PUT /api/users/:id` → `users.UpdateUserDetail` — root only
- `DELETE /api/users/:id` → `users.DeleteUserDetail` — root only
- `PUT /api/users/:id/deactivate` → `users.DeactivateUser` — root only
- `PUT /api/users/:id/activate` → `users.ActivateUser` — root only
- `POST /api/users/invite` → `users.InviteUser` — root only
- `POST /api/users/avatar` → `users.UploadAvatar`
- `DELETE /api/users/avatar` → `users.DeleteAvatar`

### Cache Utilities (protected — root only)
- `GET /api/cache/flush/:key` → `cache.FlushKey`
- `GET /api/cache/flush-all` → `cache.FlushAll`

### Catalogs
- `GET /api/catalogs/client-types` → `clienttypes.ListClientTypes`
- `GET /api/catalogs/roles` → `clientroles.ListClientRoles` (?client_id=...)
- `GET /api/catalogs/design-templates` → `designtemplates.ListDesignTemplates` — list active design templates with color palettes and font sets
- `GET /api/catalogs/design-templates/:id` → `designtemplates.GetDesignTemplate` — get single design template with full relations
- `GET /api/catalogs/color-palettes` → `designtemplates.ListColorPalettes` — list color palettes with patterns and colors
- `GET /api/catalogs/font-sets` → `designtemplates.ListFontSets` — list font sets with patterns and fonts
- `GET /api/event-types` → `eventtypes.ListEventTypes`

---

## Internal Routes — Lambda Callbacks (no Cognito auth, X-Internal-Secret only)

- `PUT /api/moments/:id/content` → `moments.UpdateMomentContent` — Called by Lambda/workers after media optimization completes. Requires `X-Internal-Secret` to match `INTERNAL_API_SECRET` or the temporary `INTERNAL_API_SECRET_PREVIOUS` rotation value; comparisons are constant-time. Body: `{ content_url: string, processing_status: "done"|"failed"|"processing", thumbnail_url?: string, error_message?: string, processing_duration_ms?: number, original_size_bytes?: number, optimized_size_bytes?: number }`. Updates `Moment.ContentURL`, `Moment.ProcessingStatus`, optional worker metadata, and busts Redis wall cache for the event.

---

## Context Values (Protected Routes)

Injected by token middleware:
```go
cognitoSub := c.Get("cognito_sub").(string)
userEmail  := c.Get("user_email").(string)
cfg        := c.Get("config").(*models.Config)
```

## Controller Mapping

| Controller File | Routes Prefix |
|----------------|---------------|
| `controllers/events/events.go` | `/api/events*` |
| `controllers/guests/guests.go` | `/api/guests*` |
| `controllers/invitations/invitations.go` | `/api/invitations*` |
| `controllers/resources/resources.go` | `/api/resources*` |
| `controllers/fonts/fonts.go` | `/api/fonts*` |
| `controllers/clients/clients.go` | `/api/clients*` |
| `controllers/users/users.go` | `/api/users*` |
| `controllers/clienttypes/clientTypes.go` | `/api/catalogs/client-types` |
| `controllers/clientroles/clientRoles.go` | `/api/catalogs/roles` |
| `controllers/cache/cache.go` | `/api/cache*` |
| `controllers/health/health.go` | `/health` |
| `controllers/eventconfig/eventconfig.go` | `/api/events/:id/config` |
| `controllers/eventsection/eventsection.go` | `/api/events/:id/sections`, `/api/sections/:id` |
| `controllers/moments/moments.go` | `/api/moments*` |
| `controllers/designtemplates/designtemplates.go` | `/api/catalogs/design-templates*`, `/api/catalogs/color-palettes`, `/api/catalogs/font-sets` |
