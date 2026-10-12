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
| Check-in | Check-in and RSVP operations only. |
| Analyst | Read event and guest analytics only. |
| Member | Standard event collaboration. |
| Viewer | Read-only access. |

Only Owner and Administrator memberships propagate down an organization hierarchy. A child organization can never grant access back to its parent.

## Delegation invariants

- A role can grant, change, or remove only roles below its hierarchy.
- An Owner cannot be assigned or removed through ordinary member management.
- A platform root (level 1 or 2) cannot assign Owner, change an Owner's role or remove an Owner through member management.
- A platform root grants (adds, invites or re-roles into) only roles whose capabilities its own level holds (`authz.PlatformRootMayGrantRole`), so it can never act through an account it controls beyond its own powers. A Primary root may grant any role below Owner. A level-2 root may grant only Check-in, Analyst, Member or Viewer, never Admin, Event manager or Editor, which carry event creation, editing or deletion or organization management (`403`; `GET /api/catalogs/roles` does not offer them).
- Changing another member's access without changing their organization role (member application access, event assignments) follows the same ranking: an Owner/Admin may act only on members whose effective role ranks strictly below its own (never itself), a level-2 root on any member except an Owner, and only a Primary root on an Owner. Nobody but a Primary root may act on a platform-root account.
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
| Check-in | Owner, Admin, Event manager, Check-in | The check-in workspace and status operations. |
| Analytics view | Owner, Admin, Event manager, Editor, Analyst | Aggregate event and RSVP metrics. |
| Members manage | Owner and Admin (and both platform-root levels) | Organization and event-team assignment, limited to members ranked below the actor (see Delegation invariants). |

An Analyst can read aggregate analytics, but cannot retrieve guest-level data such as emails, invitation tokens, dietary restrictions or exports. A Check-in role can work in the dedicated check-in workspace without gaining access to the complete guest-management surface.
