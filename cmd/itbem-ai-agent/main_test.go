package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDoctorReportFailsClosedWithoutExecutionDependenciesAndNeverLeaksValues(t *testing.T) {
	report, ready, err := doctorReport(func(key string) string {
		if key == "MINIMAX_API_KEY" || key == "AUTOMATION_CALLBACK_SECRET" {
			return "must-never-appear"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready || report["ready"] != false || report["workspaces_ready"] != false {
		t.Fatalf("missing workspace/runtime configuration must fail closed: %#v", report)
	}
	provider, ok := report["provider"].(map[string]any)
	if !ok || provider["ready"] != true {
		t.Fatalf("a configured provider should be reported without exposing its key: %#v", report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-never-appear") {
		t.Fatalf("doctor output leaked a configured secret: %s", raw)
	}
}

func TestDoctorReviewIngressNeverLeaksConfigurationAndReportsIncompleteSetup(t *testing.T) {
	status := doctorReviewIngress(func(key string) string {
		if key == "GITHUB_REVIEW_WEBHOOK_SECRET" {
			return "must-never-appear"
		}
		if key == "GITHUB_REVIEW_REPOSITORIES" {
			return "itbem/backend"
		}
		return ""
	}, false, true)
	if status["enabled"] != true || status["ready"] != false || status["status"] != "incomplete" || status["allowed_repository_count"] != 1 {
		t.Fatalf("incomplete ingress should be explicit: %#v", status)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "must-never-appear") || strings.Contains(string(raw), "itbem/backend") {
		t.Fatalf("doctor review ingress leaked secret or repository identity: %s", raw)
	}
}
