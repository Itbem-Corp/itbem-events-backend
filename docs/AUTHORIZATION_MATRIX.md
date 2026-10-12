# Identity and authorization model

The platform uses one Cognito user pool for identity and separate application
clients for each product origin. Cognito proves who the user is; the database
decides where that identity may enter and what it may do.

## Security invariants

1. A valid Cognito token is not sufficient to enter a product. The token
   audience and API hostname resolve an application, and `/api/session` requires
   an explicit application entitlement or allowed platform authority.
2. Application access, organization membership, and event assignment are
   independent ceilings. A narrower layer can remove authority but cannot add
   authority that a broader layer did not grant. The single exception is an
   EventiApp event-client grant (`event_client_grants`, behind
   `EVENT_CLIENT_ACCESS`): it is a separate authority for exactly one event,
   used only when the caller has no organization access to that event's
   organization, and it never opens organization reads. Precedence is
   platform root > organization membership > event-client grant > deny, and
   every lookup error fails closed (500, never a fall-through).
3. Customer-branded portals strip platform-root authority. Root status never
   grants implicit access to Cafetton House or another customer portal.
4. UI capabilities are advisory. API middleware and resource controllers
   enforce the same boundary server-side.
5. A user may use the same Cognito identity in multiple products, but each
   product returns only its entitled organizations and effective capabilities.

## Platform administrator levels

| Product | Root level 1 | Root level 2 |
| --- | --- | --- |
| EventiApp | Full event definition and operation across the platform, product metrics and the audit trail | Event visibility, guest operation, check-in, analytics and product metrics; cannot change event structure or read the audit trail |
| ITBEM | Organizations, members, application access, users and root assignment | Read/support view of users and organizations; cannot change governance or root levels |
| Cafetton House | No implicit access; requires an explicit organization membership and application entitlement | No implicit access; requires an explicit organization membership and application entitlement |

## Organization roles

| Role | Organization/team | Event content | Guests | Check-in | Analytics | Metrics |
| --- | --- | --- | --- | --- | --- | --- |
| Owner / Admin | Manage | Manage | Manage | Run | View | View |
| Event manager / Editor / Member | View | Manage | Manage | Run | View | EventiApp: No. Cafetton House / ITBEM: Event manager and Editor view, Member no |
| Check-in | View | View | No | Run | No | No |
| Analyst | View | View | No | No | View | EventiApp: No. Cafetton House / ITBEM: View |
| Guest | View | View | No | No | No | No |

An optional event assignment can narrow this further for one event.

### Event clients (EventiApp, `EVENT_CLIENT_ACCESS`)

| Who | Organization/team | Event content | Guests | Check-in | Analytics | Metrics | Create / delete / duplicate / members / clients |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Event client (grant on one event) | No | Manage (that event) | Manage, including PII and CSV export | Run | View | No | No |

- A grant is not an organization role: no `client_members` row, no
  organization list, no `client_id`-scoped lists, no create, delete,
  duplicate, move, team or event-member management, no metrics or audit.
- A grant is void when the holder has any `client_members` row (active or
  inactive) in the grant's organization, when the event is deleted or changes
  organization, when the organization is inactive or deleted, when the caller
  signed in through a branded tenant (`AuthTenantCode`), or when the
  organization does not resolve to EventiApp (nearest branded ancestor).
- Managers (Owner, Admin, Event manager unless narrowed by an event
  assignment, Root 1) hold `event:clients:manage` and assign clients per
  event; Root 2 does not. Owner/Admin (and both roots, through
  `members:manage`) oversee and revoke every grant of their organization.

### Event organization changes

- Moving an event (`PUT /api/events/:id` with another `client_id`) requires
  organization-level `event:manage` on both the source and the destination.
  A grant never satisfies it. A move revokes the event's grants.
- Detaching (`client_id: null`) is Root 1 only.
- Duplicating (`POST /api/events/:id/duplicate`) requires organization-level
  `event:manage` on the source organization in addition to `event:create`
  on the destination.

**Analytics** is event analytics (`analytics:view`, `GET /api/events/:id/analytics`).
**Metrics** is the product-metrics portfolio (`metrics:view`,
`GET /api/metrics/portfolio`). EventiApp reserves metrics for organization
Owner/Admin (direct or inherited, any case) and platform roots 1 and 2
(`internal/products/eventiapp/access.go`, read through
`products.ModuleRequiresOrganizationAdmin`). Cafetton House and ITBEM keep
metrics for Event manager, Editor and Analyst. The rule is per organization: a
user who is Owner of A and Editor of B sees metrics for A only.

The security audit trail (`GET /api/audit-logs`, `audit:view`) is Root 1 only
on EventiApp and ITBEM; Cafetton House strips platform authority, so nobody
reads it there. Root 2, Owner and Admin never receive it.

## Product surfaces

- EventiApp exposes event operations. Organization data is available only when
  required to select or authorize an event workspace.
- ITBEM exposes the platform control plane: organizations, users, roles and
  application entitlements.
- Cafetton House exposes its branded event/team workspace. It does not inherit
  ITBEM platform administration.

## Multi-product provisioning

To grant a member access:

1. Add the identity to an organization with one organization role.
2. Enable the desired application for that organization.
3. Create or activate the member/application entitlement.

The boot seed (`seeds.SeedApplications`, run on every start) only inserts
missing rows. It never re-enables a member/application entitlement or a
brand-root product that an operator disabled; `models.Client` AfterCreate still
upserts, because a new membership is an explicit grant.

Removing an entitlement invalidates the application session cache and blocks
the next request or token refresh. Removing the Cognito user is not required and
would incorrectly remove access to their other products.
