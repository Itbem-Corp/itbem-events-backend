package automation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestGetDispatchQueueReturnsOnlySafeCursorPaginatedAssignments(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelOperational}, nil
	}})
	t.Cleanup(restore)

	projectID := uuid.Must(uuid.NewV4())
	assignmentOne, assignmentTwo := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	executionID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	workItemID, clientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	machineID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	heartbeatSeenAt := time.Now().UTC()
	queuedAt := createdAt.Add(time.Minute)
	columns := []string{
		"assignment_id", "execution_id", "step_id", "step_key", "step_title", "step_status", "assignment_status",
		"work_item_id", "work_item_title", "work_item_state", "project_id", "project_name", "client_id", "client_name",
		"target_agent_key", "target_machine_id", "target_concurrency", "target_active_runs", "target_draining", "target_last_seen_at",
		"queued_at", "dispatched_at", "started_at", "created_at", "is_ready",
	}
	rows := sqlmock.NewRows(columns).
		AddRow(assignmentOne, executionID, stepID, "backend", "Implement endpoint", "ready", "queued", workItemID,
			"Review Authorization: Bearer private-token", "implementation", projectID, "Agent Studio", clientID, "ITBEM",
			"generalist", machineID.String(), 4, 2, false, heartbeatSeenAt, queuedAt, nil, nil, createdAt, true).
		AddRow(assignmentTwo, executionID, stepID, "frontend", "Connect dashboard", "ready", "queued", workItemID,
			"Wire dashboard", "implementation", projectID, "Agent Studio", clientID, "ITBEM",
			"generalist", machineID.String(), 2, 2, false, heartbeatSeenAt.Add(-2*time.Minute), queuedAt.Add(time.Minute), nil, nil, createdAt.Add(time.Minute), false)
	mock.ExpectQuery(`(?s)SELECT.*COUNT\(\*\) FILTER.*FROM delivery_plan_step_assignments a.*a.created_at <= \$2.*AND a.status = \$3.*AND pr.id = \$4.*AND a.target_agent_key = \$5`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "queued", projectID, "generalist").
		WillReturnRows(sqlmock.NewRows([]string{"blocked_assignments", "expired_plan_step_leases"}).AddRow(0, 1))
	mock.ExpectQuery(`(?s)SELECT.*FROM delivery_plan_step_assignments a.*ORDER BY a.created_at ASC, a.id ASC\s+LIMIT \$\d+`).
		WithArgs(sqlmock.AnyArg(), "queued", projectID, "generalist", 2).
		WillReturnRows(rows)

	request := httptest.NewRequest(http.MethodGet,
		"/api/automation/dispatch/queue?page_size=1&status=queued&project_id="+projectID.String()+"&agent_key=generalist", nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", "dispatch-queue-test")
	ctx.Set("workspace_mode", "platform")
	if err := GetDispatchQueue(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data dispatchQueuePage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	page := envelope.Data
	if page.SchemaVersion != 1 || page.GeneratedAt.IsZero() || page.BlockedAssignments != 0 || page.ExpiredPlanStepLeases != 1 || len(page.Items) != 1 || page.Items[0].AssignmentID != assignmentOne || page.Items[0].ProjectID != projectID || !page.Items[0].IsReady || page.NextCursor == nil || *page.NextCursor == "" {
		t.Fatalf("unexpected dispatch page: %#v", page)
	}
	if page.Items[0].TargetMachineID == nil || *page.Items[0].TargetMachineID != machineID.String() || page.Items[0].TargetAgentKey == nil || *page.Items[0].TargetAgentKey != "generalist" || page.Items[0].StepKey != "backend" || page.Items[0].AssignmentStatus != "queued" {
		t.Fatalf("safe operational fields missing: %#v", page.Items[0])
	}
	if page.Items[0].TargetAvailability != "working" || page.Items[0].TargetConcurrency != 4 || page.Items[0].TargetActiveRuns != 2 || page.Items[0].TargetAvailableSlots != 2 || page.Items[0].TargetLastSeenAt == nil {
		t.Fatalf("live worker capacity missing or incorrect: %#v", page.Items[0])
	}
	if strings.Contains(strings.ToLower(recorder.Body.String()), "private-token") || regexp.MustCompile(`(?i)prompt|input_ref|output_ref|objective|secret|credential|reasoning|bucket|object_key`).MatchString(recorder.Body.String()) {
		t.Fatalf("dispatch response exposed sensitive content or fields: %s", recorder.Body.String())
	}
	cursor, err := decodeDispatchQueueCursor(*page.NextCursor, dispatchQueueCursorScope(dispatchQueueFilters{
		PageSize: 1, Status: "queued", Project: &projectID, AgentKey: "generalist",
	}, "dispatch-queue-test", "platform", uuid.Nil))
	if err != nil || cursor == nil || cursor.ID != assignmentOne.String() || !cursor.Snapshot.Equal(page.GeneratedAt) || !cursor.CreatedAt.Equal(createdAt) {
		t.Fatalf("next cursor does not bind the last item and snapshot: %#v, %v", cursor, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetDispatchQueueAppliesProjectAuthorizationAndKeysetCursor(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	projectID := uuid.Must(uuid.NewV4())
	firstID := uuid.Must(uuid.NewV4())
	snapshot := time.Date(2026, 9, 24, 13, 0, 0, 0, time.UTC)
	position := snapshot.Add(-time.Hour)
	filters := dispatchQueueFilters{PageSize: 2, Project: &projectID, AgentKey: "qa", Status: "dispatched"}
	scope := dispatchQueueCursorScope(filters, "dispatch-queue-test", "platform", uuid.Nil)
	cursor := encodeDispatchQueueCursor(dispatchQueueCursor{Version: 1, Scope: scope, Snapshot: snapshot, CreatedAt: position, ID: firstID.String()})
	mock.ExpectQuery(`(?s)SELECT.*COUNT\(\*\) FILTER.*FROM delivery_plan_step_assignments a.*a.created_at <= \$2.*AND a.status = \$3.*AND pr.id = \$4.*AND a.target_agent_key = \$5`).
		WithArgs(snapshot, snapshot, "dispatched", projectID, "qa").
		WillReturnRows(sqlmock.NewRows([]string{"blocked_assignments", "expired_plan_step_leases"}).AddRow(0, 0))
	mock.ExpectQuery(`(?s)SELECT.*FROM delivery_plan_step_assignments a.*a.created_at <= \$1.*a.status = \$2.*pr.id = \$3.*a.target_agent_key = \$4.*a.created_at, a.id\) > \(\$5, \$6::uuid\).*ORDER BY a.created_at ASC, a.id ASC\s+LIMIT \$7`).
		WithArgs(snapshot, "dispatched", projectID, "qa", position, firstID.String(), 3).
		WillReturnRows(sqlmock.NewRows([]string{
			"assignment_id", "execution_id", "step_id", "step_key", "step_title", "step_status", "assignment_status",
			"work_item_id", "work_item_title", "work_item_state", "project_id", "project_name", "client_id", "client_name",
			"target_agent_key", "target_machine_id", "target_concurrency", "target_active_runs", "target_draining", "target_last_seen_at",
			"queued_at", "dispatched_at", "started_at", "created_at", "is_ready",
		}))

	request := httptest.NewRequest(http.MethodGet,
		"/api/automation/dispatch/queue?page_size=2&status=dispatched&project_id="+projectID.String()+"&agent_key=qa&cursor="+cursor, nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", "dispatch-queue-test")
	ctx.Set("workspace_mode", "platform")
	if err := GetDispatchQueue(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data dispatchQueuePage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.GeneratedAt.Equal(snapshot) || len(envelope.Data.Items) != 0 || envelope.Data.NextCursor != nil {
		t.Fatalf("cursor snapshot was not preserved: %#v", envelope.Data)
	}
	if envelope.Data.BlockedAssignments != 0 || envelope.Data.ExpiredPlanStepLeases != 0 {
		t.Fatalf("filtered queue counters were not returned: %#v", envelope.Data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetDispatchQueueRejectsNonRootAndNonPlatformWorkspace(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })

	for _, test := range []struct {
		name         string
		user         *models.User
		workspace    string
		organization *uuid.UUID
		wantStatus   int
	}{
		{name: "non-root", user: &models.User{IsRoot: false, RootLevel: models.RootLevelNone}, workspace: "platform", wantStatus: http.StatusForbidden},
		{name: "organization workspace requires an explicit project", user: &models.User{IsRoot: false, RootLevel: models.RootLevelNone}, workspace: "organization", organization: uuidPointer(uuid.Must(uuid.NewV4())), wantStatus: http.StatusBadRequest},
		{name: "platform workspace required", user: &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, workspace: "", wantStatus: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) { return test.user, nil }})
			t.Cleanup(restore)
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/dispatch/queue", nil), recorder)
			ctx.Set("cognito_sub", "dispatch-queue-test")
			if test.workspace != "" {
				ctx.Set("workspace_mode", test.workspace)
			}
			if test.organization != nil {
				ctx.Set("organization_id", *test.organization)
			}
			_ = GetDispatchQueue(ctx)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestGetDispatchQueueScopesOrganizationToAuthorizedProjectWithoutWorkerSignals(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })

	organizationID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	subject := "dispatch-project-viewer"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
	}})
	t.Cleanup(restore)

	mock.ExpectQuery(`WITH RECURSIVE organization_clients`).
		WithArgs(organizationID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(organizationID))
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, organizationID))
	mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
		WithArgs(projectID, subject, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "cognito_sub", "role", "permissions", "created_by", "created_at", "updated_at"}).
			AddRow(uuid.Must(uuid.NewV4()), projectID, subject, "viewer", `[]`, "project-admin", time.Now().UTC(), time.Now().UTC()))
	assignmentID, executionID, stepID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	queueRows := sqlmock.NewRows([]string{
		"assignment_id", "execution_id", "step_id", "step_key", "step_title", "step_status", "assignment_status",
		"work_item_id", "work_item_title", "work_item_state", "project_id", "project_name", "client_id", "client_name",
		"target_agent_key", "target_machine_id", "target_concurrency", "target_active_runs", "target_draining", "target_last_seen_at",
		"queued_at", "dispatched_at", "started_at", "created_at", "is_ready",
	}).AddRow(assignmentID, executionID, stepID, "backend", "Implement API", "ready", "queued", workItemID,
		"Implement project API", "implementation", projectID, "Agent Studio", organizationID, "ITBEM", "generalist", nil, 0, 0, false, nil,
		nil, nil, nil, createdAt, true)
	mock.ExpectQuery(`(?s)SELECT.*COUNT\(\*\) FILTER.*FROM delivery_plan_step_assignments a.*a.created_at <= \$2.*AND pr.id = \$3`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), projectID).
		WillReturnRows(sqlmock.NewRows([]string{"blocked_assignments", "expired_plan_step_leases"}).AddRow(1, 1))
	mock.ExpectQuery(`(?s)SELECT.*NULL::text AS target_machine_id.*FROM delivery_plan_step_assignments a.*WHERE a.status NOT IN.*AND pr.id = \$2.*ORDER BY a.created_at ASC, a.id ASC\s+LIMIT \$3`).
		WithArgs(sqlmock.AnyArg(), projectID, dispatchQueueDefaultPageSize+1).
		WillReturnRows(queueRows)

	request := httptest.NewRequest(http.MethodGet, "/api/automation/dispatch/queue?project_id="+projectID.String(), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set("cognito_sub", subject)
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", organizationID)
	if err := GetDispatchQueue(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data dispatchQueuePage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Items) != 1 {
		t.Fatalf("expected one authorized project assignment, got %d", len(envelope.Data.Items))
	}
	if envelope.Data.BlockedAssignments != 1 || envelope.Data.ExpiredPlanStepLeases != 1 {
		t.Fatalf("project-scoped counters missing: %#v", envelope.Data)
	}
	item := envelope.Data.Items[0]
	if item.ProjectID != projectID || item.TargetMachineID != nil || item.TargetLastSeenAt != nil || item.TargetConcurrency != 0 || item.TargetActiveRuns != 0 || item.TargetAvailableSlots != 0 || item.TargetAvailability != "unknown" {
		t.Fatalf("organization queue returned worker-wide signals or the wrong project: %#v", item)
	}
	if strings.Contains(recorder.Body.String(), `"target_machine_id":"`) || strings.Contains(recorder.Body.String(), `"target_last_seen_at":"`) {
		t.Fatalf("organization queue response exposed worker-wide identity or heartbeat: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetDispatchQueueOrganizationProjectAuthorizationStopsBeforeQueueQuery(t *testing.T) {
	for _, test := range []struct {
		name              string
		projectClientID   func(organizationID uuid.UUID) uuid.UUID
		role              string
		permissions       string
		wantStatus        int
		wantMembershipSQL bool
	}{
		{
			name:            "project outside selected organization",
			projectClientID: func(organizationID uuid.UUID) uuid.UUID { return uuid.Must(uuid.NewV4()) },
			wantStatus:      http.StatusNotFound,
		},
		{
			name:              "project member without view permission",
			projectClientID:   func(organizationID uuid.UUID) uuid.UUID { return organizationID },
			role:              "contributor",
			permissions:       `[]`,
			wantStatus:        http.StatusForbidden,
			wantMembershipSQL: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock := automationCostLedgerTestDB(t)
			previousDB := configuration.DB
			configuration.DB = db
			t.Cleanup(func() { configuration.DB = previousDB })

			organizationID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			projectClientID := test.projectClientID(organizationID)
			subject := "dispatch-project-restricted"
			restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
				return &models.User{CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone}, nil
			}})
			t.Cleanup(restore)

			mock.ExpectQuery(`WITH RECURSIVE organization_clients`).
				WithArgs(organizationID).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(organizationID))
			mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
				WithArgs(projectID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, projectClientID))
			if test.wantMembershipSQL {
				mock.ExpectQuery(`SELECT \* FROM "delivery_project_members" WHERE project_id = \$1 AND cognito_sub = \$2 ORDER BY "delivery_project_members"\."id" LIMIT \$3`).
					WithArgs(projectID, subject, 1).
					WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "cognito_sub", "role", "permissions", "created_by", "created_at", "updated_at"}).
						AddRow(uuid.Must(uuid.NewV4()), projectID, subject, test.role, test.permissions, "project-admin", time.Now().UTC(), time.Now().UTC()))
			}

			request := httptest.NewRequest(http.MethodGet, "/api/automation/dispatch/queue?project_id="+projectID.String(), nil)
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			ctx.Set("cognito_sub", subject)
			ctx.Set("workspace_mode", "organization")
			ctx.Set("organization_id", organizationID)
			_ = GetDispatchQueue(ctx)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), projectID.String()) || strings.Contains(recorder.Body.String(), projectClientID.String()) {
				t.Fatalf("denial response disclosed project identifiers: %s", recorder.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("queue query must not run after project authorization fails: %v", err)
			}
		})
	}
}

func TestDispatchQueueFiltersCursorAndReadiness(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
		valid bool
	}{
		{name: "defaults", valid: true},
		{name: "bounded max", query: "?page_size=100", valid: true},
		{name: "page size too large", query: "?page_size=101"},
		{name: "page size zero", query: "?page_size=0"},
		{name: "terminal statuses are excluded from this live queue", query: "?status=completed"},
		{name: "unknown status", query: "?status=secret"},
		{name: "malformed project", query: "?project_id=not-a-uuid"},
		{name: "invalid agent key", query: "?agent_key=Bearer-secret"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/automation/dispatch/queue"+test.query, nil)
			filters, err := parseDispatchQueueFilters(echo.New().NewContext(request, httptest.NewRecorder()))
			if test.valid && err != nil {
				t.Fatalf("valid filters rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid filters unexpectedly accepted")
			}
			if test.name == "defaults" && filters.PageSize != dispatchQueueDefaultPageSize {
				t.Fatalf("default page_size = %d", filters.PageSize)
			}
		})
	}

	projectID, assignmentID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	filters := dispatchQueueFilters{PageSize: 10, Status: "queued", Project: &projectID, AgentKey: "generalist", IncludeWorkerSignals: true}
	scope := dispatchQueueCursorScope(filters, "actor-a", "platform", uuid.Nil)
	position := time.Now().UTC().Add(-time.Minute)
	encoded := encodeDispatchQueueCursor(dispatchQueueCursor{Version: 1, Scope: scope, Snapshot: time.Now().UTC(), CreatedAt: position, ID: assignmentID.String()})
	if _, err := decodeDispatchQueueCursor(encoded, scope); err != nil {
		t.Fatalf("valid cursor rejected: %v", err)
	}
	for _, mismatch := range []string{
		dispatchQueueCursorScope(dispatchQueueFilters{PageSize: 10, Status: "queued", Project: &projectID, AgentKey: "different"}, "actor-a", "platform", uuid.Nil),
		dispatchQueueCursorScope(filters, "actor-b", "platform", uuid.Nil),
		dispatchQueueCursorScope(dispatchQueueFilters{PageSize: 25, Status: "queued", Project: &projectID, AgentKey: "generalist"}, "actor-a", "platform", uuid.Nil),
	} {
		if _, err := decodeDispatchQueueCursor(encoded, mismatch); err == nil {
			t.Fatal("cursor from another actor or filter set was accepted")
		}
	}
	organizationID, otherOrganizationID, otherProjectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	organizationFilters := dispatchQueueFilters{PageSize: 10, Status: "queued", Project: &projectID, AgentKey: "generalist"}
	organizationScope := dispatchQueueCursorScope(organizationFilters, "org-actor", "organization", organizationID)
	organizationCursor := encodeDispatchQueueCursor(dispatchQueueCursor{Version: 1, Scope: organizationScope, Snapshot: time.Now().UTC(), CreatedAt: position, ID: assignmentID.String()})
	for _, mismatch := range []string{
		dispatchQueueCursorScope(organizationFilters, "other-actor", "organization", organizationID),
		dispatchQueueCursorScope(organizationFilters, "org-actor", "organization", otherOrganizationID),
		dispatchQueueCursorScope(dispatchQueueFilters{PageSize: 10, Status: "queued", Project: &otherProjectID, AgentKey: "generalist"}, "org-actor", "organization", organizationID),
	} {
		if _, err := decodeDispatchQueueCursor(organizationCursor, mismatch); err == nil {
			t.Fatal("organization cursor from another actor, organization, or project was accepted")
		}
	}
	if _, err := decodeDispatchQueueCursor(strings.Repeat("x", dispatchQueueMaxCursorBytes+1), scope); err == nil {
		t.Fatal("oversized cursor unexpectedly accepted")
	}

	query, args := dispatchQueueQuery(filters, &dispatchQueueCursor{Snapshot: time.Now().UTC(), CreatedAt: position, ID: assignmentID.String()}, time.Now().UTC())
	for _, expected := range []string{
		"a.status NOT IN ('completed', 'failed', 'cancelled')",
		"FROM delivery_plan_step_assignments a",
		"JOIN delivery_plan_executions e",
		"JOIN delivery_plan_steps s",
		"LEFT JOIN LATERAL",
		"automation_agent_heartbeats h",
		"active.status IN ('running', 'cancel_requested')",
		"delivery_plan_step_dependencies",
		"(a.created_at, a.id) > (?, ?::uuid)",
		"ORDER BY a.created_at ASC, a.id ASC",
	} {
		if !strings.Contains(query, expected) {
			t.Fatalf("queue query omits %q: %s", expected, query)
		}
	}
	if len(args) != 7 || args[len(args)-1] != filters.PageSize+1 {
		t.Fatalf("unexpected query args: %#v", args)
	}
	organizationQuery, _ := dispatchQueueQuery(dispatchQueueFilters{PageSize: 25, Project: &projectID}, nil, time.Now().UTC())
	if strings.Contains(organizationQuery, "automation_agent_heartbeats") || strings.Contains(organizationQuery, "LEFT JOIN LATERAL") || !strings.Contains(organizationQuery, "NULL::text AS target_machine_id") {
		t.Fatalf("organization queue query must omit machine/heartbeat signals: %s", organizationQuery)
	}

	snapshot := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	snapshotQuery, snapshotArgs := dispatchQueueSnapshotQuery(filters, snapshot)
	for _, expected := range []string{
		"COUNT(*) FILTER (WHERE a.status = 'blocked') AS blocked_assignments",
		"COUNT(DISTINCT s.id) FILTER (",
		"s.status = 'running'",
		"s.lease_expires_at <= ?",
		"a.created_at <= ?",
		"AND a.status = ?",
		"AND pr.id = ?",
		"AND a.target_agent_key = ?",
	} {
		if !strings.Contains(snapshotQuery, expected) {
			t.Fatalf("filtered dispatch snapshot query omits %q: %s", expected, snapshotQuery)
		}
	}
	if strings.Contains(snapshotQuery, "automation_agent_heartbeats") || strings.Contains(snapshotQuery, "target_machine_id") {
		t.Fatalf("dispatch counters must not use worker-wide signals: %s", snapshotQuery)
	}
	if len(snapshotArgs) != 5 || snapshotArgs[0] != snapshot || snapshotArgs[1] != snapshot || snapshotArgs[2] != "queued" || snapshotArgs[3] != projectID || snapshotArgs[4] != "generalist" {
		t.Fatalf("snapshot counters did not bind the fixed time and filters: %#v", snapshotArgs)
	}
}

func TestDispatchQueueTargetAvailabilityUsesRecentHeartbeatAndSlots(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		row       dispatchQueueRow
		status    string
		freeSlots int
	}{
		{name: "missing heartbeat", row: dispatchQueueRow{}, status: "unknown"},
		{name: "stale heartbeat", row: dispatchQueueRow{TargetLastSeenAt: timePointer(now.Add(-dispatchQueueHeartbeatStale))}, status: "offline"},
		{name: "draining", row: dispatchQueueRow{TargetLastSeenAt: timePointer(now.Add(-time.Second)), TargetConcurrency: 2, TargetDraining: true}, status: "draining"},
		{name: "saturated", row: dispatchQueueRow{TargetLastSeenAt: timePointer(now.Add(-time.Second)), TargetConcurrency: 2, TargetActiveRuns: 2}, status: "saturated"},
		{name: "working with one available slot", row: dispatchQueueRow{TargetLastSeenAt: timePointer(now.Add(-time.Second)), TargetConcurrency: 3, TargetActiveRuns: 2}, status: "working", freeSlots: 1},
		{name: "available", row: dispatchQueueRow{TargetLastSeenAt: timePointer(now.Add(-time.Second)), TargetConcurrency: 3}, status: "available", freeSlots: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, slots := dispatchQueueTargetAvailability(test.row, now)
			if status != test.status || slots != test.freeSlots {
				t.Fatalf("availability = %q with %d free slots; want %q with %d", status, slots, test.status, test.freeSlots)
			}
		})
	}
}

func timePointer(value time.Time) *time.Time { return &value }
