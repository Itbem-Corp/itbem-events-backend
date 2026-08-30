package models

import (
	"encoding/json"
	"testing"
)

func TestDeliveryMetadataSerializesAsJSONObject(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"context source", DeliveryContextSource{MetadataJSON: `{"repository_role":"primary","local_change_count":2}`}},
		{"change set", DeliveryChangeSet{MetadataJSON: `{"ci":{"status":"passed"}}`}},
		{"context snapshot", DeliveryContextSnapshot{MetadataJSON: `{"revision_source":"checkpoint"}`}},
		{"evidence", DeliveryEvidence{MetadataJSON: `{"content_type":"image/png"}`}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			metadata, ok := payload["metadata"].(map[string]any)
			if !ok {
				t.Fatalf("metadata must be a JSON object, got %#v", payload["metadata"])
			}
			if len(metadata) == 0 {
				t.Fatalf("metadata unexpectedly empty: %s", encoded)
			}
		})
	}
}

func TestDeliveryMetadataFailsClosedToJSONObject(t *testing.T) {
	encoded, err := json.Marshal(DeliveryContextSource{MetadataJSON: `["not","an object"]`})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok || len(metadata) != 0 {
		t.Fatalf("invalid metadata must become {}, got %#v", payload["metadata"])
	}
}
