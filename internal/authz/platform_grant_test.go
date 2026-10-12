package authz

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"events-stocks/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seededRoleCodes mirrors seeds/ClientRoleSeed.go.
var seededRoleCodes = []string{"Owner", "Admin", "EVENT_MANAGER", "EDITOR", "CHECKIN", "ANALYST", "Member", "Guest"}

func TestPrimaryRootMayGrantEveryRole(t *testing.T) {
	for name, user := range map[string]*models.User{
		"legacy is_root":   {IsRoot: true},
		"explicit level 1": {RootLevel: models.RootLevelPrimary},
	} {
		for _, code := range seededRoleCodes {
			assert.True(t, PlatformRootMayGrantRole(user, code), "%s grants %s", name, code)
		}
	}
}

// A Root 2 may grant only roles whose capabilities it holds itself, so it can
// never reach event structure or organization governance through an account
// it controls.
func TestOperationalRootMayGrantOnlyRolesWithinItsCapabilities(t *testing.T) {
	expected := map[string]bool{
		"Owner":         false,
		"Admin":         false,
		"EVENT_MANAGER": false,
		"EDITOR":        false,
		"CHECKIN":       true,
		"ANALYST":       true,
		"Member":        true,
		"Guest":         true,
		// The membership projection reports inherited roles with a prefix.
		"INHERITED_ADMIN": false,
		"inherited_owner": false,
	}
	for name, user := range map[string]*models.User{
		"explicit level 2":       {RootLevel: models.RootLevelOperational},
		"legacy is_root level 2": {IsRoot: true, RootLevel: models.RootLevelOperational},
	} {
		for code, allowed := range expected {
			assert.Equal(t, allowed, PlatformRootMayGrantRole(user, code), "%s grants %s", name, code)
		}
	}
}

func TestNonRootMayNotGrantThroughPlatformAuthority(t *testing.T) {
	for _, code := range seededRoleCodes {
		assert.False(t, PlatformRootMayGrantRole(&models.User{}, code), "standard user grants %s", code)
		assert.False(t, PlatformRootMayGrantRole(nil, code), "nil user grants %s", code)
	}
}

// PlatformRootMayGrantRole checks every capability in allCapabilities; a
// capability missing from that list would let Root 2 grant a role that
// carries it. Keep the list equal to the declared Capability constants.
func TestAllCapabilitiesListsEveryDeclaredCapability(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "authz.go", nil, 0)
	require.NoError(t, err)

	declared := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			if ident, ok := value.Type.(*ast.Ident); !ok || ident.Name != "Capability" {
				continue
			}
			for _, expr := range value.Values {
				literal, ok := expr.(*ast.BasicLit)
				require.True(t, ok, "Capability constants must be string literals")
				unquoted, err := strconv.Unquote(literal.Value)
				require.NoError(t, err)
				declared[unquoted] = true
			}
		}
	}
	require.NotEmpty(t, declared)

	listed := map[string]bool{}
	for _, capability := range allCapabilities {
		listed[string(capability)] = true
	}
	assert.Equal(t, declared, listed)
}
