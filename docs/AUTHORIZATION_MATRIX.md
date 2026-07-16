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
   authority that a broader layer did not grant.
3. Customer-branded portals strip platform-root authority. Root status never
   grants implicit access to Cafetton House or another customer portal.
4. UI capabilities are advisory. API middleware and resource controllers
   enforce the same boundary server-side.
5. A user may use the same Cognito identity in multiple products, but each
   product returns only its entitled organizations and effective capabilities.

## Platform administrator levels

| Product | Root level 1 | Root level 2 |
| --- | --- | --- |
| EventiApp | Full event definition and operation across the platform | Event visibility, guest operation, check-in and analytics; cannot change event structure |
| ITBEM | Organizations, members, application access, users and root assignment | Read/support view of users and organizations; cannot change governance or root levels |
| Cafetton House | No implicit access; requires an explicit organization membership and application entitlement | No implicit access; requires an explicit organization membership and application entitlement |

## Organization roles

| Role | Organization/team | Event content | Guests | Check-in | Analytics |
| --- | --- | --- | --- | --- | --- |
| Owner / Admin | Manage | Manage | Manage | Run | View |
| Event manager / Editor / Member | View | Manage | Manage | Run | View |
| Check-in | View | View | No | Run | No |
| Analyst | View | View | No | No | View |
| Guest | View | View | No | No | No |

An optional event assignment can narrow this further for one event.

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

Removing an entitlement invalidates the application session cache and blocks
the next request or token refresh. Removing the Cognito user is not required and
would incorrectly remove access to their other products.
