package models

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

func TestDeliveryPlanStepEventAttributionComesFromTransactionContext(t *testing.T) {
	instanceID := uuid.Must(uuid.NewV4())
	event := DeliveryPlanStepEvent{}
	tx := &gorm.DB{Statement: &gorm.Statement{Context: WithDeliveryPlanStepAgentInstanceID(context.Background(), instanceID)}}
	if err := event.BeforeCreate(tx); err != nil {
		t.Fatal(err)
	}
	if event.AgentInstanceID == nil || *event.AgentInstanceID != instanceID {
		t.Fatalf("verified transaction identity was not attached to lifecycle event: %#v", event.AgentInstanceID)
	}

	withoutIdentity := DeliveryPlanStepEvent{}
	if err := withoutIdentity.BeforeCreate(&gorm.DB{Statement: &gorm.Statement{Context: context.Background()}}); err != nil {
		t.Fatal(err)
	}
	if withoutIdentity.AgentInstanceID != nil {
		t.Fatalf("unattributed event unexpectedly received an instance ID: %#v", withoutIdentity.AgentInstanceID)
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), instanceID.String()) || strings.Contains(string(encoded), "agent_instance_id") {
		t.Fatalf("model JSON exposed internal attribution: %s", encoded)
	}
}
