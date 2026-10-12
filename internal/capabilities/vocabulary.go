// Package capabilities is the single capability vocabulary shared by
// internal/authz (server enforcement) and services/applications (the session
// the dashboard reads). It is a leaf: it imports only the standard library, so
// both sides can use it without the authz -> productmetricshttp ->
// services/applications import cycle.
//
// The authz names (singular, "event:manage") are canonical. Session names
// ("events:manage") are a projection of them through SessionName.
package capabilities

import (
	"sort"
	"strings"
)

// Capability is an action, not a screen.
type Capability string

const (
	View          Capability = "view"
	EventCreate   Capability = "event:create"
	EventManage   Capability = "event:manage"
	EventDelete   Capability = "event:delete"
	GuestManage   Capability = "guest:manage"
	Checkin       Capability = "checkin:run"
	AnalyticsView Capability = "analytics:view"
	MembersManage Capability = "members:manage"
	OrgManage     Capability = "organization:manage"
	// MetricsView gates organization-scoped product metrics.
	MetricsView Capability = "metrics:view"
	// EventClientsManage lets a role grant and revoke EventiApp event-client
	// access. The session projects it only while EVENT_CLIENT_ACCESS is on.
	EventClientsManage Capability = "event:clients:manage"
)

// All lists every capability in a stable order.
func All() []Capability {
	return []Capability{
		View, EventCreate, EventManage, EventDelete, GuestManage, Checkin,
		AnalyticsView, MembersManage, OrgManage, MetricsView, EventClientsManage,
	}
}

var sessionNames = map[Capability]string{
	View:               "events:view",
	EventCreate:        "events:create",
	EventManage:        "events:manage",
	EventDelete:        "events:delete",
	GuestManage:        "guests:manage",
	Checkin:            "checkin:run",
	AnalyticsView:      "analytics:view",
	MembersManage:      "members:manage",
	OrgManage:          "organizations:manage",
	MetricsView:        "metrics:view",
	EventClientsManage: "events:clients:manage",
}

// SessionName returns the session (dashboard) name of a capability.
func SessionName(capability Capability) (string, bool) {
	name, ok := sessionNames[capability]
	return name, ok
}

var roleCapabilities = map[string][]Capability{
	"EVENT_MANAGER": {View, EventCreate, EventManage, GuestManage, Checkin, AnalyticsView, EventClientsManage},
	"EDITOR":        {View, EventManage, GuestManage, AnalyticsView},
	"MEMBER":        {View, GuestManage},
	"CHECKIN":       {View, Checkin},
	"ANALYST":       {View, AnalyticsView},
	"GUEST":         {View},
	"VIEWER":        {View},
}

// NormalizeRole upper-cases and trims a role code and strips the INHERITED_
// prefix that CheckAccessRecursive adds to ancestor Owner/Admin roles.
func NormalizeRole(role string) string {
	return strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(role)), "INHERITED_")
}

// IsOrganizationAdmin reports whether the role is Owner or Admin, direct or
// inherited.
func IsOrganizationAdmin(role string) bool {
	code := NormalizeRole(role)
	return code == "OWNER" || code == "ADMIN"
}

// IsKnownRole reports whether the role code is one of the seeded
// organization roles.
func IsKnownRole(role string) bool {
	code := NormalizeRole(role)
	if code == "OWNER" || code == "ADMIN" {
		return true
	}
	_, ok := roleCapabilities[code]
	return ok
}

// RoleCapabilities returns what an organization role may do. Owner/Admin get
// everything. Any other non-empty, unknown code keeps the historical
// behaviour: View only. An empty code gets nothing.
func RoleCapabilities(role string) []Capability {
	code := NormalizeRole(role)
	switch {
	case code == "":
		return nil
	case code == "OWNER" || code == "ADMIN":
		return All()
	}
	if known, ok := roleCapabilities[code]; ok {
		return append([]Capability(nil), known...)
	}
	return []Capability{View}
}

// RoleHas reports whether the role holds the capability.
func RoleHas(role string, capability Capability) bool {
	for _, held := range RoleCapabilities(role) {
		if held == capability {
			return true
		}
	}
	return false
}

// EventClientCapabilities is what an EventiApp event-client grant allows on
// its one event: the event-level powers of an Event manager, without create,
// delete, members, organization, client management or metrics.
func EventClientCapabilities() []Capability {
	return []Capability{View, EventManage, GuestManage, Checkin, AnalyticsView}
}

// policyAllowList holds, per application, the session names a
// user_application_policy may contain. Only these names are governed by the
// policy ceiling; events:delete and events:clients:manage are deliberately
// outside it.
var policyAllowList = map[string][]string{
	"itbem":         {"dashboard:view", "metrics:view", "audit:view", "automation:view", "automation:manage", "platform:users:view", "organizations:view", "members:manage"},
	"eventiapp":     {"dashboard:view", "metrics:view", "events:view", "events:create", "events:manage", "guests:manage", "checkin:run", "analytics:view", "members:manage"},
	"cafettonhouse": {"dashboard:view", "metrics:view", "events:view", "events:create", "events:manage", "guests:manage", "checkin:run", "analytics:view", "members:manage"},
}

// PolicyAllowList returns the allow-listed session names for an application
// (case-insensitive code), sorted. Unknown applications have none.
func PolicyAllowList(application string) []string {
	names := append([]string(nil), policyAllowList[normalizeApplication(application)]...)
	sort.Strings(names)
	return names
}

// PolicyAllows reports whether a policy for the application may contain the
// session name.
func PolicyAllows(application, sessionName string) bool {
	for _, name := range policyAllowList[normalizeApplication(application)] {
		if name == sessionName {
			return true
		}
	}
	return false
}

// PolicyApplications lists the applications that accept policies, sorted.
func PolicyApplications() []string {
	codes := make([]string, 0, len(policyAllowList))
	for code := range policyAllowList {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

func normalizeApplication(application string) string {
	return strings.ToLower(strings.TrimSpace(application))
}
