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

func TestProductDefinitionsDoNotExposeRegistryModuleSlices(t *testing.T) {
	all := All()
	all[0].Modules[0] = "mutated"

	resolved, ok := Resolve(all[0].Code.String())
	if !ok {
		t.Fatalf("product %q was not resolvable", all[0].Code)
	}
	if resolved.Modules[0] == "mutated" {
		t.Fatal("All exposed the registry's module slice")
	}

	resolved.Modules[0] = "mutated-again"
	again, ok := Resolve(resolved.Code.String())
	if !ok {
		t.Fatalf("product %q was not resolvable", resolved.Code)
	}
	if again.Modules[0] == "mutated-again" {
		t.Fatal("Resolve exposed the registry's module slice")
	}
}
