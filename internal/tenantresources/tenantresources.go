package tenantresources

import (
	"fmt"
	"strings"

	"events-stocks/internal/products"
	"events-stocks/models"
	"github.com/labstack/echo/v4"
)

const ContextBucketKey = "tenant_bucket"

func ParseBucketMap(value string) map[string]string {
	result := make(map[string]string)
	for _, entry := range strings.Split(value, ",") {
		parts := strings.SplitN(strings.TrimSpace(entry), "=", 2)
		if len(parts) != 2 {
			continue
		}
		tenant := strings.ToLower(strings.TrimSpace(parts[0]))
		bucket := strings.TrimSpace(parts[1])
		if tenant != "" && bucket != "" {
			result[tenant] = bucket
		}
	}
	return result
}

func ResolveBucket(cfg *models.Config, tenant string) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("configuration unavailable")
	}
	tenant = strings.ToLower(strings.TrimSpace(tenant))
	if tenant == "" {
		tenant = products.DefaultCode.String()
	}
	definition, known := products.Resolve(tenant)
	if !known {
		return "", fmt.Errorf("no registered product for tenant %s", tenant)
	}
	if definition.Code == products.DefaultCode {
		if bucket := ParseBucketMap(cfg.TenantBucketMap)["eventiapp"]; bucket != "" {
			return bucket, nil
		}
		return strings.TrimSpace(cfg.AwsBucketName), nil
	}
	bucket := ParseBucketMap(cfg.TenantBucketMap)[definition.Code.String()]
	if bucket == "" {
		return "", fmt.Errorf("no media bucket configured for tenant %s", tenant)
	}
	return bucket, nil
}

func BucketFromContext(c echo.Context) (string, error) {
	if bucket, _ := c.Get(ContextBucketKey).(string); strings.TrimSpace(bucket) != "" {
		return strings.TrimSpace(bucket), nil
	}
	cfg, _ := c.Get("config").(*models.Config)
	tenant, _ := c.Get("tenant_code").(string)
	return ResolveBucket(cfg, tenant)
}
