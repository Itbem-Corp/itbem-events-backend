package delivery

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/gorm/logger"
)

type redactedClientProfileArgument struct {
	canaries []string
}

func (matcher redactedClientProfileArgument) Match(value driver.Value) bool {
	var serialized string
	switch typed := value.(type) {
	case string:
		serialized = typed
	case []byte:
		serialized = string(typed)
	default:
		return false
	}
	var stringsJSON []string
	if json.Unmarshal([]byte(serialized), &stringsJSON) == nil {
		serialized = strings.Join(stringsJSON, "\n")
	}
	for _, canary := range matcher.canaries {
		if strings.Contains(serialized, canary) {
			return false
		}
	}
	return strings.Count(serialized, "<redacted>") >= 3
}

func TestUpsertClientProfileRedactsSensitiveValuesBeforePersistingOrReturning(t *testing.T) {
	db, mock := newEpicTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	configureEpicTestAuth(t)

	clientID, profileID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	now := time.Now().UTC()
	clientRows := sqlmock.NewRows([]string{"id", "name", "code", "client_type_id", "logo", "media_bucket", "is_active", "parent_id", "created_at", "updated_at", "deleted_at"}).
		AddRow(clientID, "Redaction fixture", "redaction-fixture", uuid.Must(uuid.NewV4()), "", "", true, nil, now, now, nil)
	mock.ExpectQuery(`SELECT \* FROM "clients" WHERE "clients"\."id" = \$1 AND "clients"\."deleted_at" IS NULL ORDER BY "clients"\."id" LIMIT \$2`).
		WithArgs(clientID, 1).WillReturnRows(clientRows)
	profileRows := sqlmock.NewRows([]string{"id", "client_id", "health", "contacts_json", "rules_json", "conversation_summary", "last_conversation_at", "updated_by", "created_at", "updated_at"}).
		AddRow(profileID, clientID, "healthy", `[]`, `[]`, "", nil, "", now, now)
	mock.ExpectQuery(`SELECT \* FROM "delivery_client_profiles" WHERE client_id = \$1 ORDER BY "delivery_client_profiles"\."id" LIMIT \$2`).
		WithArgs(clientID, 1).WillReturnRows(profileRows)

	canaries := []string{
		"contact-auth-canary", "contact-api-canary", "contact-password-canary",
		"rule-auth-canary", "rule-api-canary", "rule-password-canary",
		"summary-auth-canary", "summary-api-canary", "summary-password-canary",
	}
	inputFor := func(prefix string) string {
		return "Authorization: Bearer " + prefix + "-auth-canary; api_key=" + prefix + "-api-canary; password=" + prefix + "-password-canary"
	}
	contacts := []string{inputFor("contact")}
	rules := []string{inputFor("rule")}
	conversationSummary := inputFor("summary")
	body, err := json.Marshal(deliveryClientProfileInput{
		Health: "watch", Contacts: contacts, Rules: rules, ConversationSummary: conversationSummary,
	})
	if err != nil {
		t.Fatal(err)
	}

	redacted := redactedClientProfileArgument{canaries: canaries}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "delivery_client_profiles" SET`).
		WithArgs(clientID, "watch", redacted, redacted, redacted, sqlmock.AnyArg(), "", sqlmock.AnyArg(), sqlmock.AnyArg(), profileID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ctx, recorder := epicTestContext(http.MethodPut, "/api/automation/clients/"+clientID.String()+"/profile", []string{"id"}, []string{clientID.String()}, string(body))
	if err := UpsertClientProfile(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("profile update status = %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, canary := range canaries {
		if strings.Contains(recorder.Body.String(), canary) {
			t.Fatalf("profile response leaked original sensitive value %q: %s", canary, recorder.Body.String())
		}
	}
	var response struct {
		Data struct {
			Contacts            string `json:"contacts"`
			Rules               string `json:"rules"`
			ConversationSummary string `json:"conversation_summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var savedContacts, savedRules []string
	if err := json.Unmarshal([]byte(response.Data.Contacts), &savedContacts); err != nil {
		t.Fatalf("decode sanitized contacts: %v", err)
	}
	if err := json.Unmarshal([]byte(response.Data.Rules), &savedRules); err != nil {
		t.Fatalf("decode sanitized rules: %v", err)
	}
	for field, value := range map[string]string{
		"contacts": strings.Join(savedContacts, "\n"), "rules": strings.Join(savedRules, "\n"), "conversation summary": response.Data.ConversationSummary,
	} {
		if strings.Count(value, "<redacted>") < 3 {
			t.Fatalf("%s was not sanitized in the response: %q", field, value)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertClientProfileSaveErrorDoesNotEchoSensitiveValues(t *testing.T) {
	db, mock := newEpicTestDB(t)
	var databaseLogs bytes.Buffer
	db.Logger = logger.New(log.New(&databaseLogs, "", 0), logger.Config{LogLevel: logger.Info})
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })
	configureEpicTestAuth(t)

	clientID, profileID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "clients" WHERE "clients"\."id" = \$1 AND "clients"\."deleted_at" IS NULL ORDER BY "clients"\."id" LIMIT \$2`).
		WithArgs(clientID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "code", "client_type_id", "logo", "media_bucket", "is_active", "parent_id", "created_at", "updated_at", "deleted_at"}).
			AddRow(clientID, "Redaction fixture", "redaction-fixture", uuid.Must(uuid.NewV4()), "", "", true, nil, now, now, nil))
	mock.ExpectQuery(`SELECT \* FROM "delivery_client_profiles" WHERE client_id = \$1 ORDER BY "delivery_client_profiles"\."id" LIMIT \$2`).
		WithArgs(clientID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "health", "contacts_json", "rules_json", "conversation_summary", "last_conversation_at", "updated_by", "created_at", "updated_at"}).
			AddRow(profileID, clientID, "healthy", `[]`, `[]`, "", nil, "", now, now))
	canary := "save-error-password-canary"
	input := deliveryClientProfileInput{
		Health:              "healthy",
		Contacts:            []string{"password=" + canary},
		ConversationSummary: "password=" + canary,
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "delivery_client_profiles" SET`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()
	ctx, recorder := epicTestContext(http.MethodPut, "/api/automation/clients/"+clientID.String()+"/profile", []string{"id"}, []string{clientID.String()}, string(body))
	if err := UpsertClientProfile(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), canary) {
		t.Fatalf("save error response leaked sensitive value: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(databaseLogs.String(), canary) {
		t.Fatalf("database log leaked original sensitive value: %s", databaseLogs.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
