package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func TestContinuationRequiresExplicitSafeTransition(t *testing.T) {
	tests := map[deliveryworkflow.Action]string{deliveryworkflow.ActionApprovePlan: "implementation", deliveryworkflow.ActionRequestPlanChanges: "plan", deliveryworkflow.ActionRequestCodeChanges: "implementation", deliveryworkflow.ActionRequestQAChanges: "implementation", deliveryworkflow.ActionApproveCodeReview: "preview", deliveryworkflow.ActionPreviewReady: "qa", deliveryworkflow.ActionApproveQA: "summary", deliveryworkflow.ActionApproveRelease: "", deliveryworkflow.ActionSubmitPlan: "", deliveryworkflow.ActionCancel: "", deliveryworkflow.ActionBlock: ""}
	for action, want := range tests {
		if got := continuationAfterAction(action); got != want {
			t.Fatalf("%s => %s, want %s", action, got, want)
		}
	}
}

func TestPreviewCannotReuseOlderWorktree(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4()).String()
	branch := "itbem-agent/" + taskID
	digestBytes := sha256.Sum256([]byte("reviewed diff"))
	digest := hex.EncodeToString(digestBytes[:])
	review := models.DeliveryChangeSet{RepositoryRef: "workspace://repo", Branch: branch, ReviewType: "local_worktree", CIStatus: "passed", CreatedBy: "itbem-local-agent", MetadataJSON: `{"verification_source":"itbem-local-agent","automation_task_id":"` + taskID + `","worktree":"workspace://repo#` + branch + `","review_diff_sha256":"` + digest + `"}`}
	publication := models.DeliveryChangeSet{RepositoryRef: review.RepositoryRef, Branch: branch, ReviewType: "pull_request", CIStatus: "passed", PreviewURL: "https://preview.example.test", MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app","review_diff_sha256":"` + digest + `"}`}
	required := map[string]struct{}{review.RepositoryRef: {}}
	if got := currentReviewedPreview(required, []models.DeliveryChangeSet{publication, review}); got != publication.PreviewURL {
		t.Fatalf("missing current preview: %s", got)
	}
	newReview := review
	newReview.Branch = "itbem-agent/" + uuid.Must(uuid.NewV4()).String()
	if currentReviewedPreview(required, []models.DeliveryChangeSet{newReview, publication, review}) != "" {
		t.Fatal("old preview accepted for new review")
	}
	publication.CIStatus = "pending"
	if currentReviewedPreview(required, []models.DeliveryChangeSet{publication, review}) != "" {
		t.Fatal("pending CI accepted")
	}
}

func TestPreviewCannotReusePublicationFromAnotherReviewedDiff(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4()).String()
	branch := "itbem-agent/" + taskID
	reviewDigest := strings.Repeat("a", 64)
	otherDigest := strings.Repeat("b", 64)
	review := models.DeliveryChangeSet{RepositoryRef: "workspace://repo", Branch: branch, ReviewType: "local_worktree", CIStatus: "passed", CreatedBy: "itbem-local-agent", MetadataJSON: `{"verification_source":"itbem-local-agent","automation_task_id":"` + taskID + `","worktree":"workspace://repo#` + branch + `","review_diff_sha256":"` + reviewDigest + `"}`}
	publication := models.DeliveryChangeSet{RepositoryRef: review.RepositoryRef, Branch: branch, ReviewType: "pull_request", CIStatus: "passed", PreviewURL: "https://preview.example.test", MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app","review_diff_sha256":"` + otherDigest + `"}`}
	if got := currentReviewedPreview(map[string]struct{}{review.RepositoryRef: {}}, []models.DeliveryChangeSet{publication, review}); got != "" {
		t.Fatalf("publication from another diff accepted: %s", got)
	}
}

func TestPreviewRequiresEveryChangedRepositoryToHaveItsOwnPreview(t *testing.T) {
	digest := strings.Repeat("a", 64)
	firstTaskID := uuid.Must(uuid.NewV4()).String()
	secondTaskID := uuid.Must(uuid.NewV4()).String()
	firstBranch := "itbem-agent/" + firstTaskID
	secondBranch := "itbem-agent/" + secondTaskID
	firstReview := models.DeliveryChangeSet{RepositoryRef: "workspace://frontend", Branch: firstBranch, ReviewType: "local_worktree", CIStatus: "passed", CreatedBy: "itbem-local-agent", MetadataJSON: `{"verification_source":"itbem-local-agent","automation_task_id":"` + firstTaskID + `","worktree":"workspace://frontend#` + firstBranch + `","review_diff_sha256":"` + digest + `"}`}
	secondReview := models.DeliveryChangeSet{RepositoryRef: "workspace://backend", Branch: secondBranch, ReviewType: "local_worktree", CIStatus: "passed", CreatedBy: "itbem-local-agent", MetadataJSON: `{"verification_source":"itbem-local-agent","automation_task_id":"` + secondTaskID + `","worktree":"workspace://backend#` + secondBranch + `","review_diff_sha256":"` + digest + `"}`}
	firstPublication := models.DeliveryChangeSet{RepositoryRef: firstReview.RepositoryRef, Branch: firstBranch, ReviewType: "pull_request", CIStatus: "passed", PreviewURL: "https://frontend.preview.example.test", MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app","review_diff_sha256":"` + digest + `"}`}
	secondPublication := models.DeliveryChangeSet{RepositoryRef: secondReview.RepositoryRef, Branch: secondBranch, ReviewType: "pull_request", CIStatus: "passed", MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app","review_diff_sha256":"` + digest + `"}`}
	required := map[string]struct{}{firstReview.RepositoryRef: {}, secondReview.RepositoryRef: {}}
	if got := currentReviewedPreview(required, []models.DeliveryChangeSet{firstPublication, secondPublication, firstReview, secondReview}); got != "" {
		t.Fatalf("preview accepted without a preview for every changed repository: %s", got)
	}
}

func TestPreviewRejectsAmbiguousMultiRepositoryURLs(t *testing.T) {
	digest := strings.Repeat("c", 64)
	firstTaskID := uuid.Must(uuid.NewV4()).String()
	secondTaskID := uuid.Must(uuid.NewV4()).String()
	firstBranch := "itbem-agent/" + firstTaskID
	secondBranch := "itbem-agent/" + secondTaskID
	firstReview := models.DeliveryChangeSet{RepositoryRef: "workspace://frontend", Branch: firstBranch, ReviewType: "local_worktree", CIStatus: "passed", CreatedBy: "itbem-local-agent", MetadataJSON: `{"verification_source":"itbem-local-agent","automation_task_id":"` + firstTaskID + `","worktree":"workspace://frontend#` + firstBranch + `","review_diff_sha256":"` + digest + `"}`}
	secondReview := models.DeliveryChangeSet{RepositoryRef: "workspace://backend", Branch: secondBranch, ReviewType: "local_worktree", CIStatus: "passed", CreatedBy: "itbem-local-agent", MetadataJSON: `{"verification_source":"itbem-local-agent","automation_task_id":"` + secondTaskID + `","worktree":"workspace://backend#` + secondBranch + `","review_diff_sha256":"` + digest + `"}`}
	firstPublication := models.DeliveryChangeSet{RepositoryRef: firstReview.RepositoryRef, Branch: firstBranch, ReviewType: "pull_request", CIStatus: "passed", PreviewURL: "https://frontend.preview.example.test", MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app","review_diff_sha256":"` + digest + `"}`}
	secondPublication := models.DeliveryChangeSet{RepositoryRef: secondReview.RepositoryRef, Branch: secondBranch, ReviewType: "pull_request", CIStatus: "passed", PreviewURL: "https://backend.preview.example.test", MetadataJSON: `{"branch_published":true,"verification_source":"itbem-github-app","review_diff_sha256":"` + digest + `"}`}
	required := map[string]struct{}{firstReview.RepositoryRef: {}, secondReview.RepositoryRef: {}}
	if got := currentReviewedPreview(required, []models.DeliveryChangeSet{firstPublication, secondPublication, firstReview, secondReview}); got != "" {
		t.Fatalf("ambiguous repository previews must not be projected as one canonical preview: %s", got)
	}
	secondPublication.PreviewURL = firstPublication.PreviewURL
	if got := currentReviewedPreview(required, []models.DeliveryChangeSet{firstPublication, secondPublication, firstReview, secondReview}); got != firstPublication.PreviewURL {
		t.Fatalf("shared integrated preview should satisfy both repositories: %s", got)
	}
}

func TestCompletedContinuationFromOldDecisionCannotAdvance(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	itemID, intentID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .*delivery_work_items.*FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"id", "automation_epoch", "state"}).AddRow(itemID, 2, "implementation"))
	mock.ExpectExec(`UPDATE "delivery_continuations"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := completeContinuation(db, models.DeliveryContinuation{ID: intentID, WorkItemID: itemID, Epoch: 1, Phase: "plan"}, models.AutomationTask{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestImplementationAdmissionReservesEntireAgentBudget(t *testing.T) {
	got, err := deliveryRunBudgetReservation(nil, "delivery.implementation", 100, 8192)
	if err != nil {
		t.Fatal(err)
	}
	perCall, err := projectBudgetReservation(nil, automationagent.AgentMaxRequestBytes, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if got != perCall*automationagent.AgentMaxCalls {
		t.Fatalf("incomplete budget: %d", got)
	}
}
