package delivery

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
)

func TestDeliveryWorkItemStreamRevisionTracksEveryObservableActivitySource(t *testing.T) {
	now := time.Date(2026, time.August, 11, 20, 0, 0, 0, time.UTC)
	source := deliveryWorkItemStreamRevisionSource{
		WorkItemID:              uuid.Must(uuid.NewV4()),
		State:                   "implementation",
		WorkItemUpdatedAt:       now,
		AutomationTaskCount:     2,
		ActiveTaskCount:         1,
		AutomationTaskUpdatedAt: sql.NullTime{Time: now.Add(time.Second), Valid: true},
		PlanCount:               1,
		PlanCreatedAt:           sql.NullTime{Time: now.Add(2 * time.Second), Valid: true},
		ExecutionCount:          1,
		ExecutionCompletedAt:    sql.NullTime{Time: now.Add(3 * time.Second), Valid: true},
	}

	first := deliveryWorkItemStreamRevision(source)
	if first == "" || first != deliveryWorkItemStreamRevision(source) {
		t.Fatal("the same visible work-item state must produce a stable revision")
	}

	source.ToolExecutionCount++
	if second := deliveryWorkItemStreamRevision(source); second == first {
		t.Fatal("a new tool execution must invalidate the delivery work item")
	}

	source.ToolExecutionCount--
	source.GateCount++
	if third := deliveryWorkItemStreamRevision(source); third == first {
		t.Fatal("a human gate must invalidate the delivery work item")
	}
}

func TestDeliveryWorkItemStreamLastActivityUsesLatestSafeTimestamp(t *testing.T) {
	now := time.Date(2026, time.August, 11, 20, 0, 0, 0, time.UTC)
	source := deliveryWorkItemStreamRevisionSource{
		WorkItemUpdatedAt:    now,
		ExecutionCompletedAt: sql.NullTime{Time: now.Add(4 * time.Second), Valid: true},
		MessageCreatedAt:     sql.NullTime{Time: now.Add(2 * time.Second), Valid: true},
	}
	if got, want := deliveryWorkItemStreamLastActivity(source), now.Add(4*time.Second).Format(time.RFC3339Nano); got != want {
		t.Fatalf("last activity = %q, want %q", got, want)
	}
}

func TestDeliveryWorkItemStreamPollIntervalPrioritizesActiveAgentWork(t *testing.T) {
	if got, want := deliveryWorkItemStreamPollIntervalFor(deliveryWorkItemStreamEvent{ActiveTasks: 1}), deliveryWorkItemStreamActivePollInterval; got != want {
		t.Fatalf("active poll interval = %s, want %s", got, want)
	}
	if got, want := deliveryWorkItemStreamPollIntervalFor(deliveryWorkItemStreamEvent{}), deliveryWorkItemStreamIdlePollInterval; got != want {
		t.Fatalf("idle poll interval = %s, want %s", got, want)
	}
}

func TestDeliveryWorkItemStreamTreatsCancellationAsAClosingState(t *testing.T) {
	query := strings.ToLower(strings.Join(strings.Fields(deliveryWorkItemStreamRevisionSQL), " "))
	for _, fragment := range []string{
		"when exists ( select 1 from automation_tasks as stopping_task",
		"stopping_task.delivery_work_item_id = work_item.id",
		"stopping_task.status = 'cancel_requested'",
		") then 0 else count(*) end",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("stream must report a requested stop as neutral closure; missing %q", fragment)
		}
	}
	if got := deliveryWorkItemStreamPollIntervalFor(deliveryWorkItemStreamEvent{}); got != deliveryWorkItemStreamIdlePollInterval {
		t.Fatalf("a closing stream must use the idle cadence, got %s", got)
	}
}

func TestDeliveryWorkItemStreamQueryStaysBoundedAndDoesNotSelectPrivateAgentData(t *testing.T) {
	query := strings.ToLower(strings.Join(strings.Fields(deliveryWorkItemStreamRevisionSQL), " "))
	for _, table := range []string{
		"delivery_work_items", "automation_tasks", "delivery_plans", "delivery_change_sets",
		"delivery_gates", "delivery_evidences", "delivery_messages", "automation_executions", "automation_tool_executions",
	} {
		if !strings.Contains(query, table) {
			t.Fatalf("stream revision must notice %s changes", table)
		}
	}
	for _, forbidden := range []string{"output_ref", "input_ref", "usage_json", "structured_json", "error_message", "request_ref", "response_ref"} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("stream revision must not read or disclose private field %q", forbidden)
		}
	}
	if !strings.Contains(query, "work_item.deleted_at is null") {
		t.Fatal("stream revision must not revive a soft-deleted work item")
	}
	if strings.Contains(query, " as grant ") || strings.Contains(query, " grant.") {
		t.Fatal("stream revision must not use the reserved SQL alias grant")
	}
	if !strings.Contains(query, "as publication_grant") {
		t.Fatal("stream revision must use a non-reserved publication grant alias")
	}
}

func TestWriteDeliveryWorkItemStreamEventUsesValidSSEFrame(t *testing.T) {
	recorder := httptest.NewRecorder()
	event := deliveryWorkItemStreamEvent{
		WorkItemID: "work-1", Revision: "revision-1", State: "planning", ActiveTasks: 1,
		LastActivityAt: "2026-08-11T20:00:00Z", GeneratedAt: "2026-08-11T20:00:01Z",
	}
	if err := writeDeliveryWorkItemStreamEvent(recorder, "update", event); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"event: update\n", "id: revision-1\n", `"work_item_id":"work-1"`, "\n\n",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("SSE frame missing %q: %q", expected, body)
		}
	}
}
