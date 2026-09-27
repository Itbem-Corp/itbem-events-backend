package delivery

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
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
	if item.WorkflowProjection.Stage != "qa" || item.WorkflowProjection.StateKind != "review" || item.WorkflowProjection.Evidence.Total != 4 || !item.WorkflowProjection.Evidence.HasHumanGate {
		t.Fatalf("portfolio must expose the same server state projection: %#v", item.WorkflowProjection)
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

func TestAutomationPortfolioReviewQueueKeepsValidReviewsWhenHistoricalRowsAreUnsafe(t *testing.T) {
	now := time.Now().UTC()
	valid := automationPortfolioReviewRow{
		TaskID: uuid.Must(uuid.NewV4()), CorrelationID: "github-pr:itbem/example:42:" + strings.Repeat("b", 40),
		Status: "failed", AttemptCount: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	unsafe := valid
	unsafe.TaskID = uuid.Must(uuid.NewV4())
	unsafe.CorrelationID = "github-pr:../private:42:" + strings.Repeat("b", 40)

	input := automationPortfolioBuildInput{ReviewQueue: []automationPortfolioReview{}}
	appendAutomationPortfolioReviews(&input, []automationPortfolioReviewRow{unsafe, valid, unsafe})

	if len(input.ReviewQueue) != 1 || input.ReviewQueue[0].TaskID != valid.TaskID {
		t.Fatalf("valid review was hidden by an unsafe historical row: %#v", input.ReviewQueue)
	}
	if len(input.SummarySourcesUnavailable) != 1 || input.SummarySourcesUnavailable[0] != "review_queue" {
		t.Fatalf("partial review queue was not made explicit: %#v", input.SummarySourcesUnavailable)
	}
}

func TestAutomationPortfolioProjectSignalsAreAllowlistedNormalizedAndRedacted(t *testing.T) {
	projectID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	input := automationPortfolioBuildInput{
		GeneratedAt: time.Date(2026, time.August, 12, 15, 4, 5, 0, time.UTC),
		Projects: []models.DeliveryProject{{
			ID: projectID, ClientID: clientID, Name: "Project", Status: "active",
			Client: models.Client{ID: clientID, Name: "Client"},
		}},
		ProjectSignals: []automationPortfolioProjectSignalRow{
			{ProjectID: projectID, Kind: "runbook", Technologies: " Go API ; Next.js, go api\nC#; API_KEY=private-signal-canary; https://private.example/path; owner@example.test; /Users/alice/.ssh/id_rsa"},
			{ProjectID: projectID, Kind: "repository", RuntimeHintsJSON: `[" go ","Docker Compose","PostgreSQL 17","ghp_veryLongPrivateTokenCanary"]`},
			{ProjectID: projectID, Kind: "environment", Technologies: "must-not-appear", RuntimeHintsJSON: `["must-not-appear-either"]`},
		},
	}

	snapshot := buildAutomationPortfolio(input)
	if snapshot.SchemaVersion != 5 {
		t.Fatalf("portfolio schema version should reflect the new projection, got %d", snapshot.SchemaVersion)
	}
	if len(snapshot.Projects) != 1 {
		t.Fatalf("expected one project, got %#v", snapshot.Projects)
	}
	project := snapshot.Projects[0]
	if got, want := strings.Join(project.TechnologyTags, "|"), "Go API|Next.js|C#"; got != want {
		t.Fatalf("unexpected technology tags: got %q, want %q", got, want)
	}
	if got, want := strings.Join(project.RuntimeHints, "|"), "go|Docker Compose|PostgreSQL 17"; got != want {
		t.Fatalf("unexpected runtime hints: got %q, want %q", got, want)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"private-signal-canary", "private.example", "owner@example.test", "/Users/alice", "ghp_veryLongPrivateTokenCanary",
		"must-not-appear", "metadata_json", "reference",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("portfolio serialized forbidden source data %q: %s", forbidden, encoded)
		}
	}
	changed := input
	changed.ProjectSignals = append([]automationPortfolioProjectSignalRow(nil), input.ProjectSignals...)
	changed.ProjectSignals[0].Technologies = "Go API; SvelteKit"
	if next := buildAutomationPortfolio(changed); next.Revision == snapshot.Revision {
		t.Fatal("revision did not change after a visible project technology signal changed")
	}
}

func TestAutomationPortfolioProjectSignalsAreBoundedAndMissingSignalsAreEmptyArrays(t *testing.T) {
	projectID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	tooLong := strings.Repeat("x", automationPortfolioMaxProjectSignalLength+1)
	technologies := []string{tooLong}
	runtimeHints := []string{tooLong}
	for i := 0; i < automationPortfolioMaxTechnologyTagsPerProject+8; i++ {
		technologies = append(technologies, "Tech "+strconv.Itoa(i))
	}
	for i := 0; i < automationPortfolioMaxRuntimeHintsPerProject+8; i++ {
		runtimeHints = append(runtimeHints, "Runtime "+strconv.Itoa(i))
	}
	input := automationPortfolioBuildInput{
		GeneratedAt: time.Date(2026, time.August, 12, 15, 4, 5, 0, time.UTC),
		Projects: []models.DeliveryProject{
			{ID: projectID, ClientID: clientID, Name: "Signals", Status: "active", Client: models.Client{ID: clientID, Name: "Client"}},
			{ID: uuid.Must(uuid.NewV4()), ClientID: clientID, Name: "No signals", Status: "active", Client: models.Client{ID: clientID, Name: "Client"}},
		},
		ProjectSignals: []automationPortfolioProjectSignalRow{{
			ProjectID:    projectID,
			Kind:         "runbook",
			Technologies: strings.Join(technologies, ";"),
		}, {
			ProjectID:        projectID,
			Kind:             "repository",
			RuntimeHintsJSON: mustJSON(t, runtimeHints),
		}},
	}

	snapshot := buildAutomationPortfolio(input)
	if len(snapshot.Projects[0].TechnologyTags) != automationPortfolioMaxTechnologyTagsPerProject {
		t.Fatalf("technology tag count should be capped at %d, got %d", automationPortfolioMaxTechnologyTagsPerProject, len(snapshot.Projects[0].TechnologyTags))
	}
	if len(snapshot.Projects[0].RuntimeHints) != automationPortfolioMaxRuntimeHintsPerProject {
		t.Fatalf("runtime hint count should be capped at %d, got %d", automationPortfolioMaxRuntimeHintsPerProject, len(snapshot.Projects[0].RuntimeHints))
	}
	for _, value := range append(append([]string{}, snapshot.Projects[0].TechnologyTags...), snapshot.Projects[0].RuntimeHints...) {
		if utf8.RuneCountInString(value) > automationPortfolioMaxProjectSignalLength {
			t.Fatalf("signal exceeded %d runes: %q", automationPortfolioMaxProjectSignalLength, value)
		}
	}
	missing := snapshot.Projects[1]
	if missing.TechnologyTags == nil || missing.RuntimeHints == nil || len(missing.TechnologyTags) != 0 || len(missing.RuntimeHints) != 0 {
		t.Fatalf("missing signals must be non-nil empty arrays: %#v", missing)
	}
	encoded, err := json.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"technology_tags":[]`, `"runtime_hints":[]`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("missing signals should serialize as empty arrays (%s): %s", expected, encoded)
		}
	}
}

func TestAutomationPortfolioProjectSignalRuntimeJSONLimit(t *testing.T) {
	if hints := automationPortfolioRuntimeHints(strings.Repeat(" ", automationPortfolioMaxRuntimeHintsJSONBytes+1)); hints != nil {
		t.Fatalf("oversized runtime hint JSON should be rejected, got %#v", hints)
	}
	if hints := automationPortfolioRuntimeHints(`{"runtime_hints":["not-an-array"]}`); hints != nil {
		t.Fatalf("runtime hints must be an allowlisted string array, got %#v", hints)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
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
		"active_publication.operation = 'delivery.publish'",
		"active_publication.status IN ('queued', 'running')",
		"AS decisions_required",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("active work-item query must exclude safe closures; missing %q", fragment)
		}
	}
}

func TestAutomationPortfolioCostsAreAttributedPerProjectAndWithinThirtyDays(t *testing.T) {
	db, mock := newEpicTestDB(t)
	projectA, projectB := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	generatedAt := time.Date(2026, time.September, 25, 15, 0, 0, 0, time.UTC)
	cutoff := generatedAt.Add(-30 * 24 * time.Hour)
	input := automationPortfolioBuildInput{
		GeneratedAt: generatedAt,
		Projects: []models.DeliveryProject{
			{ID: projectA, ClientID: clientID, Name: "Project A", Status: "active", Client: models.Client{ID: clientID, Name: "Client"}},
			{ID: projectB, ClientID: clientID, Name: "Project B", Status: "active", Client: models.Client{ID: clientID, Name: "Client"}},
		},
	}

	// The fixture represents agent and tool ledger rows for each project:
	// verified USD pricing contributes to the amount, while legacy/unpriced USD
	// and priced EUR executions contribute only to the unpriced counters.
	mock.ExpectQuery(`(?s)SELECT work_item\.project_id AS project_id,.*UPPER.*currency.*<> 'USD'.*pricing_basis.*IN .*legacy.*unpriced.*completed_at <= \$1.*completed_at >= \$2 AND execution.completed_at <= \$3.*completed_at <= \$4\) AS unpriced_executions.*completed_at >= \$5 AND execution.completed_at <= \$6\) AS unpriced_executions_last_30_days.*FROM .*automation_executions.*UNION ALL.*automation_tool_executions.*WHERE work_item\.project_id IN \(\$7,\$8\).*GROUP BY`).
		WithArgs(generatedAt, cutoff, generatedAt, generatedAt, cutoff, generatedAt, projectA, projectB).
		WillReturnRows(sqlmock.NewRows([]string{"project_id", "total_cost_micros", "cost_last_30_days_micros", "unpriced_executions", "unpriced_executions_last_30_days"}).
			AddRow(projectA, int64(1_200_000), int64(600_000), int64(5), int64(3)).
			AddRow(projectB, int64(300_000), int64(300_000), int64(2), int64(1)))
	if err := automationPortfolioProjectCostsQuery(db, []uuid.UUID{projectA, projectB}, cutoff, generatedAt).Scan(&input.ProjectCosts).Error; err != nil {
		t.Fatalf("load project-scoped cost rows: %v", err)
	}
	if len(input.ProjectCosts) != 2 || input.ProjectCosts[0].CostLast30DaysMicros == 0 || input.ProjectCosts[1].CostLast30DaysMicros == 0 {
		t.Fatalf("project cost rows were not scanned with the expected 30-day field: %#v", input.ProjectCosts)
	}
	snapshot := buildAutomationPortfolio(input)
	if snapshot.Totals.TotalCostMicros != 1_500_000 || snapshot.Totals.CostLast30DaysMicros != 900_000 {
		t.Fatalf("portfolio totals lost all-time or last-30-day costs: %#v", snapshot.Totals)
	}
	if snapshot.Totals.UnpricedExecutions != 7 || snapshot.Totals.UnpricedExecutionsLast30Days != 4 {
		t.Fatalf("portfolio totals must count historical and last-30-day unpriced agent/tool executions: %#v", snapshot.Totals)
	}
	if len(snapshot.Projects) != 2 {
		t.Fatalf("expected costs for both projects: %#v", snapshot.Projects)
	}
	byID := make(map[uuid.UUID]automationPortfolioProject, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		byID[project.ID] = project
	}
	if got := byID[projectA]; got.TotalCostMicros != 1_200_000 || got.CostLast30DaysMicros != 600_000 || got.UnpricedExecutions != 5 || got.UnpricedExecutionsLast30Days != 3 {
		t.Fatalf("project A cost attribution is incorrect: %#v", got)
	}
	if got := byID[projectB]; got.TotalCostMicros != 300_000 || got.CostLast30DaysMicros != 300_000 || got.UnpricedExecutions != 2 || got.UnpricedExecutionsLast30Days != 1 {
		t.Fatalf("project B cost attribution is incorrect: %#v", got)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("serialize portfolio snapshot: %v", err)
	}
	var serialized struct {
		Totals struct {
			CostLast30DaysMicros         int64 `json:"cost_last_30_days_microusd"`
			UnpricedExecutions           int64 `json:"unpriced_executions"`
			UnpricedExecutionsLast30Days int64 `json:"unpriced_executions_last_30_days"`
		} `json:"totals"`
		Projects []struct {
			ID                           uuid.UUID `json:"id"`
			CostLast30DaysMicros         int64     `json:"cost_last_30_days_microusd"`
			UnpricedExecutions           int64     `json:"unpriced_executions"`
			UnpricedExecutionsLast30Days int64     `json:"unpriced_executions_last_30_days"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(encoded, &serialized); err != nil {
		t.Fatalf("deserialize portfolio cost fields: %v", err)
	}
	if serialized.Totals.CostLast30DaysMicros != 900_000 || serialized.Totals.UnpricedExecutions != 7 || serialized.Totals.UnpricedExecutionsLast30Days != 4 || len(serialized.Projects) != 2 {
		t.Fatalf("serialized portfolio response lost the 30-day totals or pricing coverage: %s", encoded)
	}
	type serializedProjectCost struct {
		CostLast30DaysMicros         int64
		UnpricedExecutions           int64
		UnpricedExecutionsLast30Days int64
	}
	serializedByID := make(map[uuid.UUID]serializedProjectCost, len(serialized.Projects))
	for _, project := range serialized.Projects {
		serializedByID[project.ID] = serializedProjectCost{CostLast30DaysMicros: project.CostLast30DaysMicros, UnpricedExecutions: project.UnpricedExecutions, UnpricedExecutionsLast30Days: project.UnpricedExecutionsLast30Days}
	}
	if serializedByID[projectA].CostLast30DaysMicros != 600_000 || serializedByID[projectA].UnpricedExecutions != 5 || serializedByID[projectA].UnpricedExecutionsLast30Days != 3 || serializedByID[projectB].CostLast30DaysMicros != 300_000 || serializedByID[projectB].UnpricedExecutions != 2 || serializedByID[projectB].UnpricedExecutionsLast30Days != 1 {
		t.Fatalf("serialized per-project costs were lost: %s", encoded)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
