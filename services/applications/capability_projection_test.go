package applications

import (
	"strings"
	"testing"

	"events-stocks/internal/capabilities"
	"events-stocks/internal/products"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyOrganizationCapabilities is a frozen copy of the hand-written table
// that organizationCapabilities replaced. The golden test below proves the
// derived projection is byte-identical for every role and product.
func legacyOrganizationCapabilities(role string) []string {
	code := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(role)), "INHERITED_")
	capabilities := newCapabilitySet("organizations:view")
	switch code {
	case "OWNER", "ADMIN":
		capabilities.add(
			"organizations:manage", "members:manage",
			"events:view", "events:create", "events:manage", "events:delete",
			"guests:manage", "checkin:run", "analytics:view", "metrics:view",
		)
	case "EVENT_MANAGER":
		capabilities.add("events:view", "events:create", "events:manage", "guests:manage", "checkin:run", "analytics:view", "metrics:view")
	case "EDITOR":
		capabilities.add("events:view", "events:manage", "guests:manage", "analytics:view", "metrics:view")
	case "MEMBER":
		capabilities.add("events:view", "guests:manage")
	case "CHECKIN":
		capabilities.add("events:view", "checkin:run")
	case "ANALYST":
		capabilities.add("events:view", "analytics:view", "metrics:view")
	case "GUEST", "VIEWER":
		capabilities.add("events:view")
	}
	return capabilities.values()
}

func legacyApplicationOrganizationCapabilities(application models.Application, role string) []string {
	capabilities := newCapabilitySet(legacyOrganizationCapabilities(role)...)
	if !moduleEnabled(application.Modules, "events") {
		capabilities.remove("events:view", "events:create", "events:manage", "events:delete", "guests:manage", "checkin:run", "analytics:view")
	}
	if !moduleEnabled(application.Modules, "organizations") {
		capabilities.remove("organizations:view", "organizations:manage")
	}
	if !moduleEnabled(application.Modules, "metrics") {
		capabilities.remove("metrics:view")
	}
	if !moduleEnabled(application.Modules, "automation") {
		capabilities.remove("automation:view", "automation:manage")
	}
	if products.ModuleRequiresOrganizationAdmin(application.Code, "metrics") && !isOrganizationAdminRole(role) {
		capabilities.remove("metrics:view")
	}
	return capabilities.values()
}

var projectionRoles = []string{
	"OWNER", "Owner", "ADMIN", "Admin", "INHERITED_OWNER", "INHERITED_ADMIN", "inherited_admin",
	"EVENT_MANAGER", "EDITOR", "MEMBER", "CHECKIN", "ANALYST", "GUEST", "VIEWER", "Viewer",
	"LEGACY_X", "", "  ",
}

func productApplications(t *testing.T) []models.Application {
	t.Helper()
	result := make([]models.Application, 0, 3)
	for _, code := range []string{"eventiapp", "itbem", "cafettonhouse"} {
		definition, ok := products.Resolve(code)
		require.True(t, ok, code)
		result = append(result, models.Application{
			Code:                definition.Code.String(),
			Modules:             models.StringList(definition.Modules),
			AllowsPlatformAdmin: definition.AllowsPlatformAuthority,
		})
	}
	// Every module enabled, so no capability is hidden by a module filter.
	result = append(result, models.Application{
		Code:    "eventiapp",
		Modules: models.StringList{"home", "events", "organizations", "metrics", "automation", "users"},
	})
	return result
}

func TestOrganizationCapabilitiesProjectionIsByteIdenticalToTheLegacyTable(t *testing.T) {
	for _, role := range projectionRoles {
		assert.Equal(t, legacyOrganizationCapabilities(role), organizationCapabilities(role), "role %q", role)
	}
}

func TestApplicationOrganizationCapabilitiesAreByteIdenticalPerProduct(t *testing.T) {
	for _, application := range productApplications(t) {
		for _, role := range projectionRoles {
			assert.Equal(t,
				legacyApplicationOrganizationCapabilities(application, role),
				applicationOrganizationCapabilities(application, role),
				"product %s modules %v role %q", application.Code, application.Modules, role,
			)
		}
	}
}

func TestUnknownRoleCodesKeepOnlyOrganizationsView(t *testing.T) {
	assert.Equal(t, []string{"organizations:view"}, organizationCapabilities("LEGACY_X"))
	assert.Equal(t, []string{"organizations:view"}, organizationCapabilities(""))
}

func TestEventClientCapabilityIsProjectedOnlyWhenEnabled(t *testing.T) {
	eventiapp := models.Application{Code: "eventiapp", Modules: models.StringList{"events", "organizations"}}
	for _, role := range projectionRoles {
		assert.NotContains(t, applicationOrganizationCapabilities(eventiapp, role), "events:clients:manage", role)
	}
	assert.Contains(t, withEventClientCapability(eventiapp, "EVENT_MANAGER", nil), "events:clients:manage")
	assert.Contains(t, withEventClientCapability(eventiapp, "INHERITED_OWNER", nil), "events:clients:manage")
	assert.NotContains(t, withEventClientCapability(eventiapp, "EDITOR", nil), "events:clients:manage")
	assert.NotContains(t, withEventClientCapability(eventiapp, "LEGACY_X", nil), "events:clients:manage")
	noEvents := models.Application{Code: "cafettonhouse", Modules: models.StringList{"organizations"}}
	assert.NotContains(t, withEventClientCapability(noEvents, "EVENT_MANAGER", nil), "events:clients:manage")
}

// Every session name the session derives for a role must be the projection of
// an authz capability the same role holds (plus the documented extras), so the
// dashboard never shows an action the server would refuse.
func TestSessionOrganizationCapabilitiesMatchAuthzRoleDecisions(t *testing.T) {
	sessionOnly := map[string]bool{"organizations:view": true}
	for _, role := range projectionRoles {
		for _, name := range organizationCapabilities(role) {
			if sessionOnly[name] {
				continue
			}
			found := false
			for _, capability := range capabilities.All() {
				projected, _ := capabilities.SessionName(capability)
				if projected == name {
					found = true
					if name == "metrics:view" {
						assert.True(t, capabilities.RoleHas(role, capabilities.AnalyticsView) || capabilities.RoleHas(role, capabilities.MetricsView), role)
					} else {
						assert.True(t, capabilities.RoleHas(role, capability), "role %q session %s", role, name)
					}
				}
			}
			assert.True(t, found, "role %q session name %s has no capability", role, name)
		}
	}
}

func TestCapGovernedCapabilitiesOnlyRemovesAllowListedNames(t *testing.T) {
	ceiling := map[string]struct{}{"events:view": {}}
	current := []string{"events:delete", "events:manage", "events:view", "guests:manage", "organizations:view"}
	assert.Equal(t,
		[]string{"events:delete", "events:view", "organizations:view"},
		capGovernedCapabilities("eventiapp", current, ceiling),
	)
}

func TestSessionPolicyCeilingAccessors(t *testing.T) {
	var missing *Session
	_, restricted := missing.PolicyCeiling()
	assert.False(t, restricted)
	assert.Equal(t, "", missing.ApplicationCode())

	session := &Session{Application: models.Application{Code: "EventiApp"}}
	_, restricted = session.PolicyCeiling()
	assert.False(t, restricted)
	assert.Equal(t, "eventiapp", session.ApplicationCode())

	session.policyRestricted = true
	session.policyCapabilities = map[string]struct{}{"guests:manage": {}}
	names, restricted := session.PolicyCeiling()
	assert.True(t, restricted)
	assert.Contains(t, names, "guests:manage")
}

func expectUserApplicationPolicy(mock sqlmock.Sqlmock, userID, applicationID uuid.UUID, allowed string) {
	mock.ExpectQuery(`SELECT \* FROM "user_application_policies"`).WithArgs(userID, applicationID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "application_id", "is_active", "capabilities"}).
			AddRow(uuid.Must(uuid.NewV4()), userID, applicationID, true, allowed))
}

func resolveManagerWithPolicy(t *testing.T, mode capabilities.Mode) *Session {
	t.Helper()
	db, mock := applicationServiceTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	appID := expectApplicationWithModules(mock, "eventiapp", true, `["home","events","organizations"]`)
	expectOrganizations(mock, appID, userID, sqlmock.NewRows([]string{"id", "name", "code", "logo", "access_role"}).
		AddRow(uuid.Must(uuid.NewV4()), "Org", "org", "", "EVENT_MANAGER"))
	expectUserApplicationPolicy(mock, userID, appID, `["events:view","events:manage"]`)
	service := NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsActive: true}, nil
	})
	service.ConfigurePolicyCeiling(mode)
	session, err := service.Resolve("manager-sub", "eventiapp")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	return session
}

func TestResolveExposesThePolicyCeilingInEveryMode(t *testing.T) {
	for _, mode := range []capabilities.Mode{capabilities.ModeOff, capabilities.ModeShadow, capabilities.ModeEnforce} {
		session := resolveManagerWithPolicy(t, mode)
		names, restricted := session.PolicyCeiling()
		assert.True(t, restricted, mode)
		assert.Len(t, names, 2, mode)
		// Top-level capabilities were already intersected before this change.
		assert.NotContains(t, session.Capabilities, "guests:manage", mode)
	}
}

func TestResolveCapsOrganizationCapabilitiesOnlyInEnforce(t *testing.T) {
	for _, mode := range []capabilities.Mode{capabilities.ModeOff, capabilities.ModeShadow} {
		session := resolveManagerWithPolicy(t, mode)
		assert.Contains(t, session.Organizations[0].Capabilities, "guests:manage", mode)
	}
	session := resolveManagerWithPolicy(t, capabilities.ModeEnforce)
	organization := session.Organizations[0].Capabilities
	assert.NotContains(t, organization, "guests:manage")
	assert.NotContains(t, organization, "checkin:run")
	assert.Contains(t, organization, "events:manage")
	assert.Contains(t, organization, "organizations:view", "names outside the allow-list are never capped")
}
