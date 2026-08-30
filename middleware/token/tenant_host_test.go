package token

import "testing"

func TestValidateTenantRequestHostBindsBrandedAPIs(t *testing.T) {
	configured := "api.eventiapp.com.mx=eventiapp,api.itbem.com.mx=itbem,api.cafettonhouse.com=cafettonhouse"
	for _, tc := range []struct{ host, tenant string }{
		{"api.eventiapp.com.mx:443", "eventiapp"},
		{"api.itbem.com.mx", "itbem"},
		{"api.cafettonhouse.com", "cafettonhouse"},
	} {
		if err := validateTenantRequestHost(tc.host, tc.tenant, configured); err != nil {
			t.Fatalf("%s/%s should be allowed: %v", tc.host, tc.tenant, err)
		}
	}
}

func TestValidateTenantRequestHostRejectsConfusedDeputy(t *testing.T) {
	configured := "api.itbem.com.mx=itbem,api.cafettonhouse.com=cafettonhouse"
	if err := validateTenantRequestHost("api.itbem.com.mx", "cafettonhouse", configured); err == nil {
		t.Fatal("expected cross-tenant host/token pair to be rejected")
	}
	if err := validateTenantRequestHost("unknown.example", "itbem", configured); err == nil {
		t.Fatal("expected unknown production API host to be rejected")
	}
}
