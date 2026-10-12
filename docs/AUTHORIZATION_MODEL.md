# Authorization model

Authorization is evaluated server-side from the current user, the organization that owns an event, and the requested capability. The dashboard mirrors these permissions for clarity, but never grants access itself.

## Platform scope

| Level | Role | Authority |
| --- | --- | --- |
| 1 | Primary root | Full platform governance. The only level that can grant or revoke level 2. |
| 2 | Operational root | Global visibility and support: guests, check-in and analytics, plus team management in any organization (add, invite and re-role members into the roles within its own capabilities: Check-in, Analyst, Member or Viewer; remove members below Owner; member application access; event assignments). Its user directory is restricted to standard users; it cannot inspect or change other root accounts or their organization assignments, touch an Owner, grant Owner, Admin, Event manager, Editor or root, edit event structure (directly or through an account it grants a role to), alter organization hierarchy, create another root, or change root levels. |
| 0 | Standard user | Access only through organization and event assignments. |

Historical `is_root=true` accounts are treated as level 1 until explicitly migrated.

## Organization scope

| Role | Can do |
| --- | --- |
| Owner | Organization lifecycle, hierarchy, all members and all events. |
| Administrator | Members below owner, organization operations, all events. |
| Event manager | Create and operate events, guests, check-in and analytics. |
| Editor | Edit event and guest content. |
| Check-in | Check-in operations only; it can mark guests confirmed at the door and undo door check-ins, but cannot otherwise rewrite a guest's RSVP (not the guest's own answer, not an organizer's confirmation). |
| Analyst | Read event and guest analytics only. |
| Member | Standard event collaboration. |
| Viewer | Read-only access. |

Only Owner and Administrator memberships propagate down an organization hierarchy. A child organization can never grant access back to its parent.

Inheritance is the same on every surface: direct API calls (`CheckAccessRecursive`), the organization lists (`GET /api/clients`, a user's organizations, marked `inherited`), and the application session. The session lists each descendant of an Owner/Admin membership as a workspace (`access_role` `INHERITED_OWNER`/`INHERITED_ADMIN`), so `POST /api/session/organization-context` issues a context for a child organization to the parent's Owner/Admin. Descent stops at an organization that is another application's portal (its code is an application code), so a platform Owner never gets another product's subtree as a workspace. The session also stops at inactive or deleted organizations (an inactive organization and its subtree are never inherited). Checks by organization id apply the same portal stop: for members (not platform admins) `authz.RequireClientAccess`/`RequireClientCapability` and the client service (request-scoped with `WithApplicationScope`) require the target in `ReachableClientIDsInApplication` for the session application (`User.SessionApplicationCode`, set by `authz.CurrentUser` from the Cognito tenant), else 403; a missing reach dependency fails closed.

### Event lists without an organization (caller-access)

`GET /api/events`, `/api/events/all`, `/api/events/dashboard` and
`/api/events/notifications` without `client_id`:

- Platform roots list every event, as before.
- Every other caller gets the caller-access portfolio
  (`dtos.EventCallerAccess`): the events of the organizations the application
  session lists for that caller. The rules are the ones in
  `services/applications.SessionService.organizationAccess`: an active
  membership with a role, in an active and not deleted organization, with an
  active `client_member_applications` row for the request application. The
  application ID comes from the resolved session, never from a header. The
  SQL is `eventsrepository.callerAccessEventPredicate`, shared by every list
  path (paged list and counts, unpaged list, dashboard overview,
  notifications).
- Only direct memberships count. Inherited Owner/Admin access to child
  organizations is reached with an explicit `client_id`, which
  `authz.RequireClientAccess` authorizes as before.
- Without an application session the handlers answer 503
  `Application session unavailable` and run no query.

### Organization hierarchy

Types nest Platform › Agency › Customer; a Customer cannot have sub-organizations (400 `CLIENT_HIERARCHY_LIMIT`). Only a Primary root creates top-level (Platform) organizations. An Owner of an organization (direct or inherited) may create, rename and delete organizations below it; this applies in every product whose contract enables the `organizations` module (ITBEM, Cafetton House). EventiApp's contract does not enable that module, so no EventiApp session carries `organizations:manage` and organization management is not available there. An organization's `code` is assigned once at creation and never changes on rename: it is matched against application codes and used by integrations.

## Delegation invariants

- A role can grant, change, or remove only roles below its hierarchy.
- An Owner cannot be assigned or removed through ordinary member management.
- A platform root (level 1 or 2) cannot assign Owner, change an Owner's role or remove an Owner through member management.
- A platform root grants (adds, invites or re-roles into) only roles whose capabilities its own level holds (`authz.PlatformRootMayGrantRole`), so it can never act through an account it controls beyond its own powers. A Primary root may grant any role below Owner. A level-2 root may grant only Check-in, Analyst, Member or Viewer, never Admin, Event manager or Editor, which carry event creation, editing or deletion or organization management (`403`; `GET /api/catalogs/roles` does not offer them).
- Changing another member's access without changing their organization role (member application access, event assignments), and adding an existing account to an organization (`POST /api/clients/invite`, whose new direct membership takes precedence over an Owner/Admin role inherited from a parent organization), follow the same ranking against the target's effective (direct or inherited) role: an Owner/Admin may act only on members whose effective role ranks strictly below its own (never itself), a level-2 root on any member except an Owner, and only a Primary root on an Owner. Nobody but a Primary root may act on a platform-root account.
- Only a Primary root can set `root_level`; it may set only `0` or `2`, and never on a Primary-root account (including itself), which is refused with `409 Protected account` because level 1 cannot be restored through the API.
- A Primary-root account cannot be deleted (`DELETE /api/users/:id`) or deactivated (`PUT /api/users/:id/deactivate`) by anyone, itself included, and cannot close itself through `DELETE /api/users`: all return `409 Protected account`, so the platform cannot lose its last Primary root through the API. Re-activation stays allowed.
- Event permissions are a ceiling from the organization role. An event assignment may reduce access, never elevate it.

## Event capabilities and privacy

| Capability | Typical roles | What it covers |
| --- | --- | --- |
| View | All assigned roles | Event metadata and read-only workspace access. |
| Event create | Owner, Admin, Event manager | New events in the organization. |
| Event manage | Owner, Admin, Event manager, Editor | Event configuration, sections, media and moments. |
| Event delete | Owner, Admin | Deleting events. |
| Guest manage | Owner, Admin, Event manager, Editor, Member | Guest directory, seating, invitations, RSVP links and exports. This is the boundary for guest PII. |
| Check-in | Guest-managing roles and Check-in | The check-in workspace and the check-in status operation. Its reads return the door projection (`dtos.CheckinGuest`, see below), never the full guest record. Without Guest manage, `PATCH /api/guests/bulk/status` only marks guests confirmed (the door check-in, with or without `action: checkin`) and, with `action: undo_checkin`, gives a door-checked-in guest back the RSVP they had before. Only door check-ins record provenance and can be undone: a guest manager's confirmation without `action: checkin` (the guests panel's bulk Confirmar, or `PUT /api/guests/:id`) and the guest's own confirmation answer 409, and declining or any other RSVP rewrite needs Guest manage. |
| Analytics view | Owner, Admin, Event manager, Editor, Analyst | Aggregate event and RSVP metrics. |
| Metrics view (`metrics:view`) | EventiApp: Owner and Admin (direct or inherited) plus roots 1 and 2. Cafetton House and ITBEM: Owner, Admin, Event manager, Editor, Analyst | Product-metrics portfolio (`GET /api/metrics/portfolio`). Not an event capability: event analytics stay on Analytics view. |
| Members manage | Owner and Admin (and both platform-root levels) | Organization and event-team assignment, limited to members ranked below the actor (see Delegation invariants). |
| Event clients manage (`event:clients:manage`) | Owner, Admin (direct or inherited), Event manager (unless an event assignment narrows it), Root 1. Not Root 2 | Assign external event clients to an event (`/api/events/:id/clients*`). Reported as `false` while `EVENT_CLIENT_ACCESS` is off. |

On EventiApp, `services/applications.applicationOrganizationCapabilities`
removes `metrics:view` from every organization role except Owner/Admin, so the
`/api/metrics` surface gate in `middleware/applicationaccess` answers 403
`Application capability denied`. With `client_id`, the portfolio handler also
requires `authz.CapabilityMetricsView` on that organization
(`controllers/productmetrics.authorizePortfolioScope`); other products require
`analytics:view` there.

An Analyst can read aggregate analytics, but cannot retrieve guest-level data such as emails, invitation tokens, dietary restrictions or exports. A Check-in role can work in the dedicated check-in workspace without gaining access to the complete guest-management surface.

View access never includes the event access password or a guest's invitation token, and Check-in never includes guest contact data, notes or tokens. Read routes that would carry them either require the capability, redact the field or return a narrower projection:

| Payload | Rule |
| --- | --- |
| `GET /events/:id/config`, `GET /events/:id/studio-workspace` (full config with `auth_password_preview`) | Require Event manage. |
| `GET /events/:id/seating-workspace` (guest emails and RSVP state) | Requires Guest manage. |
| `GET /events/:id/detail` | View access; `auth_password_preview` is blanked without Event manage and `guest_share_summary.first_pending` (email and invitation token) is omitted without Guest manage. A failed capability lookup redacts. |
| `GET /events/:id/checkin-workspace`, `GET /guests/checkin:<event>` | Require Check-in and return the door projection (`dtos.CheckinGuest`): name, table, party size and RSVP status only, plus `checked_in_at` while a door check-in is in place (so the page knows which guests it may undo; who checked them in and the previous RSVP stay server-side). No email, phone, notes, dietary restrictions or invitation/RSVP token, for any caller. A QR scan is resolved on the server (`qr` query). The `search` query matches name and table by substring but email and phone only by their complete value, so which guests match never reveals part of a contact field. |

The role lists in the capability table above describe intent. The enforced role-to-capability mapping is `roleHasCapability` and `eventRoleHasCapability` in `internal/authz/authz.go`, which `GET /events/:id/capabilities` and the dashboard follow.

## Event authority precedence

`internal/authz` resolves one authority per request and event
(`resolveEventAuthority`, memoized in the Echo context by
`RequireEventAccess`; `EnsureEventAccess` resolves fresh):

1. Platform root (`platformHasCapability`).
2. Organization access through `CheckAccessRecursiveE`
   (`roleHasCapability`, then the `event_members` narrowing). A lookup error
   is `500 Authorization error` and never reaches step 3.
3. Event-client grant, only when `EVENT_CLIENT_ACCESS` is on (the
   `HasEventClientGrant` hook is configured), the caller has no branded
   `AuthTenantCode`, an active grant exists and the event's organization
   resolves to EventiApp (`ResolveClientProduct`). Capability ceiling:
   view, event:manage, guest:manage, checkin:run, analytics:view
   (`EventClientCapabilityCeiling`). `event_members` is not consulted.
4. `403 Access denied`.

`RequireClientAccess` and `RequireClientCapability` never honour grants, and
now surface organization lookup errors as 500 instead of a silent deny.
`EventCapabilityForOrganizationEvents` evaluates one capability for many events
of one organization with one organization lookup and one `event_members` batch
(managers only, no grant path). `GET /api/events/:id/capabilities` answers 500
when evaluation fails instead of reporting `false`.
