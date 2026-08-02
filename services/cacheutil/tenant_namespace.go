package cacheutil

import (
	"fmt"
	"strings"

	"events-stocks/internal/products"
)

const CacheKeyVersion = "v1"

// TenantNamespace makes product and subject boundaries visible in every
// personalized Redis key. Shared catalogs deliberately keep their existing
// global keys; they contain no user or organization-specific data.
func TenantNamespace(tenantCode, subject string) string {
	return fmt.Sprintf("%s:tenant:%s:%s", CacheKeyVersion, products.NormalizeOrDefault(tenantCode), strings.Trim(strings.TrimSpace(subject), ":"))
}

func TenantKey(tenantCode, subject, resource string) string {
	return TenantNamespace(tenantCode, subject) + ":" + strings.Trim(strings.TrimSpace(resource), ":")
}
