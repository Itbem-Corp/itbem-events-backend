package delivery

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
)

func TestBuildAutomationPortfolioIsCompactAndRevisionStable(t *testing.T) {
	projectID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	workItemID := uuid.Must(uuid.NewV4())
	taskID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.August, 12, 15, 4, 5, 0, time.UTC)
	completedAt := now.Add(-time.Minute)
	publishedAt := now.Add(-30 * time.Second)

	input := automationPortfolioBuildInput{
		GeneratedAt: now,
		Projects: []models.DeliveryProject{{
			ID: projectID, ClientID: clientID, Name: "Portal de aliados", Status: "active", UpdatedAt: now,
			Client: models.Client{ID: clientID, Name: "ITBEM"},
		}},
		WorkItems: []automationPortfolioWorkItemRow{{
			ID: workItemID, ProjectID: projectID, Title: "Validar el portal", State: "qa_review", CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		}},
		Tasks: []automationPortfolioTaskRow{{
			ID: taskID, DeliveryWorkItemID: workItemID, Operation: "delivery.qa", Status: "completed", AttemptCount: 2,
			CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now, CompletedAt: &completedAt,
		}},
		WorkItemTotals: []automationPortfolioWorkItemTotals{{
			ProjectID: projectID, WorkItemCount: 26, ActiveWorkItems: 3, DecisionsRequired: 1, BlockedWorkItems: 1,
		}},
		ProjectTaskTotals: []automationPortfolioProjectTaskTotals{{
			ProjectID: projectID, AutomationTasks: 17, QueuedTasks: 1, RunningTasks: 2, AttentionTasks: 1,
		}},
		WorkItemTaskTotals: []automationPortfolioWorkItemTaskTotals{{WorkItemID: workItemID, AutomationTasks: 13}},
		GateTotals:         []automationPortfolioGateTotals{{WorkItemID: workItemID, Total: 2, Approved: 1, ChangesRequested: 1}},
		EvidenceTotals:     []automationPortfolioEvidenceTotals{{WorkItemID: workItemID, EvidenceCount: 4}},
		ReviewQueue: []automationPortfolioReview{{
			TaskID: uuid.Must(uuid.NewV4()), Repository: "itbem/example", PullRequest: 42, HeadSHA: strings.Repeat("a", 40),
			Status: "completed", AttemptCount: 1, Verdict: "approve", Event: "APPROVE",
			ReviewURL: "https://github.com/itbem/example/pull/42#pullrequestreview-77", ReviewerActor: "reviewer-bot[bot]",
			CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now, CompletedAt: &completedAt, PublishedAt: &publishedAt,
		}},
	}

	snapshot := buildAutomationPortfolio(input)
	if snapshot.SchemaVersion != automationPortfolioSchemaVersion || snapshot.Totals.WorkItems != 26 || snapshot.Totals.RunningTasks != 2 {
		t.Fatalf("unexpected portfolio totals: %#v", snapshot)
	}
	if len(snapshot.Projects) != 1 || len(snapshot.Projects[0].WorkItems) != 1 {
		t.Fatalf("unexpected portfolio shape: %#v", snapshot.Projects)
	}
	if len(snapshot.ReviewQueue) != 1 || snapshot.Totals.ReviewTasks != 1 || snapshot.Totals.PublishedReviews != 1 {
		t.Fatalf("safe review queue was lost: %#v / %#v", snapshot.ReviewQueue, snapshot.Totals)
	}
	project := snapshot.Projects[0]
	item := project.WorkItems[0]
	if !project.WorkItemsTruncated || !item.AutomationTasksTruncated {
		t.Fatalf("truncation must remain explicit: %#v / %#v", project, item)
	}
	if item.GateSummary != (automationPortfolioGateSummary{Total: 2, Approved: 1, ChangesRequested: 1}) || item.EvidenceCount != 4 {
		t.Fatalf("safe gate/evidence summaries were lost: %#v", item)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"input_ref", "output_ref", "error_message", "reference", "description", "decided_by", "captured_by"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("portfolio response leaked %q: %s", forbidden, encoded)
		}
	}

	later := input
	later.GeneratedAt = now.Add(30 * time.Second)
	if next := buildAutomationPortfolio(later); next.Revision != snapshot.Revision {
		t.Fatalf("revision changed only because generated_at changed: %q != %q", next.Revision, snapshot.Revision)
	}
	changed := input
	changed.Tasks = append([]automationPortfolioTaskRow(nil), input.Tasks...)
	changed.Tasks[0].Status = "failed"
	if next := buildAutomationPortfolio(changed); next.Revision == snapshot.Revision {
		t.Fatal("revision did not change after a visible task status changed")
	}
	changedReview := input
	changedReview.ReviewQueue = append([]automationPortfolioReview(nil), input.ReviewQueue...)
	changedReview.ReviewQueue[0].Status = "failed"
	if next := buildAutomationPortfolio(changedReview); next.Revision == snapshot.Revision {
		t.Fatal("revision did not change after a visible review status changed")
	}
}

func TestAutomationPortfolioReviewProjectionRejectsForgedOrMismatchedIdentity(t *testing.T) {
	now := time.Now().UTC()
	publishedAt := now.Add(-time.Minute)
	row := automationPortfolioReviewRow{
		TaskID: uuid.Must(uuid.NewV4()), CorrelationID: "github-pr:itbem/example:42:" + strings.Repeat("b", 40),
		Status: "completed", AttemptCount: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, CompletedAt: &now,
		PublicationRepository: "itbem/example", PublicationPullRequest: 42, PublicationHeadSHA: strings.Repeat("b", 40),
		Verdict: "approve", Event: "APPROVE", ReviewID: 77, ReviewURL: "https://github.com/itbem/example/pull/42#pullrequestreview-77",
		ReviewerActor: "reviewer-bot[bot]", PublishedAt: &publishedAt,
	}
	review, ok := automationPortfolioReviewFromRow(row)
	if !ok || review.Repository != "itbem/example" || review.ReviewURL == "" {
		t.Fatalf("valid public review projection rejected: %#v / %v", review, ok)
	}
	for name, mutate := range map[string]func(*automationPortfolioReviewRow){
		"unsafe correlation": func(value *automationPortfolioReviewRow) {
			value.CorrelationID = "github-pr:../secret:42:" + strings.Repeat("b", 40)
		},
		"wrong head":  func(value *automationPortfolioReviewRow) { value.PublicationHeadSHA = strings.Repeat("c", 40) },
		"wrong event": func(value *automationPortfolioReviewRow) { value.Event = "REQUEST_CHANGES" },
		"forged URL": func(value *automationPortfolioReviewRow) {
			value.ReviewURL = "https://example.com/itbem/example/pull/42#pullrequestreview-77"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := row
			mutate(&candidate)
			if _, ok := automationPortfolioReviewFromRow(candidate); ok {
				t.Fatal("invalid review queue identity was projected")
			}
		})
	}
}

func TestAutomationPortfolioMembershipRequiresDeliveryView(t *testing.T) {
	ownerID := uuid.Must(uuid.NewV4())
	viewerID := uuid.Must(uuid.NewV4())
	explicitID := uuid.Must(uuid.NewV4())
	blockedID := uuid.Must(uuid.NewV4())
	ids := automationPortfolioProjectIDsFromMemberships([]models.DeliveryProjectMember{
		{ProjectID: ownerID, Role: "owner"},
		{ProjectID: viewerID, Role: "viewer"},
		{ProjectID: explicitID, Role: "custom", Permissions: `["delivery:view"]`},
		{ProjectID: blockedID, Role: "custom", Permissions: `["delivery:manage"]`},
	})
	if len(ids) != 3 || ids[0] != ownerID || ids[1] != viewerID || ids[2] != explicitID {
		t.Fatalf("unexpected viewable project IDs: %#v", ids)
	}
}

func TestAutomationPortfolioOptionalSummaryFallbackOnlyHandlesMissingOptionalTable(t *testing.T) {
	if !automationPortfolioOptionalSummaryUnavailable(errors.New(`ERROR: relation "delivery_evidences" does not exist (SQLSTATE 42P01)`), "delivery_evidences") {
		t.Fatal("missing optional evidence table should be surfaced as an explicit partial summary")
	}
	for _, test := range []struct {
		err   error
		table string
	}{
		{errors.New("connection refused"), "delivery_evidences"},
		{errors.New(`ERROR: relation "delivery_gates" does not exist (SQLSTATE 42P01)`), "delivery_evidences"},
		{errors.New(`ERROR: column "decision" does not exist (SQLSTATE 42703)`), "delivery_gates"},
	} {
		if automationPortfolioOptionalSummaryUnavailable(test.err, test.table) {
			t.Fatalf("unexpected optional fallback for %v / %s", test.err, test.table)
		}
	}
}

func TestAutomationPortfolioProjectTaskTotalsOnlyCountLatestFailedOperationAttempts(t *testing.T) {
	query := automationPortfolioProjectTaskTotalsQuery()
	for _, fragment := range []string{
		"PARTITION BY task.delivery_work_item_id, task.operation",
		"WHERE operation_attempt_rank = 1",
		"latest_task.status IN ('failed', 'dispatch_failed')",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("task summary query must preserve latest-attempt semantics; missing %q", fragment)
		}
	}
	if strings.Contains(query, "'cancel_requested') THEN 1") {
		t.Fatal("a cancellation request must not be counted as attention")
	}
	for _, fragment := range []string{
		"WHEN task.status = 'queued'",
		"WHEN task.status = 'running'",
		"NOT EXISTS",
		"stopping_task.delivery_work_item_id = work_item.id",
		"stopping_task.status = 'cancel_requested'",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("a closing work item must not inflate active task totals; missing %q", fragment)
		}
	}
}

func TestAutomationPortfolioWorkItemTotalsDoNotCountClosingRunsAsActive(t *testing.T) {
	query := automationPortfolioWorkItemTotalsQuery()
	for _, fragment := range []string{
		"NOT EXISTS",
		"stopping_task.delivery_work_item_id = work_item.id",
		"stopping_task.status = 'cancel_requested'",
		"AS active_work_items",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("active work-item query must exclude safe closures; missing %q", fragment)
		}
	}
}
