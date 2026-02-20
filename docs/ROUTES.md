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
- `GET /api/events/page-spec?token=...` → `events.GetPageSpec` — SDUI: PageSpec por token (publico)
- `GET /api/events/:key` → `events.GetEvents`

### Resources
- `GET /api/resources/:id` → `resources.GetResource`
- `GET /api/resources/section/:key` → `resources.GetResourcesBySectionID`

### Guests
- `GET /api/guests/:key` → `guests.GetGuests`

### Invitations / RSVP
- `GET /api/invitations/ByToken/:token` → `invitations.GetInvitationByToken`
- `POST /api/invitations/rsvp` → `invitations.ConfirmRSVP`

---

## Protected Routes (`/api`) — Require `Authorization: Bearer <token>`

### Events
- `POST /api/events` → `events.CreateEvent`
- `PUT /api/events/:id` → `events.UpdateEvent`
- `DELETE /api/events/:id` → `events.DeleteEvent`

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
- `DELETE /api/guests/:id` → `guests.DeleteGuest`

### Moments
- `GET /api/moments` → `moments.ListMoments`
- `GET /api/moments/:id` → `moments.GetMoment`
- `POST /api/moments` → `moments.CreateMoment`
- `PUT /api/moments/:id` → `moments.UpdateMoment`
- `DELETE /api/moments/:id` → `moments.DeleteMoment`

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
