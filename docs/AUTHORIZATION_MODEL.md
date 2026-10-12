# Authorization model

Authorization is evaluated server-side from the current user, the organization that owns an event, and the requested capability. The dashboard mirrors these permissions for clarity, but never grants access itself.

## Platform scope

| Level | Role | Authority |
| --- | --- | --- |
| 1 | Primary root | Full platform governance. The only level that can grant or revoke level 2. |
| 2 | Operational root | Global visibility and support: guests, check-in and analytics. Its user directory is restricted to standard users; it cannot inspect other root accounts or their organization assignments, edit event structure, manage teams, alter organization hierarchy, create another root, grant owner access, or change root levels. |
| 0 | Standard user | Access only through organization and event assignments. |

Historical `is_root=true` accounts are treated as level 1 until explicitly migrated.

## Organization scope

| Role | Can do |
| --- | --- |
| Owner | Organization lifecycle, hierarchy, all members and all events. |
| Administrator | Members below owner, organization operations, all events. |
| Event manager | Create and operate events, guests, check-in and analytics. |
| Editor | Edit event and guest content. |
| Check-in | Check-in and RSVP operations only. |
| Analyst | Read event and guest analytics only. |
| Member | Standard event collaboration. |
| Viewer | Read-only access. |

Only Owner and Administrator memberships propagate down an organization hierarchy. A child organization can never grant access back to its parent.

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

## Delegation invariants

- A role can grant, change, or remove only roles below its hierarchy.
- An Owner cannot be assigned or removed through ordinary member management.
- A platform root cannot assign Owner through member management.
- Only a Primary root can set `root_level`; it may set only `0` or `2`.
- Event permissions are a ceiling from the organization role. An event assignment may reduce access, never elevate it.

## One capability vocabulary

`internal/capabilities` is the single table. It is a leaf package (standard
library only), imported by `internal/authz` and `services/applications`, which
cannot import each other (authz -> productmetricshttp -> services/applications).

- The authz names are canonical (`event:manage`); `internal/authz` re-exports
  them as type aliases, so callers did not change.
- The session names are a projection (`capabilities.SessionName`):

| authz | session |
| --- | --- |
| `view` | `events:view` |
| `event:create` | `events:create` |
| `event:manage` | `events:manage` |
| `event:delete` | `events:delete` |
| `guest:manage` | `guests:manage` |
| `checkin:run` | `checkin:run` |
| `analytics:view` | `analytics:view` |
| `members:manage` | `members:manage` |
| `organization:manage` | `organizations:manage` |
| `metrics:view` | `metrics:view` |
| `event:clients:manage` | `events:clients:manage` |

- `RoleCapabilities(role)` drives both `authz.roleHasCapability` and the
  session's `organizationCapabilities`. Any non-empty unknown role code keeps
  its historical behaviour: `view` in authz, only `organizations:view` in the
  session. Golden tests compare both sides with frozen copies of the previous
  tables.
- `events:clients:manage` reaches the session only while `EVENT_CLIENT_ACCESS`
  is on (`SessionService.ConfigureEventClientCapability`).
- `PolicyAllowList(application)` is the set of names a
  `user_application_policy` may contain and the set the policy ceiling governs.
  `events:delete` and `events:clients:manage` are deliberately outside it.

## Server-side ceilings behind flags

Both checks run after the platform, role (or grant) decision, through
`authz.finalizeAuthorityDecision`:

1. **Product entitlement** (`AUTHZ_PRODUCT_ENTITLEMENT`), event resources and
   event lists only. `CheckApplicationAccessRecursive` answers the role lookup
   and the entitlement in one query. `enforce` answers 403 `Your organization is
   not enabled for this application`. Member, organization and application
   management never apply it, so an Owner can always repair access.
2. **Policy ceiling** (`AUTHZ_POLICY_CEILING`). The application session's
   active policy is stamped on the per-request user; an allow-listed capability
   the policy lacks is a 403 `Your application access policy does not allow this
   action` in `enforce`. The primary root is exempt; Root 2 is capped.

`shadow` never changes a decision: it logs `authz shadow denial` once per
(user, organization, capability) every 10 minutes. Organization lookups are
memoized per request (the event capabilities endpoint runs one access query)
and a lookup error is a 500 that never falls through to another authority path.

## Event capabilities and privacy

| Capability | Typical roles | What it covers |
| --- | --- | --- |
| View | All assigned roles | Event metadata and read-only workspace access. |
| Event manage | Owner, Admin, Event manager, Editor, Member | Event configuration, sections, media and moments. |
| Guest manage | Owner, Admin, Event manager, Editor, Member | Guest directory, seating, invitations, RSVP links and exports. This is the boundary for guest PII. |
| Check-in | Guest-managing roles and Check-in | The check-in workspace and status operations. |
| Analytics view | Owner, Admin, Event manager, Editor, Member, Analyst | Aggregate event and RSVP metrics. |
| Metrics view (`metrics:view`) | EventiApp: Owner and Admin (direct or inherited) plus roots 1 and 2. Cafetton House and ITBEM: Owner, Admin, Event manager, Editor, Analyst | Product-metrics portfolio (`GET /api/metrics/portfolio`). Not an event capability: event analytics stay on Analytics view. |
| Members manage | Owner and Admin | Organization and event-team assignment. |
| Event clients manage (`event:clients:manage`) | Owner, Admin (direct or inherited), Event manager (unless an event assignment narrows it), Root 1. Not Root 2 | Assign external event clients to an event (`/api/events/:id/clients*`). Reported as `false` while `EVENT_CLIENT_ACCESS` is off. |

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

On EventiApp, `services/applications.applicationOrganizationCapabilities`
removes `metrics:view` from every organization role except Owner/Admin, so the
`/api/metrics` surface gate in `middleware/applicationaccess` answers 403
`Application capability denied`. With `client_id`, the portfolio handler also
requires `authz.CapabilityMetricsView` on that organization
(`controllers/productmetrics.authorizePortfolioScope`); other products require
`analytics:view` there.

An Analyst can read aggregate analytics, but cannot retrieve guest-level data such as emails, invitation tokens, dietary restrictions or exports. A Check-in role can work in the dedicated check-in workspace without gaining access to the complete guest-management surface.
