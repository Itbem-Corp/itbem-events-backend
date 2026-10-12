// Package products is the backend product boundary. Product folders own their
// capabilities; core middleware, cache, queues, and controllers consume this
// registry instead of scattering tenant string comparisons.
package products

import (
	"strings"

	"events-stocks/internal/products/cafettonhouse"
	"events-stocks/internal/products/core"
	"events-stocks/internal/products/eventiapp"
	"events-stocks/internal/products/itbem"
)

const DefaultCode = core.EventiApp

var orderedDefinitions = []core.Definition{
	eventiapp.Definition,
	itbem.Definition,
	cafettonhouse.Definition,
}

var definitions = func() map[core.Code]core.Definition {
	items := make(map[core.Code]core.Definition, len(orderedDefinitions))
	for _, definition := range orderedDefinitions {
		items[definition.Code] = definition
	}
	return items
}()

// All returns a copy in a stable order for setup code such as application
// seeding. Callers must not mutate the returned product catalog.
func All() []core.Definition {
	items := make([]core.Definition, len(orderedDefinitions))
	copy(items, orderedDefinitions)
	return items
}

func Resolve(value string) (core.Definition, bool) {
	definition, ok := definitions[core.Normalize(value)]
	return definition, ok
}

func NormalizeOrDefault(value string) core.Code {
	if definition, ok := Resolve(value); ok {
		return definition.Code
	}
	return DefaultCode
}

func AllowsPlatformAuthority(value string) bool {
	definition, ok := Resolve(value)
	return ok && definition.AllowsPlatformAuthority
}

func SupportsEventOperations(value string) bool {
	definition, ok := Resolve(value)
	return ok && definition.SupportsEventOperations
}

func SupportsAutomation(value string) bool {
	definition, ok := Resolve(value)
	return ok && definition.SupportsAutomation
}

// organizationAdminModules maps each product to the modules it reserves for
// organization Owner/Admin and platform roots. Products without an entry keep
// role-based module access. It is not a core.Definition field because the
// definition mirrors the pinned product contract.
var organizationAdminModules = map[core.Code][]string{
	core.EventiApp: eventiapp.OrganizationAdminModules,
}

// ModuleRequiresOrganizationAdmin reports whether the product reserves the
// module (case-insensitive) for organization Owner/Admin, direct or inherited,
// and platform roots. Unknown products and modules return false.
func ModuleRequiresOrganizationAdmin(product, module string) bool {
	definition, ok := Resolve(product)
	if !ok {
		return false
	}
	module = strings.TrimSpace(module)
	if module == "" {
		return false
	}
	for _, reserved := range organizationAdminModules[definition.Code] {
		if strings.EqualFold(strings.TrimSpace(reserved), module) {
			return true
		}
	}
	return false
}

// RequiresEventOperationsPath keeps EventiApp-owned HTTP surfaces in the
// EventiApp module while callers keep a generic product-boundary check.
func RequiresEventOperationsPath(path string) bool {
	return eventiapp.OwnsProtectedSurface(path)
}

// RequiresAutomationPath keeps orchestration and local-agent operations out
// of the EventiApp API domain even though both products share core services.
func RequiresAutomationPath(path string) bool {
	return itbem.OwnsAutomationSurface(path)
}
