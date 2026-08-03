// Package products is the backend product boundary. Product folders own their
// capabilities; core middleware, cache, queues, and controllers consume this
// registry instead of scattering tenant string comparisons.
package products

import (
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

// All returns independent definitions in a stable order for setup code such as
// application seeding. Product metadata is a core registry, so callers cannot
// accidentally mutate the catalog that later authorization decisions use.
func All() []core.Definition {
	items := make([]core.Definition, len(orderedDefinitions))
	for i, definition := range orderedDefinitions {
		items[i] = cloneDefinition(definition)
	}
	return items
}

func Resolve(value string) (core.Definition, bool) {
	definition, ok := definitions[core.Normalize(value)]
	return cloneDefinition(definition), ok
}

func cloneDefinition(definition core.Definition) core.Definition {
	definition.Modules = append([]string(nil), definition.Modules...)
	return definition
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

// RequiresEventOperationsPath keeps EventiApp-owned HTTP surfaces in the
// EventiApp module while callers keep a generic product-boundary check.
func RequiresEventOperationsPath(path string) bool {
	return eventiapp.OwnsProtectedSurface(path)
}
