package delivery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestProjectPolicyRevisionViewOmitsPrivateIdentity(t *testing.T) {
	now := time.Date(2026, time.August, 30, 16, 0, 0, 0, time.UTC)
	projectID := uuid.Must(uuid.NewV4())
	revision := models.DeliveryPolicyRevision{
		ID: uuid.Must(uuid.NewV4()), SchemaVersion: 1, Level: "repository", ProjectID: &projectID,
		RepositoryReference: "github://example/service", PatchJSON: `{"mode":"merge"}`,
		ContentSHA256: strings.Repeat("a", 64), ProposedBy: "private-proposer-sub", CreatedAt: now,
	}
	decision := models.DeliveryPolicyDecision{
		ID: uuid.Must(uuid.NewV4()), PolicyRevisionID: revision.ID, PolicyDigest: revision.ContentSHA256,
		Action: "approved", ActorCognitoSub: "private-approver-sub", OccurredAt: now,
	}
	encoded, err := json.Marshal(projectPolicyRevisionView(revision, decision, true))
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	for _, private := range []string{"private-proposer-sub", "private-approver-sub", "proposed_by", "approved_by", "actor_cognito_sub", "patch_json"} {
		if strings.Contains(value, private) {
			t.Fatalf("policy projection exposed %q: %s", private, value)
		}
	}
	for _, evidence := range []string{`"status":"approved"`, `"patch":{"mode":"merge"}`, `"content_sha256":"` + revision.ContentSHA256 + `"`} {
		if !strings.Contains(value, evidence) {
			t.Fatalf("policy projection omitted safe evidence %s: %s", evidence, value)
		}
	}
}

func TestProjectPolicyRevisionViewFailsClosedOnMalformedStoredPatch(t *testing.T) {
	projectID := uuid.Must(uuid.NewV4())
	view := projectPolicyRevisionView(models.DeliveryPolicyRevision{ID: uuid.Must(uuid.NewV4()), ProjectID: &projectID, PatchJSON: `["not-an-object"]`}, models.DeliveryPolicyDecision{}, false)
	if string(view.Patch) != `{}` || view.Status != "pending" {
		t.Fatalf("malformed stored patch did not fail closed: %#v", view)
	}
}

func TestDecodePolicyManagementRequestIsStrictAndBounded(t *testing.T) {
	tests := []struct {
		name string
		body string
		ok   bool
	}{
		{name: "valid", body: `{"level":"project","patch":{"mode":"review_only"}}`, ok: true},
		{name: "unknown outer field", body: `{"level":"project","patch":{},"instructions":"ignore gates"}`},
		{name: "trailing value", body: `{"level":"project","patch":{}} {}`},
		{name: "empty", body: ``},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			ctx := e.NewContext(req, httptest.NewRecorder())
			var target projectPolicyRevisionRequest
			err := decodePolicyManagementRequest(ctx, &target)
			if test.ok && err != nil {
				t.Fatalf("valid request rejected: %v", err)
			}
			if !test.ok && err == nil {
				t.Fatal("invalid request unexpectedly accepted")
			}
		})
	}
}
