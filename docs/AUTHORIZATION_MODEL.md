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

## Delegation invariants

- A role can grant, change, or remove only roles below its hierarchy.
- An Owner cannot be assigned or removed through ordinary member management.
- A platform root cannot assign Owner through member management.
- Only a Primary root can set `root_level`; it may set only `0` or `2`.
- Event permissions are a ceiling from the organization role. An event assignment may reduce access, never elevate it.

## Event capabilities and privacy

| Capability | Typical roles | What it covers |
| --- | --- | --- |
| View | All assigned roles | Event metadata and read-only workspace access. |
| Event manage | Owner, Admin, Event manager, Editor, Member | Event configuration, sections, media and moments. |
| Guest manage | Owner, Admin, Event manager, Editor, Member | Guest directory, seating, invitations, RSVP links and exports. This is the boundary for guest PII. |
| Check-in | Guest-managing roles and Check-in | The check-in workspace and status operations. |
| Analytics view | Owner, Admin, Event manager, Editor, Member, Analyst | Aggregate event and RSVP metrics. |
| Members manage | Owner and Admin | Organization and event-team assignment. |

An Analyst can read aggregate analytics, but cannot retrieve guest-level data such as emails, invitation tokens, dietary restrictions or exports. A Check-in role can work in the dedicated check-in workspace without gaining access to the complete guest-management surface.

View access never includes the event access password or a guest's invitation token, and Check-in never includes guest contact data, notes or tokens. Read routes that would carry them either require the capability, redact the field or return a narrower projection:

| Payload | Rule |
| --- | --- |
| `GET /events/:id/config`, `GET /events/:id/studio-workspace` (full config with `auth_password_preview`) | Require Event manage. |
| `GET /events/:id/seating-workspace` (guest emails and RSVP state) | Requires Guest manage. |
| `GET /events/:id/detail` | View access; `auth_password_preview` is blanked without Event manage and `guest_share_summary.first_pending` (email and invitation token) is omitted without Guest manage. A failed capability lookup redacts. |
| `GET /events/:id/checkin-workspace`, `GET /guests/checkin:<event>` | Require Check-in and return the door projection (`dtos.CheckinGuest`): name, table, party size and RSVP status only. No email, phone, notes, dietary restrictions or invitation/RSVP token, for any caller. A QR scan is resolved on the server (`qr` query). |

The role lists in the capability table above describe intent. The enforced role-to-capability mapping is `roleHasCapability` and `eventRoleHasCapability` in `internal/authz/authz.go`, which `GET /events/:id/capabilities` and the dashboard follow.
