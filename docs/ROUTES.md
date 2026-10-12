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
- `GET /api/events/:key` → `events.GetEvents` — Anonymous single-event read by UUID or identifier (`all` is protected). Same gate as `/meta` (`allowPublicEventReadWithProof`): an inactive event or one outside its access window is 403 unless `preview_token` is valid for it; a password-protected event needs the `verify-access` proof (`X-Event-Access-Token` or `event_access_token`) and an invitation token does not replace it; a non-public event needs a valid, unexpired invitation token of that event. Response keeps the legacy array envelope, `data: [PublicEventResponse]` (`id`, `name`, `identifier`, `description`, `cover_image_url`, `cover_view_url`, `view_url` and their expiries, `event_date_time`, `address`, `second_address`, `timezone`, `language`, `organizer_name`) and never the dashboard `EventResponse` (no organization, organizer email/phone, capacity, config, guest or media-processing fields). No first-party client calls it: the public site reads `/meta` and `/page-spec`, the dashboard reads `/events/:id/detail`.

### Resources
- `GET /api/resources/:id` → `resources.GetResource` - Public resource detail/signing. If the parent event has `EventConfig.AuthPasswordPreview`, callers must send `X-Event-Access-Token` from `POST /api/events/:identifier/verify-access` unless `preview_token` is valid.
- `GET /api/resources/section/:key` → `resources.GetResourcesBySectionID` - Public section resources/signing. Same password proof requirement as resource detail.

### Attendees
- `GET /api/events/section/:sectionId/attendees` → `guests.GetAttendees` - Public attendee list for attendee-backed sections. If the parent event has `EventConfig.AuthPasswordPreview`, callers must send `X-Event-Access-Token` unless `preview_token` is valid.

### Invitations / RSVP
- `GET /api/invitations/ByToken?token=...` → `invitations.GetInvitationByToken` — recommended for URL-sensitive tokens. Query aliases accepted: `token`, `Token`, `invitation_token`, `invitationToken`, `InvitationToken`, `pretty_token`, `prettyToken`, `PrettyToken`. `GET /api/invitations/ByToken/:token` is still registered for simple legacy tokens.
- `POST /api/invitations/rsvp` → `invitations.ConfirmRSVP` — `rsvp_method` (any of `method`, `Method`, `rsvp_method`, `rsvpMethod`, `RSVPMethod`) is stored only as `web` or `app`; `host` (the organizer's answer and what a door check-in writes), empty and any other text are stored as `web`, never a 400. The guest's own answer clears the door check-in provenance (see `PATCH /api/guests/bulk/status`).

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

### Session, audit trail and product metrics
- `GET /api/session` → `sessions.GetSession` — application session: user, organizations with per-organization `capabilities`, top-level `capabilities`. On EventiApp `metrics:view` appears only on Owner/Admin organizations (direct or inherited) and, top-level, for roots 1/2 or a user with at least one Owner/Admin organization.
- `POST /api/session/organization-context` → `sessions.IssueOrganizationContext` — issues the 5-minute signed `X-Organization-Context` credential for an organization in the session (403 otherwise). Audit: a successful re-issue for the organization the request already selected (`X-Organization-ID` equal to the body `organization_id`) writes no audit row (`audit.SkipOnSuccess`); switches, 400, 401, 403 and 503 are still audited.
- `GET /api/audit-logs` → `auditlogs.List` — Root 1 only (`authz.RequirePrimaryRoot`); everyone else gets 403 `Primary platform administrator required`.
- `GET /api/metrics/portfolio?from=&to=&client_id=` → `productmetrics.Portfolio` — surface gate `metrics:view`. With `client_id`: `analytics:view` on that organization, or `metrics:view` on EventiApp (Owner/Admin or root). Without `client_id`: platform roots only, otherwise 403 `Organization required`. Known gap: summaries with `client_id` are not filtered by tenant. `product_totals[]` = `{tenant_code, active_users}` gives distinct active users per product over the window, scoped like the rest of the response (do not sum `summaries[].active_users`, which double-counts users active in several organizations); the organization-scoped timeline's daily `active_users` counts only that organization.

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
- `GET /api/events/all` → `events.ListEvents` — compatibility path for dashboard list; requires auth and returns root/all or caller-access events
- `GET /api/events` → `events.ListEvents` — Protected. Query params: `?client_id=UUID` (optional), `page`/`page_size`/`search`/`filter` (paged). Root users see all events; other users without `client_id` see the caller-access portfolio: organizations the application session lists for them (active, not deleted, membership enabled for the request application; direct memberships only). With `?client_id` returns events for that client (access-checked, inherited Owner/Admin included). See `docs/AUTHORIZATION_MODEL.md`.
- `GET /api/events/dashboard` → `events.GetDashboardOverview` and `GET /api/events/notifications` → `events.GetEventNotifications` — same scope rules as `GET /api/events`.
- `POST /api/events` → `events.CreateEvent`
- `PUT /api/events/:id` → `events.UpdateEvent`
- `DELETE /api/events/:id` → `events.DeleteEvent`
- `POST /api/events/:id/cover` → `events.UploadEventCover` — Stores a validated source, returns `202` plus a signed pending preview, and queues responsive processing. The previous public cover remains active until a generation-matched terminal callback succeeds. Local environments without SQS retain a synchronous fallback.
- `DELETE /api/events/:id/cover` → `events.DeleteEventCover` — Clears `cover_image_url` and best-effort deletes the old S3 object.
- `POST /api/events/covers/backfill` → `events.BackfillEventCovers` — Primary-root-only (level 1; Root 2, standard users and other-product sessions get `403`) bounded/idempotent backfill for legacy covers without responsive variants. The candidate scan is global across tenants.
- `PUT /api/events/:id/cover/content` → `events.UpdateEventCoverContent` — Internal authenticated media callback; rejects stale generations with `409`.
- `POST /api/moments/backfill` → `moments.BackfillMomentVariants` — Primary-root-only (level 1; Root 2, standard users and other-product sessions get `403`) bounded/idempotent discovery and requeue of legacy image moments without variants. The candidate scan is global across tenants.
- `POST /api/events/:identifier/performance` → `events.TrackPerformance` — Public aggregate-only RUM ingestion with anonymous five-minute operational histograms retained for 48 hours.
- `GET /api/events/:id/analytics` → `events.GetEventAnalytics` — GetEventAnalytics — returns EventAnalytics for the event

- `GET /api/events/:id/detail` -> `events.GetEvent` - protected event detail by UUID for dashboard pages. Requires event view access. Fields are redacted by capability: without `event:manage`, `event_config.auth_password_preview` and `config.auth_password_preview` are `""` (`is_auth_preview` stays); without `guest:manage`, `guest_share_summary.first_pending` (guest email + invitation pretty token) is omitted while the share counts stay. A failed capability lookup redacts (fails closed).
- `GET /api/events/:id/capabilities` → `eventmembers.Capabilities` — Requires event view access; returns the caller's own capability booleans (`event:manage`, `event:delete`, `guest:manage`, `checkin:run`, `analytics:view`, `members:manage`) after event-assignment narrowing.

### Event Config (1:1 with Event, same ID)
- `GET /api/events/:id/config` → `eventconfig.GetEventConfig` — Requires `event:manage` (the config includes the plaintext `auth_password_preview`).
- `PUT /api/events/:id/config` → `eventconfig.UpdateEventConfig` — Requires `event:manage`. Validates public availability: `active_until` must be strictly after `active_from` when both are set; blank `active_until` is allowed.
- `GET /api/events/:id/studio-workspace` → `eventconfig.GetStudioWorkspace` — Requires `event:manage`. Returns `{event, config, sections}` for Studio's first paint; `config` is the same full payload as `GET /config`, including `auth_password_preview`.

### Tables / Seating
- `GET /api/events/:id/tables` → `eventtables.ListTables` — Requires event view access; returns table id, name, capacity and sort order only (no guest data).
- `GET /api/events/:id/seating-workspace` → `eventtables.GetSeatingWorkspace` — Requires `guest:manage`. Returns `{tables, guests}` where each guest carries name, email, table and RSVP status (same projection as `GET /guests/seating:<id>`).
- `POST /api/events/:id/tables`, `PUT /api/events/:id/tables/assign`, `PUT /api/events/:id/tables/plan`, `PUT /api/tables/:id`, `DELETE /api/tables/:id` → `eventtables.*` — Require `guest:manage`.

### Check-in
- `GET /api/events/:id/checkin-workspace` → `guests.GetCheckinWorkspace` — Requires `checkin:run`. Returns `{event, statuses, guests}`; `guests` is the first page (60) of the door projection `dtos.CheckinGuest` with the event summary: id, event_id, first/last name, table (`table_id`, `table`, `table_number`), `guest_status`/`status` with ids, `guests_count`, `rsvp_status`, `rsvp_guest_count`, and `checked_in_at` while a door check-in is in place. No email, phone, notes, RSVP notes, dietary restrictions, `pretty_token`, `invitation_id` or `rsvp_token_id`, for any caller.
- `GET /api/guests/checkin:<eventID>` → `guests.GetGuests` — Requires `checkin:run`. Paged (`page`, `page_size` 1-100, `search`, `filter`, `qr`, `sort`, `direction`) door projection, same fields as the workspace, plus `summary`. `qr` matches the guest id or invitation `pretty_token` exactly on the server, so the scanner never needs the token in the response. `search` is the door search (`DoorSearch`): a substring of the name or the table (number or name), with `%`, `_` and `\` matched literally; email only as the complete address (case-insensitive) and phone only as the complete number (digits compared, formatting ignored). A partial email or phone matches nothing, so the list cannot be used to read contact data back. `page:`, `invitations:` and the CSV export keep the substring search over name, email, phone and table.
- `PATCH /api/guests/bulk/status` → `guests.BulkUpdateGuestStatus` — Requires `checkin:run`. Body `{event_id, ids (1-100), status_id|guest_status_id, rsvp_status, rsvp_method: "host", action?: "checkin"|"undo_checkin"}`; returns no guest data. Without `guest:manage` a caller can only check guests in and undo door check-ins; the full rules are under Guests.

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
- `GET /api/guests/:key` → `guests.GetGuests` — protected; supports `all:<eventID>`, `page:<eventID>`, `invitations:<eventID>`, `seating:<eventID>`, `analytics:<eventID>`, `share:<eventID>` and a guest UUID (all `guest:manage`), `summary:<eventID>` (`analytics:view`) and `checkin:<eventID>` (`checkin:run`, door projection; see Check-in). `page:` and `invitations:` return the full guest record, including `pretty_token`.
- `GET /api/events/:id/guests/export` → `guests.ExportGuestsCSV` — `guest:manage`. CSV download (`?view=` default, `invitations` or `rsvp`; same `search`/`filter`/`sort`/`direction` as the guest list; 413 above 250,000 rows). Every data cell is neutralized against spreadsheet formula injection: a cell whose first character after leading spaces is `= + - @`, TAB or CR gets a leading `'` (so `+52 55…` exports as `'+52 55…`). Header rows are constants.
- `POST /api/guests` → `guests.CreateGuest` — `guest:manage` on `event_id`. Body fields are an allowlist (`guestWritePayload` in `controllers/guests/guests.go`, snake_case plus the camel/Pascal aliases): `first_name`, `last_name`, `nickname`, `email`, `phone`, `show_contact_info`, `role`, `bio`, `headline`, `signature`, `order`, `image_url`, `image1_url`..`image3_url`, `table_number`, `table_id`, `is_host`, `notes`, `rsvp_notes`, `dietary_restrictions`, `guests_count`, `rsvp_guest_count`, `max_guests`, `status_id`/`guest_status_id`, `rsvp_status`, `rsvp_method`, plus `event_id`. Anything else (`id`, `invitation_id`, `rsvp_token_id`, `rsvp_at`, `created_at`, `updated_at`, `pretty_token`, nested `table`/`guest_status`/`status` objects) is silently ignored. `invitation_id` and `rsvp_token_id` are server-owned: the guest always gets its own new invitation and token. `table_id` `""`/`null` means no table; any other value must be a live (not soft-deleted) table of the same event, else 400 `{"message":"Mesa inválida","error":"table_outside_event"}` and nothing is written. A status id under any spelling (`status_id`, `guest_status_id`, `statusId`, `GuestStatusID`...) that is `""` or the nil UUID is "not sent" and keeps the stored status (pending on create); a snake_case status id that is not a UUID string answers 400.
- `POST /api/guests/batch` → `guests.CreateGuests` — atomic batch: creates guests + invitations + tokens in one transaction. Max 1000 guests (400 `Too many guests`), all of one event; same field allowlist and `table_id` rule as `POST /api/guests`. The batch's distinct table ids are checked in one query; one bad table rejects the whole batch (400 `table_outside_event`) and nothing is created.
- `PUT /api/guests/:id` → `guests.UpdateGuest` — `guest:manage`. Same field allowlist as create; `event_id` and the server-owned columns in the body are ignored, JSON `null` leaves a stored value unchanged, `invitation_id` and `rsvp_token_id` can never be changed here (every spelling is ignored), so a guest cannot be linked to another event's invitation or token and the response's `pretty_token` is always the guest's own. A body without any `table_id` key keeps the stored table; `""`/`null` clears it; any other value must be a live table of the guest's event, else 400 `{"message":"Mesa inválida","error":"table_outside_event"}` and nothing is written. `max_guests` syncs the guest's stored invitation only, and only when that invitation belongs to the guest's event (a legacy link to another event's invitation is left untouched and logged).
- `DELETE /api/guests/bulk` → `guests.BulkDeleteGuests` — `guest:manage` per guest. Body: `{"ids": ["uuid1", "uuid2"]}`, max 100 ids (400 `Too many IDs`), duplicates collapsed. Soft-deletes multiple guests and invalidates per-event Redis cache.
- `PATCH /api/guests/bulk/status` → `guests.BulkUpdateGuestStatus` — `checkin:run` on `event_id`. Body: `{"event_id", "ids" (1-100, no duplicates), "status_id"|"guest_status_id", "rsvp_status": "pending"|"confirmed"|"declined", "rsvp_method": "host", "action"?: "checkin"|"undo_checkin"}`. The status must exist (400 `Invalid status_id`) and its catalog code must equal `rsvp_status` (400 `status_id does not match rsvp_status`), for every caller. All writes lock the guests (`FOR UPDATE`) in one transaction; a guest outside `event_id` fails the request (400) and nothing is written. Like `PUT /api/guests/:id`, `rsvp_at` is stamped only when a guest's RSVP status changes and the `rsvp_confirmed`/`rsvp_declined` analytics move per guest.
  - **Check-in** (`"action": "checkin"` with `rsvp_status` `confirmed` and the confirmed status, from any `checkin:run` caller; or the same body without `action` from a caller without `guest:manage`, who can only check guests in). Sets confirmed/`host` and records the door check-in provenance (`checked_in_at`, `checked_in_by_user_id` and the previous RSVP status, guest status, method and `rsvp_at`). A guest still checked in at the door is left untouched (a repeat check-in is a no-op). `action: checkin` with any other `rsvp_status` answers 400 `Invalid rsvp_status`.
  - **Organizer confirmation** (`rsvp_status` `confirmed` without `action`, from a `guest:manage` caller, e.g. the guests panel's bulk Confirmar): written like `PUT /api/guests/:id`. Sets confirmed/`host`, records no provenance and clears a door check-in already in place, so check-in staff cannot undo it (409). A guest manager checking guests in at the door sends `"action": "checkin"`. If the `guest:manage` check fails with anything but 403, that error is returned and nothing is written.
  - **Undo** (`"action": "undo_checkin"`; status fields are ignored): any `checkin:run` caller. A guest still checked in at the door (provenance present, confirmed, `host`) gets the recorded previous values back and the provenance is cleared; a guest who is not confirmed is a no-op (double and concurrent undos are idempotent). A guest confirmed any other way (their own RSVP, an organizer confirmation through `PUT /api/guests/:id` or this endpoint, only the status code, an RSVP given after the check-in) answers 409 `Check-in cannot be undone` and nothing is written. Any other `action` answers 400 `Invalid action`.
  - **Pending or declined**: needs `guest:manage` as well (403 `Access denied` otherwise); clears the door check-in provenance.
  - Any other RSVP write clears the provenance too (`PUT /api/guests/:id` when `rsvp_status`, `rsvp_method` or the status changes, and the public RSVP), so an undo can never restore a superseded answer. Guest responses carry `checked_in_at` while a door check-in is in place.
- `DELETE /api/guests/:id` → `guests.DeleteGuest`

### Event Members (team assignments)
- `GET /api/events/:id/capabilities` → `eventmembers.Capabilities` — the caller's effective event capabilities.
- `GET /api/events/:id/members` → `eventmembers.List` — `members:manage` on the event.
- `PUT /api/events/:id/members` → `eventmembers.Upsert` — body `{"user_id": UUID, "role": "EVENT_OWNER|MANAGER|EDITOR|CHECKIN|ANALYST|VIEWER"}`; `members:manage` plus `clients.AuthorizeMemberTarget` on the event's organization (same ranking as member application access). Denied → `403 Permission Denied`; target without organization access → `400`.
- `DELETE /api/events/:id/members/:user_id` → `eventmembers.Remove` — same authorization as `Upsert`; a deleted account has no rank, so its stale assignment can still be removed.

### Moments
- `GET /api/moments` → `moments.ListMoments` — supports `?event_id=<uuid>` to filter by event. When `event_id` is provided, only returns moments with `processing_status NOT IN ('pending','processing')` — i.e., fully optimized by Lambda or legacy uploads. S3 media keys in `content_url` / `thumbnail_url` are returned as presigned URLs for the dashboard.
- `POST /api/moments/bulk-approve` → `moments.BulkApproveRejectMoments` — Bulk approve or reject moments. Body: `{"ids": ["uuid1", "uuid2"], "is_approved": true}`, max 5000 ids (400 `Too many IDs`), duplicates collapsed. Authorized with one batch lookup and one `event:manage` check per distinct event (a missing id is 404; a moment without an event falls back to the per-moment check). Returns 200 OK. Invalidates Redis cache.
- `DELETE /api/moments/bulk` → `moments.BulkDeleteMoments` — Bulk delete moments. Body: `{"ids": ["uuid1", "uuid2"]}`, same 5000-id cap, dedupe and per-event authorization as bulk-approve. Returns 200 OK. Invalidates Redis cache and per-event wall cache.
- `PATCH /api/moments/reorder` → `moments.ReorderMoments` — Body: `[{"id": "uuid", "order": 1}]`, max 5000 items (400 `Too many IDs`). The same id twice with the same order collapses; with different orders it is a 400. Same per-event `event:manage` authorization as bulk-approve.
- `POST /api/moments/batch/reoptimize` → `moments.BatchReoptimizeMoments` — Body: `{"ids": [...]}`, max 200 ids, `event:manage` per moment. Response: `{succeeded, skipped, failed}`.
- `GET /api/moments/:id` → `moments.GetMoment`
- `POST /api/moments` → `moments.CreateMoment` — `event:manage` on `event_id`. Body fields are an allowlist (`momentCreatePayload` in `controllers/moments/moments.go`): `event_id`, `title`, `description`, `content_url`, `thumbnail_url`, `content_type`, `is_approved`, `order`, `moment_type_id`, `guest_id`, `invitation_id`. Anything else (`id`, `processing_*`, `media_variants`, `error_message`, size metrics, nested `guest`/`invitation`/`moment_type` objects) is ignored, so a new moment never starts in a processed state. `content_url` and `thumbnail_url`, when not empty, must be keys of this event's moments: `moments/<event_id>/<...>`, bare or under `organizations/<uuid>/` (`momentsService.IsEventMomentKey`); absolute URLs, other prefixes, `..` and backslashes answer 400 `{"message":"Archivo inválido","error":"moment_content_outside_event"}`. `guest_id` and `invitation_id` (the nil UUID means none) must name a guest or invitation of the same event, else 400 `{"message":"Referencia inválida","error":"reference_outside_event"}`. These checks run after authorization and before any write; the admin response therefore only presigns keys of the event.
- `PUT /api/moments/:id/requeue` → `moments.RequeueMoment` — Admin action to retry failed/stuck Lambda processing. Resets `processing_status` to `"pending"` and re-publishes the SQS job. Returns 200 OK with updated moment. Requires raw S3 key to still be present in `content_url` (i.e. moment not yet fully optimized).
- `PUT /api/moments/:id` → `moments.UpdateMoment` — `event:manage`. Binds only `title`, `description`, `is_approved` (also `isApproved`/`IsApproved`) and `order`; media keys, processing state, references and `event_id` in the body are ignored. Invalidates Redis wall cache for the event.
- `DELETE /api/moments/:id` → `moments.DeleteMoment` — Invalidates Redis wall cache for the event.

### Clients / Organizations
- `POST /api/clients` → `clients.CreateNewClient` — Owner of the parent (direct or inherited) or Root 1; only Root 1 creates top-level organizations. Hierarchy is Platform › Agency › Customer (Customers have no children). Errors (code in `error`, Spanish `message`): 400 `CLIENT_HIERARCHY_LIMIT`, 400 `CLIENT_TYPE_NOT_ALLOWED`, 400 `CLIENT_TYPE_INVALID`, 400 `CLIENT_NAME_REQUIRED`, 403 `CLIENT_PERMISSION_DENIED`, 404 `CLIENT_PARENT_NOT_FOUND`, 409 `CLIENT_CODE_CONFLICT`, 500 `CLIENT_CREATE_FAILED`; logo problems keep the upload statuses. `code` is generated once from the name (accent-free slug, `-2`, `-3`… if already used by an organization or an application)
- `GET /api/clients` → `clients.ListMyClients`
- `GET /api/clients/children` → `clients.GetMySubClients` (?parent_id=...)
- `GET /api/clients/:id` → `clients.GetClient` (recursive)
- `PUT /api/clients/:id` → `clients.UpdateClient` — Owner (or Root 1); renaming changes `name` only, `code` is stable; authorization runs before the logo upload, and a failed update removes the new object. Errors: 403 `CLIENT_PERMISSION_DENIED`, 404 `CLIENT_NOT_FOUND` (code in `error`)
- `DELETE /api/clients/:id` → `clients.DeleteClient` (recursive) — 409 `CLIENT_HAS_CHILDREN` when it has sub-organizations, 403 `CLIENT_PERMISSION_DENIED`, 404 `CLIENT_NOT_FOUND`; logo deleted after commit
- `POST /api/clients/invite` → `clients.InviteUser` — body `{"client_id", "user_id" or "email", "role_id"}`; adds an existing account. Besides the member-write rules below, the account is ranked with `clients.AuthorizeMemberTarget` on its effective (direct or inherited) role in `client_id`, because the new direct membership takes precedence over an Owner/Admin role inherited from a parent organization: an Owner/Admin may only add accounts ranked strictly below its own effective role (never itself), Root 2 anyone except an Owner, only a primary root an Owner; nobody but a primary root a platform-root account. Denied → `403 Permission Denied` with nothing written.
- `POST /api/clients/members` → `clients.CreateClientMember`
- `GET /api/clients/members` → `clients.ListClientMembers`
- `PUT /api/clients/members/:user_id` → `clients.UpdateMemberRole` (?client_id=...)
- `DELETE /api/clients/members/:user_id` → `clients.RemoveMember` (?client_id=...)
- `GET /api/clients/:id/member-applications/:user_id` → `sessions.ListMemberApplications` — `members:manage` on the organization; Root 2 cannot inspect platform-root accounts (`403`).
- `PUT /api/clients/:id/member-applications/:user_id/:application_code` → `sessions.SetMemberApplication` — body `{"is_active": bool}`; `members:manage` plus `clients.AuthorizeMemberTarget`: an Owner/Admin may only toggle members ranked strictly below its own effective role (never itself), Root 2 any member except an Owner, only a primary root an Owner; nobody but a primary root a platform-root account. Denied → `403 Permission Denied`; application not enabled for the organization → `409`.
- Member writes (`InviteUser`, `CreateClientMember`, `UpdateMemberRole`, `RemoveMember`): both platform-root levels (`IsPlatformAdmin()`; level 1, level 2 and legacy `is_root` accounts) take the root path and manage teams in any organization, but can never assign, change or remove an Owner (`403`); Root 2 also cannot target platform-root accounts (`403`). A root grants (`InviteUser`, `CreateClientMember`, `UpdateMemberRole`) only roles within its own capabilities (`authz.PlatformRootMayGrantRole`): Root 1 any role below Owner, Root 2 only Check-in, Analyst, Member or Viewer; Admin, Event manager or Editor from Root 2 → `403` before any account is created or membership written. Everyone else must hold Owner/Admin (direct or inherited) in that organization and can only manage roles below its own. A root entering a branded product (e.g. Cafetton House) is scoped to an ordinary identity and takes the membership path.

### Users (self + admin)
- `GET /api/users` → `users.GetUser` (my profile)
- `PUT /api/users` → `users.UpdateUser`
- `DELETE /api/users` → `users.DeleteUser` — closes the caller's own account (Cognito + local). A primary-root account (checked on the stored account, whatever product the caller entered through) gets `409 Protected account` with `error: "USER_PRIMARY_ROOT_PROTECTED"`.
- `GET /api/users/all` → `users.ListAllUsers` — root only; paginated and filterable; query params: `?page=1&page_size=50&search=ana&status=active` (status: `active`, `inactive`, `root`; default page=1, page_size=50, max page_size=200). `search` matches email, first name, last name and the full name; `%`/`_` are literal. Root 2's directory keeps the `active`/`inactive` filter
- `GET /api/users/:id` → `users.GetUserDetail` — root only
- `GET /api/users/:id/clients` → `users.ListUserClients` — root only. With `page_size` it returns the organizations the user reaches (direct + Owner/Admin inheritance), each with `access_role` and `inherited`, and counters over that same set: `total`, `active`, `inactive`, `direct`, `inherited`. `clients_count` in `GET /api/users/all` uses the same set
- `PUT /api/users/:id` → `users.UpdateUserDetail` — root only
- `DELETE /api/users/:id` → `users.DeleteUserDetail` — primary root only; a primary-root target (another level 1, a legacy `is_root` account or the caller itself) → `409 Protected account` with `error: "USER_PRIMARY_ROOT_PROTECTED"`. Other refusals (code in `error`, Spanish `message`): 409 `USER_HAS_ACTIVE_MEMBERSHIPS` (remove the memberships first), 409 `USER_SELF_ACTION`, 404 `USER_NOT_FOUND`. Order: disable Cognito → delete local rows (tx) → delete Cognito; a local failure re-enables Cognito, a Cognito delete failure leaves the identity disabled and is logged
- `PUT /api/users/:id/deactivate` → `users.DeactivateUser` — root only; Root 2 cannot target platform-root accounts (`403`); a primary-root target, including the caller itself → `409 Protected account` with `error: "USER_PRIMARY_ROOT_PROTECTED"`; 409 `USER_SELF_ACTION`, 404 `USER_NOT_FOUND`
- `PUT /api/users/:id/activate` → `users.ActivateUser` — root only; Root 2 cannot target platform-root accounts (`403`); re-activating a primary root stays allowed
- `PUT /api/users/:id/root-level` → `users.UpdateUserRootLevel` — Primary-root-only (Root 2, standard users and other-product sessions get `403`). Body `{"root_level": 0|2}` grants or revokes operational Root 2 and returns the admin user summary. Level `1` is never assignable (`400`); a primary-root target (level 1 or a legacy `is_root` account, including the caller itself) is refused with `409 Protected account` (`error: "USER_PRIMARY_ROOT_PROTECTED"`) because level 1 cannot be restored through the API; unknown user → `404`.
- `POST /api/users/invite` → `users.InviteUser` — root only
- `POST /api/users/avatar` → `users.UploadAvatar`
- `DELETE /api/users/avatar` → `users.DeleteAvatar`

### Cache Utilities (protected — primary root only)
- `DELETE /api/cache/flush/:key` → `cache.FlushKey` — `authz.RequirePrimaryRoot`; Root 2 gets `403`. Audited by `middleware/audit`.
- `DELETE /api/cache/flush-all` → `cache.FlushAll` — `authz.RequirePrimaryRoot`; Root 2 gets `403`. Audited by `middleware/audit`.
- **Deprecated:** `GET /api/cache/flush/:key` and `GET /api/cache/flush-all` run the same handlers with the same guard, but `middleware/audit` records only `POST`/`PUT`/`PATCH`/`DELETE`, so a successful `GET` flush leaves no audit row. Use `DELETE`; the `GET` aliases stay only for compatibility until ops confirms no script depends on them.

### Catalogs
- `GET /api/catalogs/client-types` → `clienttypes.ListClientTypes` — without parameters: the types the caller can create (Root 1: all; others: child types of the organizations they own, e.g. Customer for an Agency owner). `?parent_id=` → types allowed under that parent (Owner required; 403/404 otherwise). `?parent_type_code=` → static rule (PLATFORM→AGENCY, AGENCY→CUSTOMER, CUSTOMER→none)
- `GET /api/catalogs/roles` → `clientroles.ListClientRoles` (?client_id=...) — roles the caller may assign there: in any organization, a primary root gets every role below Owner and Root 2 only the roles within its own capabilities (Check-in, Analyst, Member, Viewer; never Admin, Event manager or Editor); everyone else needs a membership and gets the roles below its own (Owner/Admin only).
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
| `controllers/eventmembers/eventmembers.go` | `/api/events/:id/capabilities`, `/api/events/:id/members*` |
| `controllers/sessions/sessions.go` | `/api/session*`, `/api/clients/:id/member-applications*` |
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
