package gormrepository

import "testing"

func TestValidateQueryOptionsAcceptsRepositoryContracts(t *testing.T) {
	t.Parallel()

	err := validateQueryOptions(QueryOptions{
		Filters:  map[string]interface{}{"event_id": "event-1"},
		Preload:  []string{"Patterns.Font", "Patterns.Font.Resource"},
		OrderBy:  `"order"`,
		OrderDir: "desc",
	})
	if err != nil {
		t.Fatalf("expected trusted repository options to be accepted: %v", err)
	}
}

func TestValidateQueryOptionsRejectsSQLFragments(t *testing.T) {
	t.Parallel()

	tests := []QueryOptions{
		{OrderBy: "created_at DESC; DROP TABLE events"},
		{OrderDir: "DESC; DROP TABLE events"},
		{Filters: map[string]interface{}{"id OR 1=1": "x"}},
		{Preload: []string{"User; DELETE FROM users"}},
	}
	for _, options := range tests {
		if err := validateQueryOptions(options); err == nil {
			t.Fatalf("expected invalid options to be rejected: %#v", options)
		}
	}
}

func TestSafeIdentifierPath(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"id", "event_id", `"order"`, "Patterns.Font"} {
		if !isSafeIdentifierPath(value) {
			t.Fatalf("expected %q to be accepted", value)
		}
	}
	for _, value := range []string{"", "id DESC", "id;select", "id..name", "1id"} {
		if isSafeIdentifierPath(value) {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}
