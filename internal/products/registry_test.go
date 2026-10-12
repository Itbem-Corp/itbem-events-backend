package products

import (
	"testing"

	"events-stocks/internal/products/core"
)

func TestProductRegistryKeepsPlatformAndEventBoundariesExplicit(t *testing.T) {
	if !AllowsPlatformAuthority("eventiapp") || !AllowsPlatformAuthority("itbem") {
		t.Fatal("platform control-plane products lost their declared authority")
	}
	if AllowsPlatformAuthority("cafettonhouse") {
		t.Fatal("Cafetton House must require explicit organization membership")
	}
	if !SupportsEventOperations("eventiapp") || SupportsEventOperations("itbem") || SupportsEventOperations("cafettonhouse") {
		t.Fatal("event operations must remain exclusive to EventiApp")
	}
	if SupportsAutomation("eventiapp") || !SupportsAutomation("itbem") || SupportsAutomation("cafettonhouse") {
		t.Fatal("automation must remain exclusive to ITBEM")
	}
	if got := NormalizeOrDefault("unknown"); got != core.EventiApp {
		t.Fatalf("default product = %q, want %q", got, core.EventiApp)
	}
}

func TestModuleRequiresOrganizationAdmin(t *testing.T) {
	tests := []struct {
		product string
		module  string
		want    bool
	}{
		{product: "eventiapp", module: "metrics", want: true},
		{product: " EventiApp ", module: " METRICS ", want: true},
		{product: "eventiapp", module: "events", want: false},
		{product: "eventiapp", module: "home", want: false},
		{product: "eventiapp", module: "", want: false},
		{product: "itbem", module: "metrics", want: false},
		{product: "cafettonhouse", module: "metrics", want: false},
		{product: "unknown", module: "metrics", want: false},
		{product: "", module: "metrics", want: false},
	}
	for _, test := range tests {
		if got := ModuleRequiresOrganizationAdmin(test.product, test.module); got != test.want {
			t.Errorf("ModuleRequiresOrganizationAdmin(%q, %q) = %v, want %v", test.product, test.module, got, test.want)
		}
	}
}
