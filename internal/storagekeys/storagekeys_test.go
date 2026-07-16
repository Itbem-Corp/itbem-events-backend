package storagekeys

import "testing"

func TestNamespace(t *testing.T) {
	if got := Namespace("organizations/org-1/moments/event/raw/a.jpg"); got != "organizations/org-1/" {
		t.Fatalf("unexpected namespace %q", got)
	}
	if got := Namespace("moments/event/raw/a.jpg"); got != "" {
		t.Fatalf("legacy key should have no namespace, got %q", got)
	}
}

func TestScoped(t *testing.T) {
	if got := Scoped("organizations/org-1/", "events/e/covers/j.webp"); got != "organizations/org-1/events/e/covers/j.webp" {
		t.Fatalf("unexpected scoped key %q", got)
	}
}
