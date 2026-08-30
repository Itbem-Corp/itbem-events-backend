package itbem

import "strings"

// OwnsAutomationSurface identifies the private ITBEM control-plane API. The
// local agent authenticates separately; EventiApp never receives this surface.
func OwnsAutomationSurface(path string) bool {
	path = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(path)), "/")
	return strings.HasPrefix(path, "/api/automation")
}
