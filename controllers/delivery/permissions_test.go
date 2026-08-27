package delivery

import (
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"strings"
	"testing"
	"time"
)

func TestDeliveryProjectRoleMatrix(t *testing.T) {
	tests := []struct {
		name       string
		member     models.DeliveryProjectMember
		permission deliveryPermission
		want       bool
	}{
		{"owner manages", models.DeliveryProjectMember{Role: "owner"}, deliveryManage, true},
		{"requester creates requests", models.DeliveryProjectMember{Role: "requester"}, deliveryRequest, true},
		{"requester cannot run agents", models.DeliveryProjectMember{Role: "requester"}, deliveryManage, false},
		{"qa reviewer approves qa", models.DeliveryProjectMember{Role: "qa_reviewer"}, deliveryQA, true},
		{"qa reviewer cannot release", models.DeliveryProjectMember{Role: "qa_reviewer"}, deliveryRelease, false},
		{"explicit release permission", models.DeliveryProjectMember{Role: "viewer", Permissions: `["delivery:release"]`}, deliveryRelease, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := memberAllows(test.member, test.permission); got != test.want {
				t.Fatalf("memberAllows(%q, %q) = %v, want %v", test.member.Role, test.permission, got, test.want)
			}
		})
	}
}

func TestTransitionPermissionsKeepHumanReviewSeparated(t *testing.T) {
	if got := transitionPermission(deliveryworkflow.ActionApproveQA); got != deliveryQA {
		t.Fatalf("QA approval permission = %q, want %q", got, deliveryQA)
	}
	if got := transitionPermission(deliveryworkflow.ActionApproveRelease); got != deliveryRelease {
		t.Fatalf("release permission = %q, want %q", got, deliveryRelease)
	}
	if got := transitionPermission(deliveryworkflow.ActionSubmitCodeReview); got != deliveryManage {
		t.Fatalf("code submission permission = %q, want %q", got, deliveryManage)
	}
}

func TestHumanGateActionsRequireReviewEvidence(t *testing.T) {
	for _, action := range []deliveryworkflow.Action{
		deliveryworkflow.ActionApprovePlan,
		deliveryworkflow.ActionRequestPlanChanges,
		deliveryworkflow.ActionApproveCodeReview,
		deliveryworkflow.ActionRequestCodeChanges,
		deliveryworkflow.ActionApproveQA,
		deliveryworkflow.ActionRequestQAChanges,
		deliveryworkflow.ActionApproveRelease,
	} {
		if !isHumanGateAction(action) {
			t.Fatalf("%s must require a human gate evidence checklist", action)
		}
	}
	for _, action := range []deliveryworkflow.Action{
		deliveryworkflow.ActionSubmitPlan,
		deliveryworkflow.ActionSubmitCodeReview,
		deliveryworkflow.ActionPreviewReady,
		deliveryworkflow.ActionSubmitQA,
	} {
		if isHumanGateAction(action) {
			t.Fatalf("%s is a human submission, not a decision gate", action)
		}
	}
}

func TestValidateHumanGateInputRequiresReasonAndChecklist(t *testing.T) {
	if _, err := validateHumanGateInput(deliveryworkflow.ActionApprovePlan, "", []string{"plan reviewed"}); err == nil {
		t.Fatal("expected a human gate without a reason to be rejected")
	}
	if _, err := validateHumanGateInput(deliveryworkflow.ActionApprovePlan, "Reviewed scope", nil); err == nil {
		t.Fatal("expected a human gate without evidence to be rejected")
	}
	items, err := validateHumanGateInput(deliveryworkflow.ActionApprovePlan, "Reviewed scope", []string{" PR reviewed ", "", "CI passed"})
	if err != nil || len(items) != 2 || items[0] != "PR reviewed" || items[1] != "CI passed" {
		t.Fatalf("unexpected normalized gate input: %#v, %v", items, err)
	}
	if _, err := validateHumanGateInput(deliveryworkflow.ActionSubmitPlan, "", nil); err != nil {
		t.Fatalf("human submission should not require a decision gate: %v", err)
	}
}

func TestRenderDeliveryReportKeepsHumanDeliveryEvidence(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	report := renderDeliveryReport(models.DeliveryWorkItem{
		Title:           "Refinar módulo Delivery",
		ExpectedOutcome: "Flujo visible y revisable.",
		Project:         models.DeliveryProject{Name: "ITBEM Platform"},
		ChangeSets:      []models.DeliveryChangeSet{{RepositoryRef: "workspace://dashboard", Branch: "itbem-agent/123", CIStatus: "passed", PullRequestURL: "https://example.test/pr/1", PreviewURL: "https://preview.example.test"}},
		Evidence:        []models.DeliveryEvidence{{Title: "QA visual", Phase: "qa", Reference: "s3://private/evidence.png"}},
		Gates:           []models.DeliveryGate{{Kind: "qa_review", Decision: "approved", DecidedAt: now, Comment: "Checklist completo"}},
	}, models.DeliveryRelease{Status: "released", ExecutiveJSON: `{"what_changed":"Delivery report","how_to_test":"Open the workflow"}`, TechnicalJSON: `{"decisions":["Human gates"]}`, ReleasedAt: &now})

	for _, expected := range []string{"# Entrega: Refinar módulo Delivery", "## Resumen ejecutivo", "https://example.test/pr/1", "https://preview.example.test", "QA visual", "Checklist completo"} {
		if !strings.Contains(report, expected) {
			t.Fatalf("report does not contain %q:\n%s", expected, report)
		}
	}
}
