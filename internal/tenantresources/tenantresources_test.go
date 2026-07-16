package tenantresources

import (
	"testing"

	"events-stocks/models"
)

func TestResolveBucketUsesPhysicalTenantBoundary(t *testing.T) {
	cfg := &models.Config{
		AwsBucketName:   "legacy-eventiapp",
		TenantBucketMap: "eventiapp=eventi-media,itbem=itbem-media,cafettonhouse=cafetton-media",
	}
	for tenant, want := range map[string]string{
		"eventiapp": "eventi-media", "itbem": "itbem-media", "cafettonhouse": "cafetton-media",
	} {
		got, err := ResolveBucket(cfg, tenant)
		if err != nil || got != want {
			t.Fatalf("tenant %s: got %q, err %v; want %q", tenant, got, err, want)
		}
	}
}

func TestResolveBucketFailsClosedForUnconfiguredBrand(t *testing.T) {
	_, err := ResolveBucket(&models.Config{AwsBucketName: "legacy"}, "cafettonhouse")
	if err == nil {
		t.Fatal("expected missing branded bucket to fail closed")
	}
}
