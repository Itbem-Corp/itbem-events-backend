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

- `GET /api/automation/agents` → `automation.GetAgentDirectory` — v1 snapshot of logical profiles, recent worker instances, assigned active runs, 30-day usage/cost aggregates, and queued counts by operation. Platform admins (Root 1/2) may query platform-wide or filter `client_id`/`project_id`; organization-workspace callers must select a client or project, and client-level data is intersected with their project-view memberships. Organization context and project/client ownership are validated server-side; IDs are not authorization. Scoped aggregates do not mix other clients/projects, and scoped availability is explicitly unknown because worker heartbeats are not hierarchy-owned. The response omits prompts, private object refs, raw usage, hostnames, paths, and credentials. See `docs/AGENT_DIRECTORY.md` for status and identity trust-boundary details.
- `GET /api/automation/agents/stream?client_id=<UUID>&project_id=<UUID>` → `automation.StreamAgentDirectory` — SSE invalidation feed compatible with `GET /api/automation/agents` filters and authorization. Platform admins can subscribe globally or to a validated client/project scope; organization workspaces remain organization-bound, require `client_id` or `project_id` for non-platform admins, and apply project-view membership. When both filters are present the project must belong to the selected client subtree. Emits `snapshot` on connect and `update` only when the safe directory projection in that same authorized scope changes; each event is exactly `{ revision, generated_at }`, where `revision` is a random opaque token rather than a hash/count/data fingerprint. The server revalidates the same workspace, filters, and authorization every 12 seconds, sends comment heartbeats every 12 seconds, and rolls over after 55 seconds so the authenticated dashboard hook reconnects and re-fetches the protected snapshot/history APIs. Responses use private/no-store security headers and include no directory, task, provider, or secret data.
- `GET|POST /api/automation/agent-instances`, `DELETE /api/automation/agent-instances/:id` → `automation.ListAgentInstances` / `RegisterAgentInstance` / `RevokeAgentInstance` — Primary-root-only machine enrollment. Registration accepts only profile key, stable machine UUID, and Ed25519 public key; APIs expose a fingerprint, never the private key or provider credentials. See `docs/AGENT_DIRECTORY.md` for the callback signing contract.
- `POST /api/internal/automation/agent-instances/enroll` → `automation.EnrollGatewayAgentInstance` — First-start self-enrollment for gateway workers. Requires the exact lane-bound gateway token plus an Ed25519 proof from the local key; the server assigns the instance ID and rejects revoked identities. See `docs/AGENT_DIRECTORY.md` for enrollment and callback security.
- `GET /api/automation/agents/:agentKey/history` → `automation.GetAgentHistory` — Cursor-paginated allow-list timeline across database-captured append-only task lifecycle events (created/claimed/reclaimed/reassigned/attempt/status), legacy snapshots only where no lifecycle event exists, inference/tool calls, plan-step lifecycle events, and sanitized step activity. Query: `limit` (default 50, max 100), opaque `cursor`, RFC3339 `from`/`to` (`to` exclusive), and optional `client_id`, `project_id`, `work_item_id`, `worker_id`, `machine_id`, `agent_instance_id`, `run_id`, `operation`, `status`, `provider`. `platform` workspace requires a platform administrator; requested client/project records are validated server-side, and when both are provided the project must belong to the selected client subtree. `organization` workspace always intersects the middleware-selected organization, validates client ancestry, and uses canonical project-view membership. A non-root client-level query is intersected with only projects whose membership grants view; IDs never grant access. Identity filters are correlation only. Cursor scope binds actor, workspace, agent, selected hierarchy and effective authorized projects. The projection may show current/previous assignment IDs and fixed event labels, but never prompts, raw output/errors, object references, commands, file paths, or reasoning.
- `GET /api/automation/dispatch/queue` → `automation.GetDispatchQueue` — Read-only view of nonterminal assignments from approved delivery-plan executions. Platform Root operators may inspect the global queue; organization workspaces must provide `project_id`, which is checked with project-view authorization before querying. Organization responses omit machine identity and worker-wide heartbeat/capacity signals so shared workers cannot disclose activity outside that project. Query: `page_size` (default 25, max 100), actor/filter-bound `cursor`, optional assignment `status`, `project_id`, and `agent_key`. Platform responses may include the fixed target agent/machine and heartbeat-derived target status/capacity (90-second liveness window). Both scopes return safe task/step/project labels, dependency readiness, lifecycle timestamps, and an optional next cursor when more results exist; never prompts/input/object references or credentials, and never changes the scheduler's fixed assignment.
- `GET /api/automation/traces` → `automation.GetAutomationTraces` — One server-side UNION timeline, globally ordered and keyset-paginated across legacy task snapshots, task lifecycle events, inference/tool calls, plan-step events, and validated step activity. Query filters: `client_id`, `project_id`, `epic_id`, `work_item_id`, `step_id` or `step_key`, `agent_key`, `worker_id`, `machine_id`, `agent_instance_id`, `run_id`, exact case-sensitive `model`, `operation`, allow-listed `tool` (`agent_loop`, `stagehand`), `status`, `provider`, RFC3339 `from`/`to` (`to` exclusive), `limit` (default 50, max 100), `cursor`, and optional initial `snapshot_at`. `model` is trimmed and limited to 128 ASCII model-ID characters; `run_id` is trimmed and limited to the existing 64-character run ID format. Both accept only one query value. The response returns `items`, `has_more`, `next_cursor`, `snapshot_at`, and `limit`; the cursor binds the complete filter set, snapshot, actor, tenant, workspace, and organization. `platform` workspace requires platform-admin authority. `organization` workspace always intersects the selected organization and descendant clients; other users also need access to the exact project they filter by. The API exposes only safe identifiers, fixed summaries, bounded metadata, and validated evidence digests—never request/response payloads, prompts, secrets, raw output, private object references, or reasoning.
- `GET /api/automation/costs` → `automation.CostOverview` — Cost ledger summary intersected with the authenticated workspace: organization mode requires and applies `organization_id`; platform mode is platform-admin-only. Within an organization, actor ownership/project membership is intersected with the tenant boundary (not ORed around it). Filters: `days` (1–365), or an optional paired RFC3339 `from_at`/exclusive `to_at` window (maximum 368 days); `project_id`, `epic_id`, `work_item_id`, `step_key`, `agent_key`, `agent_instance_id`, `provider`, `model`, and either offset `page`/`page_size` or opaque `cursor` pagination. The explicit timestamp window overrides the relative-days preset; the dashboard converts the selected calendar days in the caller's local timezone into RFC3339 instants. Epic filtering uses an `EXISTS` predicate over the task's project-scoped membership interval `[created_at, deleted_at)` as of each execution's `completed_at`, so historical cost stays with the epic active when the call completed and a task is never double-counted at a move boundary. Both ledger and work-item cursors bind all filters, workspace, and actor. Summary, breakdowns, ledger rows, work-item costs, and agent-instance costs share the selected time window; budget-watch arrays remain current portfolio guardrails for the selected workspace and are not re-filtered. Ledger rows include project/work-item/agent routing metadata but never request or response bodies.
- `GET /api/automation/ai/projects/:projectId/provider-usage` / `POST .../provider-usage/refresh` → `automation.GetProjectProviderUsage` / `RefreshProjectProviderUsage` — Project-scoped, permission-checked vendor balance/quota snapshots. GET returns the latest safe append-only capture (or an empty `accounts` array); refresh requires project-management permission and uses only that project's own credentials. DeepSeek and MiniMax report their native balance/quota units; OpenRouter is explicitly unsupported until a separate management credential is configured. Provider payloads, keys, and raw errors are never persisted or returned.
- `GET|POST /api/automation/projects` → `delivery.ListProjects` / `delivery.CreateProject`
- `GET /api/automation/projects/:id` → `delivery.GetProject`
- `GET /api/automation/projects/:id/autonomy-readiness?repository=github://owner/repo` → `delivery.GetAutonomyReadiness` — Project-view access required. Verifies the registered Vault and effective policy, diagnoses local source/publication App configuration, and distinguishes live GitHub/environment unknowns from verified configuration. The response grants no execution authority and does not advertise production autonomy while the delegated coordinator is incomplete.
- `GET|POST /api/automation/projects/:projectId/schedules`, `GET|PATCH /api/automation/projects/:projectId/schedules/:scheduleId`, `POST .../:scheduleId/pause|resume`, `GET .../:scheduleId/occurrences|events` → project-scoped recurring work-item schedules. Reads require project-view; create, edit, pause, and resume require project-manage within the authenticated organization. Recurrence accepts only `daily`, `weekly`, or `monthly`, an IANA `time_zone`, local `HH:MM`, `starts_on`/optional `ends_on`, and `misfire_policy: coalesce`. Every occurrence key is unique for `(schedule, revision, local wall-clock occurrence)`; the bounded dispatcher claims due schedules with `FOR UPDATE SKIP LOCKED`. A due tick creates a new work item in `planning`, snapshots current ready context, and adds only a `plan` continuation. It never approves a plan, creates implementation work, or dispatches a provider directly; the existing planning/gate/continuation flow remains authoritative. Each schedule requires a positive per-occurrence budget. Invalid/stale context safely records a blocked occurrence and pauses the schedule. The safe API projection omits stored JSON columns, secrets, and arbitrary event details.
- `GET /api/automation/projects/:id/epics` / `POST /api/automation/projects/:id/epics` → `delivery.ListEpics` / `delivery.CreateEpic`
- `GET /api/automation/epics/:epic` → `delivery.GetEpic` — Returns safe epic metadata and a separately cursor-paginated task projection. Query: `tasks_limit` (default 25, max 100), optional exact `tasks_state` (one allow-listed work-item state; empty or omitted includes all states), and `tasks_cursor`. The cursor is bound to both the epic and task-state filter, so it cannot be reused across states. `work_items.total` counts only tasks matching the selected state; task DTOs omit descriptions, mandates, prompts/results, and private object references.
- `POST /api/automation/epics/:epic/work-items` / `DELETE /api/automation/epics/:epic/work-items/:workItemID` → `delivery.AddWorkItemToEpic` / `delivery.RemoveWorkItemFromEpic`
- `POST /api/automation/projects/:id/context` → `delivery.CreateContext`
- `PATCH /api/automation/projects/:id/context/:sourceID` → `delivery.UpdateContextMetadata` — Updates allowlisted metadata for repository architecture, environment routing, or a project runbook. Environment updates require a valid branch/deployment pair and only accept HTTP(S) URLs without credentials, query, or fragment; workflow updates are bounded text. Existing work-item context snapshots remain immutable; runbook edits advance the `vN` revision and environment edits align the revision to the branch.
- `POST /api/automation/projects/:id/work-items` → `delivery.CreateWorkItem` — accepts optional `max_concurrency` from 1 to 8; absent an explicit value, new approved work items default to 2 simultaneous independent plan steps. The scheduler still respects DAG readiness, worker slots, workspace isolation, and one aggregate project/task budget.
- `GET /api/automation/work-items/:id` → `delivery.GetWorkItem`
- `GET /api/automation/plans/:id/steps` → `delivery.ListPlanSteps` — Explicit, safe step DTO includes bounded `evidence_requirements` frozen into the approved plan hash. Each requirement has a stable key/title, optional description, required flag, MIME allow-list (`application/json`, `image/jpeg`, `image/png`, `text/csv`, `text/markdown`, `text/plain`) and `max_bytes` (1..1 MiB). Empty/legacy requirements serialize as `[]` and do not alter the legacy approved-plan hash bytes.
- `GET /api/automation/plans/:id/steps/:stepId/evidence` → `delivery.ListPlanStepEvidence` — Project-view authorized, newest-first cursor pagination (default 25, max 100), optional `requirement_key` and `run_id` filters. Metadata includes only evidence ID, requirement key, safe display filename, MIME, size, digest, source, task/run, logical agent/instance, fence and timestamp; no object bucket/key, arbitrary metadata or presigned URL is returned.
- `GET /api/automation/plans/:id/steps/:stepId/evidence/:evidenceId/content` → `delivery.DownloadPlanStepEvidence` — Project-view authorized proxy download. The server rechecks private object size and SHA-256 and returns an attachment with `nosniff`, `sandbox`, and `private, no-store`; it does not redirect to storage.
- `GET /api/automation/work-items/:id/stream` → `delivery.StreamWorkItem` — authenticated SSE invalidation feed for live execution maps. It emits a `snapshot` on connect, then `update` only when the database-backed work-item revision changes. Its bounded payload is `{ work_item_id, revision, state, active_tasks, last_activity_at, generated_at }`; it never includes prompts, private object references, provider payloads, or task output. Connections refresh authorization after 55 seconds and clients reconnect using the SSE retry directive.
- `POST /api/automation/work-items/:id/transitions` → `delivery.TransitionWorkItem`
- `POST /api/automation/work-items/:id/agent-runs` → `delivery.StartAgentRun` — returns HTTP 409 with error code `plan_execution_conflict` when an active execution or a live unassigned legacy step lease owns the approved plan; retry after that ownership is released. Fan-out creation and legacy claims serialize on the plan lock so both admission orders reject conflicting ownership.
- `GET /api/automation/plans/:id/assignment-events` → `delivery.ListPlanStepAssignmentEvents` — authenticated project-view authorization intersected with the selected organization; cursor-paginated append-only history of assignment creation and actual status/target changes. Query: `limit` (default 25, max 100), opaque cursor, and optional `step_id`, `assignment_id`, `task_id` (parent or child), `status`, `agent_key`, `machine_id`. Cursor binds the actor, workspace, plan, organization, and filters. The minimal DTO contains assignment/execution/step/parent-task/child-task IDs, fixed event type, previous/current status and target identities, and timestamp; no request/output payload, credentials, or arbitrary metadata. PostgreSQL captures updates made through direct SQL as well as application code. Older assignments are retained but do not receive synthetic historical events.
- `POST /api/automation/work-items/:id/evidence` → `delivery.CreateEvidence`
- `POST /api/automation/work-items/:id/messages` → `delivery.CreateMessage`

`GET /api/automation/tasks/:id/artifacts/:name` is an owner-only, task-scoped
presigned download for private QA screenshots and other agent artifacts.

Delivery submissions are deliberately gated: `submit_plan`, `submit_code_review`,
`submit_qa` and the final `approve_release` decision require their completed
matching agent result. Release approval also fails closed unless the selected
Gatekeeper event is the newest evaluation, is no more than ten minutes old,
reproduces an `allowed` decision for this exact work-item change-set, and carries
the same authenticated human actor. Code-review submission
also requires `pull_request_url` to be a valid HTTP(S) URL; `preview_ready`
requires a valid HTTP(S) `preview_url`. These inputs are recorded with the
append-only human decision and never authorize production release on their own.

These private ITBEM routes require a platform administrator. Human gate decisions
are append-only, and a code approval only authorizes controlled preview deployment;
QA and production release remain independent gates.

The `release_gate` agent-run phase is available only during `release_review` to
a release-authorized human. It is routed to the providerless `release_manager`
worker on the `release` lane. The initial candidate is derived exclusively from
GitHub-App-published change sets covering every repository marked `changes` in
the approved plan. The worker refreshes GitHub PR/branch/check state; completed
exact-matrix QA tasks promote only configured local `security:secrets` and
`security:high-critical` results into an immutable security event. The control
plane independently reloads policy, Vault, QA, and security ledgers. Missing
compatibility, migration, dependency, environment, or recovery evidence remains
blocking.
Gatekeeper schema v3 binds the human approval subject to the exact revision
matrix, resolved policy digest, canonical Vault evidence digest, required test
kinds, protected-branch requirements (including GitHub integration identity)
and recovery classification. Any policy, Vault revision/reconciliation or
required-check identity change therefore makes the previous approval stale.
Legacy v1/v2 events remain auditable but cannot authorize a v3 action.

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

### Internal ITBEM worker gateway

- `PUT /api/internal/automation/agents/heartbeat`, `PUT /api/internal/automation/tasks/:id`, and `/steps/*` callbacks → `automation.AgentHeartbeat`, `Complete`, and delivery plan-step handlers — Registered per-instance Ed25519 signature over method, request URI, timestamp, single-use nonce, and raw-body digest; TLS outside loopback. The server derives profile/machine identity from the key and still checks project assignment, active lease and fencing.
- `POST /api/internal/automation/steps/:id/evidence` → `automation.UploadDeliveryPlanStepEvidence` — Signed raw-file callback; full request body remains under the existing 1 MiB callback cap. Query identity is `task_id`, `run_id`, `worker_id`, `agent_key`, `machine_id`, `fencing_token`, idempotent `event_id`, `requirement_key`, signed `content_type`, and safe `file_name`; the `Content-Type` header must equal that signed query value and match the frozen requirement. Optional `X-Content-SHA256` is checked against the actual bytes. The service binds the registered callback instance to the live task/step lease, assignment and fence, scans known high-confidence credential patterns, validates supported text/JSON/PNG/JPEG content, and writes AES-256-SSE private S3 bytes before appending metadata. Retries with the same event ID and exact semantic payload are idempotent. Completion is blocked until every required requirement has a verified record for the same plan version, task/run, instance and fence. Upload response contains safe metadata only; browser uploads are not part of this worker callback.
- `POST /api/internal/automation/inference` → `automation.Infer` — Active attempt-policy capability and task/run-lease authenticated (not the callback secret). Resolves the provider key only inside the cloud backend and returns a normalized completion; it never exposes a provider credential.

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

## Trace event correlation

Each `GET /api/automation/traces` item may include `automation_task_id` and a
sanitized `run_id` when the source event has a durable task/run relationship.
Synthetic gate-decision events intentionally omit `automation_task_id`. These
IDs are correlation fields only; authorization is still evaluated from the
event's project/work-item scope and never granted by an identifier.

Global trace items expose token, latency, and cost values only for immutable
inference/tool-call ledger rows. These fields are omitted for lifecycle,
assignment, and evidence events; a missing usage value must not be interpreted
as a free provider call. Each paginated traces/history response includes
`cost_coverage` with `scope: returned_page`, `verified_usd_executions`, and
`unpriced_executions`; these counters describe only the returned page, not the
whole filtered result set. Inference/tool rows carry
`cost_pricing_status: verified_usd` only when currency is USD and a concrete
pricing basis is recorded. Otherwise the status is `unknown`, cost is omitted,
and observed token counts are retained. An explicit numeric zero with verified
USD coverage is a recorded zero, not a missing value.
