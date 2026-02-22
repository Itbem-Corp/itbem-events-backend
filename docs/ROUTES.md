# Routes Index

> Update this file whenever you add, modify, or remove routes.
> Source of truth: `routes/routes.go` — function `ConfigurarRutas`

## Route Structure

- **Public** (`/api`) — Redis cache middleware only, no auth
- **Protected** (`/api`) — Token auth + Redis cache middleware

## Health Check

- `GET /health` → `controllers/health.Check` — DB ping + Redis ping, returns `200 ok` or `503 degraded`

---

## Public Routes (`/api`)

### Events
- `GET /api/events/page-spec?token=...` → `events.GetPageSpec` — SDUI: PageSpec por token (publico). Includes `access` object with `activeFrom`, `activeUntil`, `passwordProtected`.
- `POST /api/events/:identifier/view` → `events.TrackView` — Fire-and-forget view increment (no auth). Uses sessionStorage in client to deduplicate per session.
- `POST /api/events/:identifier/verify-access` → `events.VerifyEventAccess` — Password gate verification. Body: `{password: string}`. Returns 200 on match, 401 on wrong password.
- `GET /api/events/:key` → `events.GetEvents`

### Resources
- `GET /api/resources/:id` → `resources.GetResource`
- `GET /api/resources/section/:key` → `resources.GetResourcesBySectionID`

### Guests
- `GET /api/guests/:key` → `guests.GetGuests`

### Invitations / RSVP
- `GET /api/invitations/ByToken/:token` → `invitations.GetInvitationByToken`
- `POST /api/invitations/rsvp` → `invitations.ConfirmRSVP`

### Public Moments (MomentWall)
- `GET /api/events/:identifier/moments` → `moments.ListPublicMoments` — Paginated wall. Query: `?page=1&limit=20` (max 50). Returns only `is_approved=true AND processing_status IN ('','done')`. Response: `{ items, total, page, limit, has_more, published, uploads_remaining, uploads_used }`. Cached in Redis (`moments:wall:{eventID}:p{n}:l{n}`, TTL 5min).
- `POST /api/events/:identifier/moments` → `moments.CreatePublicMoment` — Upload with personal `pretty_token`. Rate limited (sensitiveRateLimiter). Per-IP limit: controlled by `EventConfig.MaxUploadsPerGuest` (default 3) per event (Redis counter, 30-day window). Body limit: 225MB. Stores raw file to S3 `moments/{eventID}/raw/{uuid}.ext`, sets `processing_status="pending"`, queues SQS job synchronously. If `EventConfig.AutoApproveUploads=true`, moment is auto-approved on creation.
- `POST /api/events/:identifier/moments/shared` → `moments.CreateSharedMoment` — Upload via QR code (no personal token). Requires `EventConfig.ShareUploadsEnabled=true`. Same configurable per-IP limit, and async processing as personal upload. Respects `AutoApproveUploads`.

---

## Protected Routes (`/api`) — Require `Authorization: Bearer <token>`

### Events
- `GET /api/events` → `events.ListEvents` — Protected. Query params: `?client_id=UUID` (optional). Root users see all events; regular users see events for their accessible clients; with `?client_id` returns events for that client (access-checked).
- `POST /api/events` → `events.CreateEvent`
- `PUT /api/events/:id` → `events.UpdateEvent`
- `DELETE /api/events/:id` → `events.DeleteEvent`
- `GET /api/events/:id/analytics` → `events.GetEventAnalytics` — GetEventAnalytics — returns EventAnalytics for the event

### Event Config (1:1 with Event, same ID)
- `GET /api/events/:id/config` → `eventconfig.GetEventConfig`
- `PUT /api/events/:id/config` → `eventconfig.UpdateEventConfig`

### Event Sections
- `GET /api/events/:id/sections` → `eventsection.ListSectionsByEvent`
- `POST /api/events/:id/sections` → `eventsection.CreateSection`
- `PUT /api/sections/:id` → `eventsection.UpdateSection`
- `DELETE /api/sections/:id` → `eventsection.DeleteSection`

### Resources
- `POST /api/resources` → `resources.CreateResource`
- `POST /api/resources/multiple` → `resources.UploadMultipleResources`
- `PUT /api/resources/:id/content` → `resources.UpdateFileContent`
- `PUT /api/resources/:id/replace` → `resources.ReplaceFile`
- `DELETE /api/resources/:id` → `resources.DeleteResource`

### Fonts
- `POST /api/fonts/upload` → `fonts.UploadFonts`

### Guests
- `POST /api/guests` → `guests.CreateGuest`
- `POST /api/guests/batch` → `guests.CreateGuests` — atomic batch: creates guests + invitations + tokens in one transaction
- `PUT /api/guests/:id` → `guests.UpdateGuest`
- `DELETE /api/guests/bulk` → `guests.BulkDeleteGuests` — body: `{"ids": ["uuid1", "uuid2"]}`. Soft-deletes multiple guests and invalidates per-event Redis cache.
- `DELETE /api/guests/:id` → `guests.DeleteGuest`

### Moments
- `GET /api/moments` → `moments.ListMoments` — supports `?event_id=<uuid>` to filter by event. When `event_id` is provided, only returns moments with `processing_status NOT IN ('pending','processing')` — i.e., fully optimized by Lambda or legacy uploads. Admins never see "still processing" items.
- `POST /api/moments/bulk-approve` → `moments.BulkApproveRejectMoments` — Bulk approve or reject moments. Body: `{"ids": ["uuid1", "uuid2"], "is_approved": true}`. Returns 200 OK. Invalidates Redis cache.
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
- `GET /api/users/all` → `users.ListAllUsers` — paginated; query params: `?page=1&page_size=50` (default: page=1, page_size=50, max page_size=200)
- `GET /api/users/:id` → `users.GetUserDetail`
- `GET /api/users/:id/clients` → `users.ListUserClients`
- `PUT /api/users/:id/deactivate` → `users.DeactivateUser`
- `PUT /api/users/:id/activate` → `users.ActivateUser`
- `POST /api/users/invite` → `users.InviteUser`
- `POST /api/users/avatar` → `users.UploadAvatar`
- `DELETE /api/users/avatar` → `users.DeleteAvatar`

### Cache Utilities (protected — requires auth)
- `GET /api/cache/flush/:key` → `cache.FlushKey`
- `GET /api/cache/flush-all` → `cache.FlushAll`

### Catalogs
- `GET /api/catalogs/client-types` → `clienttypes.ListClientTypes`
- `GET /api/catalogs/roles` → `clientroles.ListClientRoles` (?client_id=...)
- `GET /api/event-types` → `eventtypes.ListEventTypes`

---

## Internal Routes — Lambda Callbacks (no Cognito auth, X-Internal-Secret only)

- `PUT /api/moments/:id/content` → `moments.UpdateMomentContent` — Called by Lambda after media optimization completes. Requires header `X-Internal-Secret: {INTERNAL_API_SECRET}`. Body: `{ content_url: string, processing_status: "done"|"failed"|"processing" }`. Updates `Moment.ContentURL` and `Moment.ProcessingStatus` in DB, busts Redis wall cache for the event.

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
