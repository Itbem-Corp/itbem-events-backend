package models

import (
	"encoding/json"
	"testing"
)

func TestLegacyWorkItemMandateDefaultsToTwoBoundedParallelLanes(t *testing.T) {
	encoded, err := json.Marshal(DeliveryWorkItem{Title: "Parallel delivery", ExpectedOutcome: "Independent steps"})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Mandate struct {
			MaxConcurrency int `json:"max_concurrency"`
		} `json:"mandate"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("decode work item mandate: %v", err)
	}
	if response.Mandate.MaxConcurrency != DefaultDeliveryMandateMaxConcurrency {
		t.Fatalf("legacy work item max_concurrency = %d, want %d", response.Mandate.MaxConcurrency, DefaultDeliveryMandateMaxConcurrency)
	}
}
