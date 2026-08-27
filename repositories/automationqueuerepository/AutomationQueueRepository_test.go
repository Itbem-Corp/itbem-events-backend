package automationqueuerepository

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func TestQueueAttributeCountFailsClosedForMissingMalformedOrNegativeValues(t *testing.T) {
	name := types.QueueAttributeNameApproximateNumberOfMessages
	for _, sample := range []struct {
		value string
		ok    bool
	}{
		{"0", true}, {"14", true}, {"", false}, {"unknown", false}, {"-1", false},
	} {
		count, ok := queueAttributeCount(map[string]string{string(name): sample.value}, name)
		if ok != sample.ok || (ok && count < 0) {
			t.Fatalf("queue count %q = %d, %v", sample.value, count, ok)
		}
	}
}

func TestQueueCountsRequireEveryApproximateCounter(t *testing.T) {
	attributes := map[string]string{
		string(types.QueueAttributeNameApproximateNumberOfMessages):           "5",
		string(types.QueueAttributeNameApproximateNumberOfMessagesNotVisible): "2",
		string(types.QueueAttributeNameApproximateNumberOfMessagesDelayed):    "1",
	}
	visible, inFlight, delayed, ok := queueCountsFromAttributes(attributes)
	if !ok || visible != 5 || inFlight != 2 || delayed != 1 {
		t.Fatalf("queue counts = %d, %d, %d, %v", visible, inFlight, delayed, ok)
	}
	delete(attributes, string(types.QueueAttributeNameApproximateNumberOfMessagesDelayed))
	if _, _, _, ok := queueCountsFromAttributes(attributes); ok {
		t.Fatal("partial queue attributes must not be reported as healthy telemetry")
	}
}

func TestValidateRejectsMessagesTheWorkerWouldNeverBeAllowedToConsume(t *testing.T) {
	valid := Message{SchemaVersion: 1, JobID: "job", TenantCode: "itbem", Type: "ai.local.process"}
	valid.Payload.TaskID, valid.Payload.Operation, valid.Payload.Attempt = "task", "code.review", 1
	if err := Validate(valid); err != nil {
		t.Fatalf("valid review message rejected: %v", err)
	}
	for _, mutate := range []func(*Message){
		func(message *Message) { message.Payload.Attempt = 0 },
		func(message *Message) { message.Payload.Operation = "shell.execute" },
		func(message *Message) { message.Payload.Operation = " Code.review" },
	} {
		candidate := valid
		mutate(&candidate)
		if err := Validate(candidate); err == nil {
			t.Fatalf("invalid automation message was admitted: %#v", candidate)
		}
	}
}
