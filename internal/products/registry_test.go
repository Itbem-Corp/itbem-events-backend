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
	if got := NormalizeOrDefault("unknown"); got != core.EventiApp {
		t.Fatalf("default product = %q, want %q", got, core.EventiApp)
	}
}
