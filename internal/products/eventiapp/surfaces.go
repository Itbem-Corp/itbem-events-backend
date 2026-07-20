package eventiapp

import "strings"

var protectedSurfacePrefixes = []string{
	"/api/events", "/api/guests", "/api/moments", "/api/invitations",
	"/api/sections", "/api/tables", "/api/resources", "/api/admin/resources",
	"/api/fonts", "/api/catalogs/design-templates", "/api/catalogs/design-workspace",
	"/api/catalogs/color-palettes", "/api/catalogs/font-sets", "/api/catalogs/resource-types",
	"/api/catalogs/guest-statuses", "/api/event-types",
}

// OwnsProtectedSurface identifies routes that only exist in the EventiApp
// product domain. It is intentionally path-only: authorization happens in the
// core layer after the product boundary is established.
func OwnsProtectedSurface(path string) bool {
	path = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(path)), "/")
	for _, prefix := range protectedSurfacePrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
