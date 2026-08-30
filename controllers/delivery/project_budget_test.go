package delivery

import (
	"events-stocks/models"
	"testing"
	"time"
)

func TestMonthStartUTCIsStableAcrossTimezones(t *testing.T) {
	location := time.FixedZone("UTC-6", -6*60*60)
	value := time.Date(2026, time.August, 1, 1, 30, 0, 0, location)
	got := monthStartUTC(value)
	want := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("month start = %s, want %s", got, want)
	}
}

func TestBudgetAdmissionAccountsForActiveReservations(t *testing.T) {
	if !budgetAdmissionAllowed(1_000, 300, 200, 500) {
		t.Fatal("a run exactly within the project allocation must be admitted")
	}
	if budgetAdmissionAllowed(1_000, 300, 200, 501) {
		t.Fatal("active holds must prevent concurrent overspend")
	}
	if budgetAdmissionAllowed(0, 0, 0, 0) {
		t.Fatal("unmetered projects do not use the enforced admission helper")
	}
}

func TestTaskBudgetAdmissionIsAnIndependentHardCeiling(t *testing.T) {
	const taskBudget = int64(1_000)
	if !budgetAdmissionAllowed(taskBudget, 450, 250, 300) {
		t.Fatal("a task run within its own allocation must be admitted")
	}
	if budgetAdmissionAllowed(taskBudget, 450, 250, 301) {
		t.Fatal("a task cap must reject a run that would exceed its allocation even when the project has budget left")
	}
}

func TestDeliveryQABudgetReservationIncludesStagehandSemanticCall(t *testing.T) {
	cfg := &models.Config{AutomationPricingJSON: `{
        "version":"test-v1",
        "basis":"test",
        "models":{"minimax:minimax-m3":{"input_microusd_per_million":1000000,"output_microusd_per_million":1000000}}
      }`}
	primary, err := deliveryRunBudgetReservation(cfg, "delivery.plan", 100, 200)
	if err != nil {
		t.Fatalf("primary reservation: %v", err)
	}
	qa, err := deliveryRunBudgetReservation(cfg, "delivery.qa", 100, 200)
	if err != nil {
		t.Fatalf("QA reservation: %v", err)
	}
	wantAdditional := int64(defaultQASemanticInputTokenReserve + defaultQASemanticOutputTokenReserve)
	if qa != primary+wantAdditional {
		t.Fatalf("QA reservation = %d, want primary %d plus Stagehand reserve %d", qa, primary, wantAdditional)
	}
}

func TestDeliveryQABudgetReservationHonorsConfiguredStagehandBounds(t *testing.T) {
	cfg := &models.Config{
		AutomationPricingJSON: `{
          "version":"test-v1",
          "basis":"test",
          "models":{"minimax:minimax-m3":{"input_microusd_per_million":1000000,"output_microusd_per_million":1000000}}
        }`,
		AutomationQASemanticInputTokenReserve:  800,
		AutomationQASemanticOutputTokenReserve: 200,
	}
	primary, err := deliveryRunBudgetReservation(cfg, "delivery.plan", 100, 200)
	if err != nil {
		t.Fatalf("primary reservation: %v", err)
	}
	qa, err := deliveryRunBudgetReservation(cfg, "delivery.qa", 100, 200)
	if err != nil {
		t.Fatalf("QA reservation: %v", err)
	}
	if qa != primary+1_000 {
		t.Fatalf("QA reservation = %d, want configured Stagehand reserve added to %d", qa, primary)
	}
}
