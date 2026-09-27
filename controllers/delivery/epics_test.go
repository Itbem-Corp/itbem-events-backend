package delivery

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
	"events-stocks/services/deliveryworkflow"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func newEpicTestDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db, mock
}

func configureEpicTestAuth(t *testing.T) {
	t.Helper()
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)
}

func epicTestContext(method, target string, paramNames, paramValues []string, body string) (echo.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(method, target, strings.NewReader(body)), recorder)
	ctx.Request().Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	ctx.Set("cognito_sub", "epic-test-operator")
	ctx.SetParamNames(paramNames...)
	ctx.SetParamValues(paramValues...)
	return ctx, recorder
}

func TestDeliveryEpicCursorIsStableAndBoundToResultScope(t *testing.T) {
	projectID, epicID, rowID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 9, 22, 12, 30, 5, 123000000, time.UTC)
	for _, scope := range []string{epicListCursorScope(projectID, "active"), epicTaskCursorScope(epicID, "")} {
		encoded := encodeDeliveryEpicCursor(deliveryEpicCursor{Version: 1, Scope: scope, CreatedAt: createdAt, ID: rowID.String()})
		decoded, err := decodeDeliveryEpicCursor(encoded, scope)
		if err != nil || decoded == nil || decoded.ID != rowID.String() || !decoded.CreatedAt.Equal(createdAt) {
			t.Fatalf("cursor did not round-trip: %#v, %v", decoded, err)
		}
	}
	valid := encodeDeliveryEpicCursor(deliveryEpicCursor{Version: 1, Scope: epicListCursorScope(projectID, "active"), CreatedAt: createdAt, ID: rowID.String()})
	if _, err := decodeDeliveryEpicCursor(valid, epicListCursorScope(uuid.Must(uuid.NewV4()), "active")); err == nil {
		t.Fatal("cursor from a different project must be rejected")
	}
	if _, err := decodeDeliveryEpicCursor(valid, epicListCursorScope(projectID, "planned")); err == nil {
		t.Fatal("cursor from a different filter must be rejected")
	}
	if _, err := decodeDeliveryEpicCursor("not-a-cursor", epicListCursorScope(projectID, "active")); err == nil {
		t.Fatal("malformed cursor must be rejected")
	}
}

func TestDeliveryEpicPageSizeIsBounded(t *testing.T) {
	if got, err := epicPageSize(""); err != nil || got != defaultEpicPageSize {
		t.Fatalf("default page size = %d, %v", got, err)
	}
	for _, raw := range []string{"0", "-1", "101", "many"} {
		if _, err := epicPageSize(raw); err == nil {
			t.Fatalf("invalid page size %q was accepted", raw)
		}
	}
}

func TestProjectCanManageUsesEffectiveProjectMembership(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	projectID := uuid.Must(uuid.NewV4())
	query := `SELECT "role","permissions" FROM "delivery_project_members" WHERE (project_id = \$1 AND cognito_sub = \$2) ORDER BY "delivery_project_members"."id" LIMIT \$3`
	mock.ExpectQuery(query).
		WithArgs(projectID, "project-manager", 1).
		WillReturnRows(sqlmock.NewRows([]string{"role", "permissions"}).AddRow("viewer", ` ["delivery:manage"] `))
	canManage, err := projectCanManage(projectID, &models.User{CognitoSub: "project-manager"})
	if err != nil || !canManage {
		t.Fatalf("explicit project manager permission = %v, %v; want true", canManage, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryEpicListReturnsKeysetPageAndSafeProjection(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	configureEpicTestAuth(t)

	projectID := uuid.Must(uuid.NewV4())
	firstID, nextID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT .*FROM "delivery_projects".*`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(projectID))
	listQuery := regexp.QuoteMeta("SELECT delivery_epics.id, delivery_epics.project_id, delivery_epics.title, delivery_epics.summary, delivery_epics.status, delivery_epics.created_at, delivery_epics.updated_at,") + `.*FROM "delivery_epics".*WHERE delivery_epics.project_id = \$1 AND delivery_epics.status = \$2 ORDER BY delivery_epics.created_at DESC, delivery_epics.id DESC LIMIT \$3`
	mock.ExpectQuery(listQuery).
		WithArgs(projectID, "active", 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "title", "summary", "status", "created_at", "updated_at", "task_count"}).
			AddRow(firstID, projectID, "Operational dashboard", "Keep the delivery scope clear", "active", createdAt, createdAt, 2).
			AddRow(nextID, projectID, "Billing", "", "active", createdAt.Add(-time.Minute), createdAt, 1))

	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/projects/"+projectID.String()+"/epics?status=active&limit=1", []string{"id"}, []string{projectID.String()}, "")
	ctx.Set("workspace_mode", "platform")
	if err := ListEpics(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data deliveryEpicPage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.CanManage || len(envelope.Data.Items) != 1 || envelope.Data.Items[0].ID != firstID || envelope.Data.Items[0].TaskCount != 2 || envelope.Data.NextCursor == "" {
		t.Fatalf("unexpected epic page: %#v", envelope.Data)
	}
	if strings.Contains(recorder.Body.String(), "private_ref") || strings.Contains(recorder.Body.String(), "prompt") || strings.Contains(recorder.Body.String(), "description") {
		t.Fatalf("epic list exposed a forbidden field: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetEpicReturnsPagedWorkItemsWithoutTaskContext(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	configureEpicTestAuth(t)

	epicID, projectID, workItemID, membershipID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "delivery_epics" WHERE id = $1 ORDER BY "delivery_epics"."id" LIMIT $2`)).
		WithArgs(epicID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at"}).
			AddRow(epicID, projectID, "Agent operations", "Coordinate work safely", "active", "private-user", createdAt, createdAt))
	mock.ExpectQuery(`SELECT count\(\*\) FROM delivery_epic_work_items AS membership JOIN delivery_work_items AS work_item.*WHERE membership\.epic_id = \$1 AND membership\.project_id = \$2 AND membership\.deleted_at IS NULL`).
		WithArgs(epicID, projectID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT membership\.id AS membership_id, membership\.created_at AS added_at, work_item\.id AS id, work_item\.title AS title, work_item\.state AS state, work_item\.created_at AS created_at, work_item\.updated_at AS updated_at FROM delivery_epic_work_items AS membership JOIN delivery_work_items AS work_item.*WHERE membership\.epic_id = \$1 AND membership\.project_id = \$2 AND membership\.deleted_at IS NULL ORDER BY membership\.created_at DESC, membership\.id DESC LIMIT \$3`).
		WithArgs(epicID, projectID, 26).
		WillReturnRows(sqlmock.NewRows([]string{"membership_id", "added_at", "id", "title", "state", "created_at", "updated_at"}).
			AddRow(membershipID, createdAt, workItemID, "Review queue contract", "planning", createdAt.Add(-time.Hour), createdAt))

	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/epics/"+epicID.String()+"?tasks_limit=25", []string{"id"}, []string{epicID.String()}, "")
	ctx.Set("workspace_mode", "platform")
	if err := GetEpic(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data deliveryEpicDetail `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.CanManage || envelope.Data.Epic.ID != epicID || envelope.Data.Epic.TaskCount != 1 || len(envelope.Data.WorkItems.Items) != 1 || envelope.Data.WorkItems.Items[0].ID != workItemID {
		t.Fatalf("unexpected epic detail page: %#v", envelope.Data)
	}
	for _, forbidden := range []string{"private-user", "description", "mandate", "prompt", "s3://", "input_ref", "output_ref"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("epic detail leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateEpicNormalizesAndPersistsOnlyAllowlistedFields(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	configureEpicTestAuth(t)

	epicID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	epicRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at"}).
			AddRow(epicID, projectID, "Old title", "Old context", "planned", "private-user", createdAt, createdAt)
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "delivery_epics" WHERE id = $1 ORDER BY "delivery_epics"."id" LIMIT $2`)).
		WithArgs(epicID, 1).WillReturnRows(epicRows())
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "delivery_epics" WHERE id = \$1 AND project_id = \$2 ORDER BY "delivery_epics"\."id" LIMIT \$3 FOR UPDATE`).
		WithArgs(epicID, projectID, 1).WillReturnRows(epicRows())
	mock.ExpectExec(`UPDATE "delivery_epics" SET .*WHERE id = \$5 AND project_id = \$6`).
		WithArgs("active", "Bounded project context", "API delivery work", sqlmock.AnyArg(), epicID, projectID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT count\(\*\) FROM delivery_epic_work_items AS membership JOIN delivery_work_items AS work_item.*WHERE membership\.epic_id = \$1 AND membership\.project_id = \$2 AND membership\.deleted_at IS NULL`).
		WithArgs(epicID, projectID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	body := `{"title":"  API   delivery work  ","summary":" Bounded project context ","status":" ACTIVE "}`
	ctx, recorder := epicTestContext(http.MethodPut, "/api/automation/epics/"+epicID.String(), []string{"id"}, []string{epicID.String()}, body)
	ctx.Set("workspace_mode", "platform")
	if err := UpdateEpic(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data deliveryEpicSummary `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Title != "API delivery work" || envelope.Data.Summary != "Bounded project context" || envelope.Data.Status != "active" || envelope.Data.TaskCount != 3 {
		t.Fatalf("updated epic was not normalized or projected safely: %#v", envelope.Data)
	}
	if strings.Contains(recorder.Body.String(), "private-user") || strings.Contains(recorder.Body.String(), "created_by") {
		t.Fatalf("update response leaked model-only fields: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateEpicRequiresProjectManagePermission(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	viewerID := "epic-viewer"
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{CognitoSub: viewerID}, nil
	}})
	t.Cleanup(restore)

	epicID, projectID, clientID, organizationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "delivery_epics" WHERE id = $1 ORDER BY "delivery_epics"."id" LIMIT $2`)).
		WithArgs(epicID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at"}).
			AddRow(epicID, projectID, "Epic", "Context", "planned", "private-user", createdAt, createdAt))
	mock.ExpectQuery(`WITH RECURSIVE organization_clients`).
		WithArgs(organizationID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(clientID))
	mock.ExpectQuery(`SELECT "id","client_id" FROM "delivery_projects" WHERE id = \$1 AND "delivery_projects"\."deleted_at" IS NULL ORDER BY "delivery_projects"\."id" LIMIT \$2`).
		WithArgs(projectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id"}).AddRow(projectID, clientID))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "delivery_project_members" WHERE project_id = $1 AND cognito_sub = $2 ORDER BY "delivery_project_members"."id" LIMIT $3`)).
		WithArgs(projectID, viewerID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "cognito_sub", "role", "permissions"}).
			AddRow(uuid.Must(uuid.NewV4()), projectID, viewerID, "viewer", `[]`))

	ctx, recorder := epicTestContext(http.MethodPut, "/api/automation/epics/"+epicID.String(), []string{"id"}, []string{epicID.String()}, `{"title":"Changed","summary":"Changed","status":"active"}`)
	ctx.Set("workspace_mode", "organization")
	ctx.Set("organization_id", organizationID)
	if err := UpdateEpic(ctx); err == nil {
		t.Fatal("viewer must not update epic context")
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateEpicRejectsCredentialLikeContextWithoutPersistence(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	configureEpicTestAuth(t)

	epicID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "delivery_epics" WHERE id = $1 ORDER BY "delivery_epics"."id" LIMIT $2`)).
		WithArgs(epicID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at"}).
			AddRow(epicID, projectID, "Epic", "Old context", "planned", "private-user", createdAt, createdAt))

	secret := "sk-proj-1234567890abcdefghijklmnop" // gitleaks:allow synthetic test-only security canary; never used for provider access
	body := `{"title":"Epic","summary":"OPENAI_API_KEY=` + secret + `","status":"active"}`
	ctx, recorder := epicTestContext(http.MethodPut, "/api/automation/epics/"+epicID.String(), []string{"id"}, []string{epicID.String()}, body)
	ctx.Set("workspace_mode", "platform")
	if err := UpdateEpic(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("credential-like summary status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("validation response echoed a secret-like value: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateEpicRejectsCredentialLikeContextBeforePersistence(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	configureEpicTestAuth(t)

	projectID := uuid.Must(uuid.NewV4())
	secret := "sk-proj-1234567890abcdefghijklmnop" // gitleaks:allow synthetic test-only security canary; never used for provider access
	ctx, recorder := epicTestContext(http.MethodPost, "/api/automation/projects/"+projectID.String()+"/epics", []string{"id"}, []string{projectID.String()}, `{"title":"Epic","summary":"OPENAI_API_KEY=`+secret+`"}`)
	ctx.Set("workspace_mode", "platform")
	if err := CreateEpic(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("credential-like summary status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("validation response echoed a secret-like value: %s", recorder.Body.String())
	}

	unknown, unknownRecorder := epicTestContext(http.MethodPost, "/api/automation/projects/"+projectID.String()+"/epics", []string{"id"}, []string{projectID.String()}, `{"title":"Epic","summary":"Safe","prompt":"not accepted"}`)
	unknown.Set("workspace_mode", "platform")
	if err := CreateEpic(unknown); err != nil {
		t.Fatal(err)
	}
	if unknownRecorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown create field status = %d, want 400: %s", unknownRecorder.Code, unknownRecorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("invalid epic requests reached persistence: %v", err)
	}
}

func TestEpicSensitiveMaterialDetectionAndStrictUpdateBody(t *testing.T) {
	privateURL := "https://" + "fixture-user" + ":" + strings.Repeat("x", 16) + "@example.test/private"
	for _, value := range []string{
		"OPENAI_API_KEY=not-a-real-key",
		"Authorization: Bearer abcdefghijklmnop",
		"-----BEGIN RSA PRIVATE KEY-----", // gitleaks:allow synthetic test-only security canary; never used for provider access
		"sk-proj-1234567890abcdefghijklmnop", // gitleaks:allow synthetic test-only security canary; never used for provider access
		"AIza12345678901234567890123456789012",
		"glpat-123456789012345678901234", // gitleaks:allow synthetic test-only security canary; never used for provider access
		"eyJhbGciOiJub25lIn0.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature-material-for-test", // gitleaks:allow synthetic test-only security canary; never used for provider access
		privateURL,
		"https://example.test/?access_token=abcdefghijklmnop",
		"https://example.test/?key=abcdefghijklmnop",
	} {
		if !containsEpicSensitiveMaterial(value) {
			t.Errorf("sensitive value was not detected: %q", value)
		}
	}
	for _, value := range []string{"API integrations for the platform", "Use the GitHub repository", "Coordinate step planning"} {
		if containsEpicSensitiveMaterial(value) {
			t.Errorf("ordinary context was rejected: %q", value)
		}
	}

	for _, body := range []string{
		`{"title":"Epic","summary":"Context","status":"planned","agent_prompt":"private"}`,
		`{"title":"Epic","summary":"Context","status":"planned"}{"title":"Other"}`,
	} {
		ctx, recorder := epicTestContext(http.MethodPut, "/api/automation/epics/id", nil, nil, body)
		var input updateDeliveryEpicInput
		if err := decodeDeliveryEpicUpdate(ctx, &input); err == nil {
			t.Errorf("invalid update body accepted: %s", body)
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("decoder unexpectedly wrote an HTTP response: %s", recorder.Body.String())
		}
	}
}

func TestGetEpicFiltersWorkItemsByExactStateAndPaginatesWithinFilter(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	configureEpicTestAuth(t)

	epicID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	firstID, secondID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	firstMembershipID, secondMembershipID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "delivery_epics" WHERE id = $1 ORDER BY "delivery_epics"."id" LIMIT $2`)).
		WithArgs(epicID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at"}).
			AddRow(epicID, projectID, "Agent operations", "Coordinate work safely", "active", "private-user", createdAt, createdAt))
	mock.ExpectQuery(`SELECT count\(\*\) FROM delivery_epic_work_items AS membership JOIN delivery_work_items AS work_item.*WHERE \(membership\.epic_id = \$1 AND membership\.project_id = \$2 AND membership\.deleted_at IS NULL\) AND work_item\.state = \$3`).
		WithArgs(epicID, projectID, deliveryworkflow.StatePlanning).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`SELECT membership\.id AS membership_id, membership\.created_at AS added_at, work_item\.id AS id, work_item\.title AS title, work_item\.state AS state, work_item\.created_at AS created_at, work_item\.updated_at AS updated_at FROM delivery_epic_work_items AS membership JOIN delivery_work_items AS work_item.*WHERE \(membership\.epic_id = \$1 AND membership\.project_id = \$2 AND membership\.deleted_at IS NULL\) AND work_item\.state = \$3 ORDER BY membership\.created_at DESC, membership\.id DESC LIMIT \$4`).
		WithArgs(epicID, projectID, deliveryworkflow.StatePlanning, 2).
		WillReturnRows(sqlmock.NewRows([]string{"membership_id", "added_at", "id", "title", "state", "created_at", "updated_at"}).
			AddRow(firstMembershipID, createdAt, firstID, "Review queue contract", "planning", createdAt.Add(-time.Hour), createdAt).
			AddRow(secondMembershipID, createdAt.Add(-time.Minute), secondID, "Implement lease renewal", "planning", createdAt.Add(-time.Hour), createdAt))

	ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/epics/"+epicID.String()+"?tasks_state=planning&tasks_limit=1", []string{"id"}, []string{epicID.String()}, "")
	ctx.Set("workspace_mode", "platform")
	if err := GetEpic(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data deliveryEpicDetail `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.WorkItems.Total != 2 || len(envelope.Data.WorkItems.Items) != 1 || envelope.Data.WorkItems.Items[0].ID != firstID || envelope.Data.WorkItems.Items[0].State != deliveryworkflow.StatePlanning || envelope.Data.WorkItems.NextCursor == "" {
		t.Fatalf("state-filtered page is incorrect: %#v", envelope.Data.WorkItems)
	}
	cursor, err := decodeDeliveryEpicCursor(envelope.Data.WorkItems.NextCursor, epicTaskCursorScope(epicID, deliveryworkflow.StatePlanning))
	if err != nil || cursor == nil {
		t.Fatalf("next cursor must be bound to planning state: %#v, %v", cursor, err)
	}
	if _, err := decodeDeliveryEpicCursor(envelope.Data.WorkItems.NextCursor, epicTaskCursorScope(epicID, deliveryworkflow.StateBlocked)); err == nil {
		t.Fatal("cursor from planning tasks must not be reusable for blocked tasks")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetEpicRejectsInvalidRepeatedAndCrossFilterTaskCursors(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "unknown state", query: "tasks_state=not-a-state"},
		{name: "case mismatch", query: "tasks_state=Planning"},
		{name: "oversized state", query: "tasks_state=" + strings.Repeat("a", maxEpicTaskStateBytes+1)},
		{name: "repeated value", query: "tasks_state=planning&tasks_state=blocked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock := newEpicTestDB(t)
			previousDB := configuration.DB
			configuration.DB = db
			t.Cleanup(func() { configuration.DB = previousDB })
			configureEpicTestAuth(t)

			epicID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			createdAt := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "delivery_epics" WHERE id = $1 ORDER BY "delivery_epics"."id" LIMIT $2`)).
				WithArgs(epicID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at"}).
					AddRow(epicID, projectID, "Agent operations", "Coordinate work safely", "active", "private-user", createdAt, createdAt))

			ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/epics/"+epicID.String()+"?"+test.query, []string{"id"}, []string{epicID.String()}, "")
			ctx.Set("workspace_mode", "platform")
			if err := GetEpic(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("cursor from another state", func(t *testing.T) {
		db, mock := newEpicTestDB(t)
		previousDB := configuration.DB
		configuration.DB = db
		t.Cleanup(func() { configuration.DB = previousDB })
		configureEpicTestAuth(t)

		epicID, projectID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		createdAt := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "delivery_epics" WHERE id = $1 ORDER BY "delivery_epics"."id" LIMIT $2`)).
			WithArgs(epicID, 1).
			WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at"}).
				AddRow(epicID, projectID, "Agent operations", "Coordinate work safely", "active", "private-user", createdAt, createdAt))
		cursor := encodeDeliveryEpicCursor(deliveryEpicCursor{Version: 1, Scope: epicTaskCursorScope(epicID, deliveryworkflow.StateBlocked), CreatedAt: createdAt, ID: uuid.Must(uuid.NewV4()).String()})
		ctx, recorder := epicTestContext(http.MethodGet, "/api/automation/epics/"+epicID.String()+"?tasks_state=planning&tasks_cursor="+cursor, []string{"id"}, []string{epicID.String()}, "")
		ctx.Set("workspace_mode", "platform")
		if err := GetEpic(ctx); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestEpicTaskStateFilterTreatsOmittedAndEmptyAsAllStates(t *testing.T) {
	for _, values := range [][]string{nil, {""}} {
		if state, err := epicTaskStateFilter(values); err != nil || state != "" {
			t.Fatalf("values %#v yielded state %q, %v; want no filter", values, state, err)
		}
	}
	if state, err := epicTaskStateFilter([]string{deliveryworkflow.StateQARunning}); err != nil || state != deliveryworkflow.StateQARunning {
		t.Fatalf("valid exact state yielded %q, %v", state, err)
	}
}

func TestAddWorkItemToEpicRejectsDifferentProjectBeforeWriting(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	configureEpicTestAuth(t)

	epicID, epicProjectID, otherProjectID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	epicRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "project_id", "title", "summary", "status", "created_by", "created_at", "updated_at"}).
			AddRow(epicID, epicProjectID, "Epic", "", "planned", "operator", createdAt, createdAt)
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "delivery_epics" WHERE id = $1 ORDER BY "delivery_epics"."id" LIMIT $2`)).
		WithArgs(epicID, 1).WillReturnRows(epicRows())
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "delivery_epics" WHERE id = \$1 ORDER BY "delivery_epics"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(epicID, 1).WillReturnRows(epicRows())
	mock.ExpectQuery(`SELECT "id","project_id" FROM "delivery_work_items" WHERE \(id = \$1 AND project_id = \$2\) AND "delivery_work_items"\."deleted_at" IS NULL ORDER BY "delivery_work_items"\."id" LIMIT \$3 FOR UPDATE`).
		WithArgs(workItemID, epicProjectID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id"}).AddRow(workItemID, otherProjectID))
	mock.ExpectRollback()

	body, _ := json.Marshal(associateDeliveryEpicWorkItemInput{WorkItemID: workItemID.String()})
	ctx, recorder := epicTestContext(http.MethodPost, "/api/automation/epics/"+epicID.String()+"/work-items", []string{"id"}, []string{epicID.String()}, string(body))
	ctx.Set("workspace_mode", "platform")
	if err := AddWorkItemToEpic(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-project association status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), otherProjectID.String()) || strings.Contains(recorder.Body.String(), "private") {
		t.Fatalf("cross-project response disclosed unrelated data: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryEpicDTODoesNotSerializeModelOnlyFields(t *testing.T) {
	model := models.DeliveryEpic{ID: uuid.Must(uuid.NewV4()), ProjectID: uuid.Must(uuid.NewV4()), Title: "Safe title", Summary: "Safe summary", Status: "planned", CreatedBy: "private-user"}
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-user") || strings.Contains(string(encoded), "project_id") || strings.Contains(string(encoded), "title") {
		t.Fatalf("storage model unexpectedly serializes directly: %s", encoded)
	}
	api, err := json.Marshal(epicSummaryFromModel(model, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"created_by", "private-user", "description", "reference", "prompt", "input_ref", "output_ref"} {
		if strings.Contains(string(api), forbidden) {
			t.Fatalf("safe DTO leaked %q: %s", forbidden, api)
		}
	}
}
