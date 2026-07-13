package app

import (
	"net/url"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"
)

// redactedRequestURI keeps route-level diagnostics without ever logging query
// values. Public invitation, preview, password-proof, and signed-access tokens
// all travel in query strings, and the legacy invitation lookup also carries a
// token in the path. Echo's registered route template removes path-parameter
// values; query names remain useful for debugging while every value is redacted.
func redactedRequestURI(c echo.Context) string {
	route := strings.TrimSpace(c.Path())
	if route == "" {
		// An unmatched path is attacker-controlled and may itself contain a
		// bearer token. Keep the 404 diagnostic without copying raw path data.
		route = "/<unmatched>"
	}

	query := c.Request().URL.Query()
	if len(query) == 0 {
		return route
	}
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for i := range keys {
		keys[i] = url.QueryEscape(keys[i]) + "=<redacted>"
	}
	return route + "?" + strings.Join(keys, "&")
}
