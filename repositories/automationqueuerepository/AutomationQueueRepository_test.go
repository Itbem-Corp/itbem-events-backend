package automationqueuerepository

import (
	"events-stocks/internal/agentwork"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const completeLaneQueues = `{
	"orchestration":"https://sqs.example/queues/orchestration",
	"engineering":"https://sqs.example/queues/engineering",
	"review":"https://sqs.example/queues/review",
	"qa":"https://sqs.example/queues/qa",
	"release":"https://sqs.example/queues/release"
}`

func TestParseQueueTargetsKeepsLegacyModeBackwardCompatible(t *testing.T) {
	t.Parallel()
	targets, err := parseQueueTargets(" https://sqs.example/queues/legacy ", " https://sqs.example/queues/legacy-dlq ", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !targets.configured() || len(targets.laneURLs) != 0 {
		t.Fatalf("legacy queue targets = %#v", targets)
	}
	queueURL, err := targets.queueURLForOperation(agentwork.OperationCodeReview)
	if err != nil || queueURL != "https://sqs.example/queues/legacy" {
		t.Fatalf("legacy review route = %q, %v", queueURL, err)
	}
}

func TestParseQueueTargetsRoutesEveryOperationToItsRoleLane(t *testing.T) {
	t.Parallel()
	targets, err := parseQueueTargets("https://sqs.example/queues/legacy", "https://sqs.example/queues/legacy-dlq", completeLaneQueues, "https://sqs.example/queues/role-dlq")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		agentwork.OperationAIChat:                  "orchestration",
		agentwork.OperationDeliverySummary:         "orchestration",
		agentwork.OperationDeliveryPlan:            "engineering",
		agentwork.OperationDeliveryImplementation:  "engineering",
		agentwork.OperationCodeReview:              "review",
		agentwork.OperationDeliveryQA:              "qa",
		agentwork.OperationDeliveryOnboardingProbe: "qa",
		agentwork.OperationDeliveryPublish:         "release",
	}
	for operation, lane := range tests {
		queueURL, routeErr := targets.queueURLForOperation(operation)
		if routeErr != nil || queueURL != "https://sqs.example/queues/"+lane {
			t.Fatalf("route %s = %q, %v", operation, queueURL, routeErr)
		}
	}
}

func TestParseQueueTargetsRejectsPartialAmbiguousOrUnknownRoleMaps(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]struct {
		lanes string
		dlq   string
	}{
		"partial":        {`{"orchestration":"one"}`, "role-dlq"},
		"missing dlq":    {completeLaneQueues, ""},
		"unknown lane":   {`{"orchestration":"one","engineering":"two","review":"three","qa":"four","release":"five","admin":"six"}`, "role-dlq"},
		"duplicate lane": {`{"orchestration":"same","engineering":"same","review":"three","qa":"four","release":"five"}`, "role-dlq"},
		"multiple json":  {completeLaneQueues + `{}`, "role-dlq"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if targets, err := parseQueueTargets("legacy", "legacy-dlq", input.lanes, input.dlq); err == nil {
				t.Fatalf("invalid queue map accepted: %#v", targets)
			}
		})
	}
	if _, err := parseQueueTargets("", "orphan-dlq", "", ""); err == nil {
		t.Fatal("orphan legacy DLQ was accepted")
	}
	if _, err := parseQueueTargets("legacy", "legacy-dlq", "", "orphan-role-dlq"); err == nil {
		t.Fatal("orphan role DLQ was accepted")
	}
}

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
