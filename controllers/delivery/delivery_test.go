package delivery

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

func TestDeliveryProjectResponseKeepsWorkflowProjectionOnProjectWorkItems(t *testing.T) {
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: uuid.Must(uuid.NewV4()), State: deliveryworkflow.StatePlanReview, Title: "Review"}
	response := deliveryProjectResponse{
		DeliveryProject: models.DeliveryProject{ID: item.ProjectID, Name: "Project", WorkItems: []models.DeliveryWorkItem{item}},
		WorkItems:       []deliveryProjectWorkItem{{DeliveryWorkItem: item, WorkflowProjection: buildDeliveryWorkflowProjection(item, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))}},
		Preparation:     projectPreparation{Version: 1, Checks: []preparationCheck{}},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		WorkItems []struct {
			WorkflowProjection deliveryWorkflowProjection `json:"workflow_projection"`
		} `json:"work_items"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.WorkItems) != 1 || decoded.WorkItems[0].WorkflowProjection.Stage != "plan" {
		t.Fatalf("project response lost work-item projection: %s", encoded)
	}
}

func TestAppendMandatoryProjectContextKeepsSelectedSourcesAndDeduplicates(t *testing.T) {
	backend := models.DeliveryContextSource{ID: uuid.Must(uuid.NewV4()), Kind: "repository", Reference: "workspace://backend"}
	environment := models.DeliveryContextSource{ID: uuid.Must(uuid.NewV4()), Kind: "environment", Reference: "local://project/development"}
	workflow := models.DeliveryContextSource{ID: uuid.Must(uuid.NewV4()), Kind: "runbook", Reference: "workflow://project/rules"}
	got := appendMandatoryProjectContext([]models.DeliveryContextSource{backend, environment}, []models.DeliveryContextSource{environment, workflow})
	if len(got) != 3 || got[0].ID != backend.ID || got[1].ID != environment.ID || got[2].ID != workflow.ID {
		t.Fatalf("expected selected sources followed by missing project rules without duplicates, got %#v", got)
	}
}

func TestNormalizeWorkItemRequestAcceptsOptionalEpicID(t *testing.T) {
	projectID := uuid.Must(uuid.NewV4())
	contextID := uuid.Must(uuid.NewV4())
	base := workItemRequest{
		Title: "Implement endpoint", ExpectedOutcome: "Endpoint is available",
		ContextSourceIDs: []string{contextID.String()},
	}
	standalone, err := normalizeWorkItemRequest(projectID, "operator", base, false)
	if err != nil || standalone.EpicID != nil {
		t.Fatalf("standalone task should keep no epic association: %#v, %v", standalone, err)
	}
	epicID := uuid.Must(uuid.NewV4())
	base.EpicID = epicID.String()
	withEpic, err := normalizeWorkItemRequest(projectID, "operator", base, false)
	if err != nil || withEpic.EpicID == nil || *withEpic.EpicID != epicID {
		t.Fatalf("valid epic_id was not preserved: %#v, %v", withEpic.EpicID, err)
	}
	base.EpicID = "not-a-uuid"
	if _, err := normalizeWorkItemRequest(projectID, "operator", base, false); !errors.Is(err, errWorkItemEpicIDInvalid) {
		t.Fatalf("invalid epic_id should return a fixed validation error, got %v", err)
	}
}

func TestWorkItemCreationRequiresManageOnlyWhenAnEpicIsSelected(t *testing.T) {
	requester := models.DeliveryProjectMember{Role: "requester"}
	standalonePermission := workItemCreatePermission(workItemRequest{})
	if standalonePermission != deliveryRequest {
		t.Fatalf("standalone task permission = %q, want request", standalonePermission)
	}
	if !memberAllows(requester, standalonePermission) {
		t.Fatal("a project requester must retain standalone task creation")
	}
	epicPermission := workItemCreatePermission(workItemRequest{EpicID: "  " + uuid.Must(uuid.NewV4()).String() + "  "})
	if epicPermission != deliveryManage {
		t.Fatalf("epic-associated task permission = %q, want manage", epicPermission)
	}
	if memberAllows(requester, epicPermission) {
		t.Fatal("a project requester without manage permission must not create an epic-associated task")
	}
	manager := models.DeliveryProjectMember{Role: "delivery_manager"}
	if !memberAllows(manager, epicPermission) {
		t.Fatal("a project manager must be able to create an epic-associated task")
	}
}

func TestAttachWorkItemToEpicRejectsForeignOrSensitiveEpicWithoutEcho(t *testing.T) {
	t.Run("foreign project", func(t *testing.T) {
		db, mock := newEpicTestDB(t)
		projectID, epicID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		mock.ExpectQuery(`SELECT .*FROM "delivery_epics".*FOR UPDATE`).
			WithArgs(epicID, projectID, 1).
			WillReturnRows(sqlmockRows("id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at"))
		input := normalizedWorkItemRequest{ProjectID: projectID, EpicID: &epicID, RequestedBy: "operator"}
		err := attachWorkItemToEpicInTransaction(db, input, uuid.Must(uuid.NewV4()), time.Now().UTC())
		if !errors.Is(err, errWorkItemEpicNotInProject) || strings.Contains(err.Error(), epicID.String()) {
			t.Fatalf("foreign epic should be rejected with a non-echoing fixed error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("legacy sensitive text", func(t *testing.T) {
		db, mock := newEpicTestDB(t)
		projectID, epicID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		secret := "OPENAI_API_KEY=sk-proj-never-echo-this"
		mock.ExpectQuery(`SELECT .*FROM "delivery_epics".*FOR UPDATE`).
			WithArgs(epicID, projectID, 1).
			WillReturnRows(sqlmockRows("id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at").
				AddRow(epicID, projectID, "Legacy", secret, "active", "operator", time.Now(), time.Now()))
		input := normalizedWorkItemRequest{ProjectID: projectID, EpicID: &epicID, RequestedBy: "operator"}
		err := attachWorkItemToEpicInTransaction(db, input, uuid.Must(uuid.NewV4()), time.Now().UTC())
		if !errors.Is(err, errWorkItemEpicContainsSensitiveMaterial) || strings.Contains(err.Error(), secret) {
			t.Fatalf("legacy secret should be rejected without echo, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAttachWorkItemToEpicPersistsAllowlistedImmutableSnapshot(t *testing.T) {
	db, mock := newEpicTestDB(t)
	projectID, epicID, itemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	now := time.Date(2026, 9, 25, 12, 30, 45, 123456789, time.FixedZone("UTC+2", 2*60*60)).UTC()
	epicUpdatedAt := time.Date(2026, 9, 24, 8, 7, 6, 987654321, time.FixedZone("UTC-5", -5*60*60))
	associationID := uuid.Must(uuid.NewV4())
	input := normalizedWorkItemRequest{ProjectID: projectID, EpicID: &epicID, RequestedBy: "operator"}
	metadata := `{"epic_id":"` + epicID.String() + `","status":"active","summary":"Approved scope"}`

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .*FROM "delivery_epics".*FOR UPDATE`).
		WithArgs(epicID, projectID, 1).
		WillReturnRows(sqlmockRows("id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at").
			AddRow(epicID, projectID, "API integration", "Approved scope", "active", "owner", now, epicUpdatedAt))
	mock.ExpectQuery(`INSERT INTO "delivery_epic_work_items"`).
		WithArgs(projectID, epicID, itemID, "operator", now, nil).
		WillReturnRows(sqlmockRows("id").AddRow(associationID))
	mock.ExpectQuery(`INSERT INTO "delivery_context_snapshots"`).
		WithArgs(itemID, associationID, "epic", "API integration", "epic://"+epicID.String(), epicUpdatedAt.UTC().Format(time.RFC3339Nano), metadata, now, sqlmock.AnyArg()).
		WillReturnRows(sqlmockRows("id").AddRow(uuid.Must(uuid.NewV4())))
	mock.ExpectCommit()

	err := db.Transaction(func(tx *gorm.DB) error {
		return attachWorkItemToEpicInTransaction(tx, input, itemID, now)
	})
	if err != nil {
		t.Fatalf("expected association and snapshot to persist: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEpicMembershipAndSnapshotRollbackTogether(t *testing.T) {
	db, mock := newEpicTestDB(t)
	projectID, epicID, itemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	now := time.Date(2026, 9, 25, 12, 30, 45, 123456789, time.FixedZone("UTC+2", 2*60*60)).UTC()
	epicUpdatedAt := time.Date(2026, 9, 24, 8, 7, 6, 987654321, time.FixedZone("UTC-5", -5*60*60))
	associationID := uuid.Must(uuid.NewV4())
	input := normalizedWorkItemRequest{ProjectID: projectID, EpicID: &epicID, RequestedBy: "operator"}
	metadata := `{"epic_id":"` + epicID.String() + `","status":"active","summary":"Approved scope"}`

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO delivery_work_items \(id, project_id\) VALUES \(\$1, \$2\)`).
		WithArgs(itemID, projectID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .*FROM "delivery_epics".*FOR UPDATE`).
		WithArgs(epicID, projectID, 1).
		WillReturnRows(sqlmockRows("id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at").
			AddRow(epicID, projectID, "API integration", "Approved scope", "active", "owner", now, epicUpdatedAt))
	mock.ExpectQuery(`INSERT INTO "delivery_epic_work_items"`).
		WillReturnRows(sqlmockRows("id").AddRow(associationID))
	mock.ExpectQuery(`INSERT INTO "delivery_context_snapshots"`).
		WithArgs(itemID, associationID, "epic", "API integration", "epic://"+epicID.String(), epicUpdatedAt.UTC().Format(time.RFC3339Nano), metadata, now, sqlmock.AnyArg()).
		WillReturnError(errors.New("snapshot write failed"))
	mock.ExpectRollback()

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("INSERT INTO delivery_work_items (id, project_id) VALUES (?, ?)", itemID, projectID).Error; err != nil {
			return err
		}
		return attachWorkItemToEpicInTransaction(tx, input, itemID, now)
	})
	if err == nil || !strings.Contains(err.Error(), "snapshot write failed") {
		t.Fatalf("failed immutable snapshot insert should abort whole transaction, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func sqlmockRows(columns ...string) *sqlmock.Rows {
	return sqlmock.NewRows(columns)
}

func TestMandatoryProjectContextSourceExcludesOrdinaryRunbooks(t *testing.T) {
	checks := []struct {
		kind      string
		reference string
		mandatory bool
	}{
		{"environment", "local://project/development", true},
		{"runbook", "workflow://project/rules", true},
		{"runbook", "document://release-notes", false},
		{"repository", "workspace://backend", false},
	}
	for _, check := range checks {
		if got := mandatoryProjectContextSource(models.DeliveryContextSource{Kind: check.kind, Reference: check.reference}); got != check.mandatory {
			t.Fatalf("mandatory context for %s %s = %t, want %t", check.kind, check.reference, got, check.mandatory)
		}
	}
}

func TestNormalizeEnvironmentContextMetadataKeepsOnlyValidatedRouting(t *testing.T) {
	credentialURL := "https://" + "fixture-user" + ":" + strings.Repeat("x", 16) + "@example.test"
	metadata, err := normalizeEnvironmentContextMetadata(map[string]any{
		"branch": " staging ", "deployment": "Manual", "url": "https://staging.example.test",
		"promotion": "After QA, open a PR to main", "excerpt": "QA environment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata["branch"] != "staging" || metadata["deployment"] != "manual" || metadata["url"] != "https://staging.example.test" || metadata["promotion"] != "After QA, open a PR to main" {
		t.Fatalf("environment routing was changed: %#v", metadata)
	}
	for _, invalid := range []map[string]any{
		{"branch": "", "deployment": "manual"},
		{"branch": "main", "deployment": "sometimes"},
		{"branch": "main", "deployment": "none", "url": "javascript:alert(1)"},
		{"branch": "main;echo", "deployment": "none"},
		{"branch": "main", "deployment": "none", "url": credentialURL},
		{"branch": "main", "deployment": "none", "url": "https://example.test/?token=secret"},
		{"branch": "main", "deployment": "none", "api_key": "secret"},
	} {
		if _, err := normalizeEnvironmentContextMetadata(invalid); err == nil {
			t.Fatalf("invalid environment accepted: %#v", invalid)
		}
	}
}

func TestMergeDeliveryEnvironmentContextMetadataPreservesUntouchedProjectRules(t *testing.T) {
	stored := `{"branch":"dev","deployment":"automatic","url":"https://dev.example.test","promotion":"After QA, open a PR to main","excerpt":"Local development"}`
	metadata, err := mergeDeliveryEnvironmentContextMetadata(stored, map[string]any{
		"branch": " staging ", "deployment": "manual", "url": "", "promotion": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata["branch"] != "staging" || metadata["deployment"] != "manual" || metadata["excerpt"] != "Local development" {
		t.Fatalf("environment settings were not merged: %#v", metadata)
	}
	if _, exists := metadata["url"]; exists {
		t.Fatalf("cleared URL should be removed: %#v", metadata)
	}
	if _, exists := metadata["promotion"]; exists {
		t.Fatalf("cleared promotion should be removed: %#v", metadata)
	}
	for _, invalid := range []map[string]any{
		{"branch": "dev", "deployment": "manual", "github_app_private_key": "secret"},
		{"branch": "dev", "deployment": "manual", "url": "https://dev.example.test/?token=secret"},
	} {
		if _, err := mergeDeliveryEnvironmentContextMetadata(stored, invalid); err == nil {
			t.Fatalf("invalid environment update accepted: %#v", invalid)
		}
	}
}

func TestMergeDeliveryRunbookContextMetadataValidatesAndPreservesProjectRules(t *testing.T) {
	stored := `{"technologies":"Go API; Next.js frontend","issue_workflow":"Track work in GitHub Issues","branch_workflow":"feature/* from dev","pull_request_workflow":"Require review before merge","release_workflow":"Promote staging to main after QA","excerpt":"Never publish without a human gate"}`
	metadata, err := mergeDeliveryRunbookContextMetadata(stored, map[string]any{
		"technologies": "Go API; Next.js frontend v16", "issue_workflow": "", "branch_workflow": "feature/* from dev",
		"pull_request_workflow": "Require review and green checks", "release_workflow": "Promote staging to main after QA",
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata["technologies"] != "Go API; Next.js frontend v16" || metadata["pull_request_workflow"] != "Require review and green checks" || metadata["excerpt"] != "Never publish without a human gate" {
		t.Fatalf("runbook settings were not merged: %#v", metadata)
	}
	if _, exists := metadata["issue_workflow"]; exists {
		t.Fatalf("cleared issue rule should be removed: %#v", metadata)
	}
	for _, invalid := range []map[string]any{
		{"technologies": "Go", "source_id": "another-project"},
		{"technologies": 42},
		{"technologies": strings.Repeat("x", 1201)},
	} {
		if _, err := mergeDeliveryRunbookContextMetadata(stored, invalid); err == nil {
			t.Fatalf("invalid runbook update accepted: %#v", invalid)
		}
	}
	if _, err := mergeDeliveryRunbookContextMetadata(`{}`, map[string]any{
		"technologies": "", "issue_workflow": "", "branch_workflow": "", "pull_request_workflow": "", "release_workflow": "",
	}); err == nil {
		t.Fatal("a workflow cannot be cleared to an empty project rule set")
	}
	if _, err := mergeDeliveryRunbookContextMetadata(`{"managed_reference":"workflow://project/rules","issue_workflow":"Create and scope an issue"}`, map[string]any{
		"issue_workflow": "",
	}); err == nil {
		t.Fatal("unrelated metadata must not count as a project workflow rule")
	}
}

func TestNextDeliveryRunbookRevisionIncrementsVersion(t *testing.T) {
	for _, test := range []struct{ current, want string }{
		{"v1", "v2"}, {"v8", "v9"}, {"legacy", "v2"}, {"", "v2"},
	} {
		if got := nextDeliveryRunbookRevision(test.current); got != test.want {
			t.Fatalf("nextDeliveryRunbookRevision(%q) = %q, want %q", test.current, got, test.want)
		}
	}
}

func TestLegacyDeliveryWorkItemResponseProjectsConservativeMandateObject(t *testing.T) {
	item := models.DeliveryWorkItem{Title: "Legacy task", ExpectedOutcome: "A checked result", IncludedScopeJSON: `["src"]`, ExcludedScopeJSON: `["prod"]`}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Mandate map[string]any `json:"mandate"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Mandate["objective"] != "A checked result" || decoded.Mandate["autonomy_policy"] != "bounded_autonomy" {
		t.Fatalf("legacy work item must expose a usable conservative mandate: %s", encoded)
	}
	if _, exposed := decoded.Mandate["mandate_json"]; exposed {
		t.Fatal("private mandate storage must never leak into the API")
	}
}

func TestAgentOperationForSubmission(t *testing.T) {
	tests := []struct {
		action    deliveryworkflow.Action
		operation string
		phase     string
	}{
		{deliveryworkflow.ActionSubmitPlan, "delivery.plan", "plan"},
		{deliveryworkflow.ActionSubmitCodeReview, "delivery.implementation", "implementation"},
		{deliveryworkflow.ActionSubmitQA, "delivery.qa", "qa"},
		{deliveryworkflow.ActionApproveRelease, "delivery.summary", "summary"},
	}

	for _, test := range tests {
		operation, phase := agentOperationForSubmission(test.action)
		if operation != test.operation || phase != test.phase {
			t.Fatalf("%s = (%q, %q), want (%q, %q)", test.action, operation, phase, test.operation, test.phase)
		}
	}
}

func TestValidWebURL(t *testing.T) {
	for _, value := range []string{"https://github.com/itbem/project/pull/42", "http://localhost:3000/preview"} {
		if !validWebURL(value) {
			t.Fatalf("expected %q to be accepted", value)
		}
	}
	for _, value := range []string{"", "ftp://example.test/change", "https:///missing-host", "javascript:alert(1)"} {
		if validWebURL(value) {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestDeliveryArtifactReferenceAcceptsLegacyAndRunScopedAssets(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "itbem-ai-outputs-local"}
	reference := "s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/artifacts/01-preview.png"
	parsedTaskID, key, name, ok := deliveryArtifactReference(cfg, reference)
	if !ok || parsedTaskID != taskID || key != "automation/"+taskID.String()+"/artifacts/01-preview.png" || name != "01-preview.png" {
		t.Fatalf("unexpected private artifact parse: %s / %s / %s / %v", parsedTaskID, key, name, ok)
	}
	if _, _, _, ok := deliveryArtifactReference(cfg, "s3://itbem-ai-outputs-local/automation/"+taskID.String()+"/result.json"); ok {
		t.Fatal("result documents must not be served as visual artifacts")
	}

	runScopedReference := "s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/runs/" + runID.String() + "/artifacts/01-preview.png"
	parsedTaskID, key, name, ok = deliveryArtifactReference(cfg, runScopedReference)
	if !ok || parsedTaskID != taskID || key != "automation/"+taskID.String()+"/runs/"+runID.String()+"/artifacts/01-preview.png" || name != "01-preview.png" {
		t.Fatalf("unexpected run-scoped artifact parse: %s / %s / %s / %v", parsedTaskID, key, name, ok)
	}
	parsedRunID, runScoped := deliveryArtifactRunID(key)
	if !runScoped || parsedRunID != runID.String() {
		t.Fatalf("unexpected run lineage: %q / %v", parsedRunID, runScoped)
	}
	if _, runScoped := deliveryArtifactRunID("automation/" + taskID.String() + "/artifacts/01-preview.png"); runScoped {
		t.Fatal("legacy artifact key must not claim a worker run")
	}
	for _, invalid := range []string{
		"s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/runs/not-a-uuid/artifacts/01-preview.png",
		"s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/runs/" + runID.String() + "/steps/01-preview.png",
		"s3://itbem-ai-outputs-local/automation/" + taskID.String() + "/runs/" + runID.String() + "/artifacts/../01-preview.png",
	} {
		if _, _, _, ok := deliveryArtifactReference(cfg, invalid); ok {
			t.Fatalf("invalid private artifact reference accepted: %s", invalid)
		}
	}
}

func TestPublicationGrantInvalidationFollowsWorkflowBoundary(t *testing.T) {
	invalidating := []deliveryworkflow.Action{
		deliveryworkflow.ActionPreviewReady,
		deliveryworkflow.ActionRequestQAChanges,
		deliveryworkflow.ActionApproveQA,
		deliveryworkflow.ActionApproveRelease,
		deliveryworkflow.ActionBlock,
		deliveryworkflow.ActionCancel,
	}
	for _, action := range invalidating {
		if !publicationGrantsInvalidatedBy(action) {
			t.Fatalf("%q must close a live publication grant", action)
		}
	}
	for _, action := range []deliveryworkflow.Action{deliveryworkflow.ActionSubmitPlan, deliveryworkflow.ActionApprovePlan, deliveryworkflow.ActionSubmitCodeReview} {
		if publicationGrantsInvalidatedBy(action) {
			t.Fatalf("%q must not invalidate a grant outside the publishing lifecycle", action)
		}
	}
}

func TestDeliveryEvidenceSHA256AcceptsCanonicalDigestAndFailsClosedForMalformedMetadata(t *testing.T) {
	digest, present, err := deliveryEvidenceSHA256(`{"sha256":"ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}`)
	if err != nil || !present || digest != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Fatalf("unexpected canonical digest parse: %q / %v / %v", digest, present, err)
	}
	if _, present, err := deliveryEvidenceSHA256(`{}`); err != nil || present {
		t.Fatalf("legacy evidence must remain readable: %v / %v", present, err)
	}
	if _, _, err := deliveryEvidenceSHA256(`{"sha256":"not-a-digest"}`); err == nil {
		t.Fatal("malformed explicit integrity metadata must fail closed")
	}
}

func TestCodeReviewRequiredRepositoriesUsesOnlyApprovedChangedScope(t *testing.T) {
	plan := `{"repository_impact":[{"reference":"workspace://api","impact":"changes"},{"reference":"workspace://dashboard","impact":"consulted"},{"reference":"workspace://worker","impact":"changes"}]}`
	required, err := codeReviewRequiredRepositories(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 2 {
		t.Fatalf("required repositories = %#v, want two", required)
	}
	for _, reference := range []string{"workspace://api", "workspace://worker"} {
		if _, ok := required[reference]; !ok {
			t.Fatalf("%s must require its own review", reference)
		}
	}
	if _, ok := required["workspace://dashboard"]; ok {
		t.Fatal("consulted repositories must not require a change review")
	}
}

func TestCodeReviewRequiredRepositoriesFailsClosedForMalformedModernMatrix(t *testing.T) {
	for _, plan := range []string{
		`{"summary":"missing matrix"}`,
		`{"repository_impact":"workspace://api"}`,
		`{"repository_impact":[{"reference":"workspace://api","impact":"unknown"}]}`,
		`{"repository_impact":[{"reference":"","impact":"changes"}]}`,
	} {
		if _, err := codeReviewRequiredRepositories(plan); err == nil {
			t.Fatalf("modern plan must fail closed: %s", plan)
		}
	}
	for _, legacy := range []string{"", "{}"} {
		required, err := codeReviewRequiredRepositories(legacy)
		if err != nil || len(required) != 0 {
			t.Fatalf("legacy plan should retain one-review compatibility: %#v / %v", required, err)
		}
	}
}

func TestValidPublishedChangeRecordRequiresGitHubAppEvidence(t *testing.T) {
	valid := models.DeliveryChangeSet{
		RepositoryRef: "workspace://backend", Branch: "itbem-agent/123", ReviewType: "pull_request",
		MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app"}`,
	}
	if !validPublishedChangeRecord(valid) {
		t.Fatal("published GitHub App evidence should be accepted")
	}
	for _, invalid := range []models.DeliveryChangeSet{
		{RepositoryRef: valid.RepositoryRef, Branch: valid.Branch, ReviewType: "pull_request", MetadataJSON: `{"branch_published":true}`},
		{RepositoryRef: valid.RepositoryRef, Branch: valid.Branch, ReviewType: "local_worktree", MetadataJSON: valid.MetadataJSON},
		{RepositoryRef: valid.RepositoryRef, Branch: valid.Branch, ReviewType: "pull_request", MetadataJSON: `{"branch_published":false,"verification_source":"itbem-github-app"}`},
	} {
		if validPublishedChangeRecord(invalid) {
			t.Fatalf("unproven publication must not unlock preview: %#v", invalid)
		}
	}
}

func TestMissingPublishedRepositoriesRequiresEveryChangedRepository(t *testing.T) {
	required := map[string]struct{}{"workspace://backend": {}, "workspace://dashboard": {}}
	backend := models.DeliveryChangeSet{RepositoryRef: "workspace://backend", Branch: "itbem-agent/1", ReviewType: "pull_request", MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app"}`}
	if missing := missingPublishedRepositories(required, []models.DeliveryChangeSet{backend}); len(missing) != 1 || missing[0] != "workspace://dashboard" {
		t.Fatalf("preview must remain blocked for the unpublished repository: %#v", missing)
	}
	dashboard := backend
	dashboard.RepositoryRef, dashboard.Branch = "workspace://dashboard", "itbem-agent/2"
	if missing := missingPublishedRepositories(required, []models.DeliveryChangeSet{backend, dashboard}); len(missing) != 0 {
		t.Fatalf("all published repositories should satisfy preview coverage: %#v", missing)
	}
}
