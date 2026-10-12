package eventiapp

import "testing"

func TestParseEventClientAccess(t *testing.T) {
	for raw, want := range map[string]bool{
		"on": true, " ON ": true,
		"": false, "off": false, "OFF": false,
		"true": false, "1": false, "yes": false, "onn": false,
	} {
		if got := ParseEventClientAccess(raw); got != want {
			t.Fatalf("ParseEventClientAccess(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestEventClientAccessDefaultsOff(t *testing.T) {
	defer SetEventClientAccess(false)
	if EventClientAccessEnabled() {
		t.Fatal("event client access must default to off")
	}
	SetEventClientAccess(true)
	if !EventClientAccessEnabled() {
		t.Fatal("flag did not switch on")
	}
}
