package models

import (
	"errors"
	"testing"
)

func TestDeliveryAutomationScheduleEventsAreAppendOnly(t *testing.T) {
	event := DeliveryAutomationScheduleEvent{}
	if err := event.BeforeUpdate(nil); !errors.Is(err, ErrDeliveryAutomationScheduleEventImmutable) {
		t.Fatalf("update guard = %v, want append-only sentinel", err)
	}
	if err := event.BeforeDelete(nil); !errors.Is(err, ErrDeliveryAutomationScheduleEventImmutable) {
		t.Fatalf("delete guard = %v, want append-only sentinel", err)
	}
}

func TestDeliveryAutomationScheduleOccurrencesAreAppendOnly(t *testing.T) {
	occurrence := DeliveryAutomationScheduleOccurrence{}
	if err := occurrence.BeforeUpdate(nil); !errors.Is(err, ErrDeliveryAutomationScheduleOccurrenceImmutable) {
		t.Fatalf("update guard = %v, want append-only sentinel", err)
	}
	if err := occurrence.BeforeDelete(nil); !errors.Is(err, ErrDeliveryAutomationScheduleOccurrenceImmutable) {
		t.Fatalf("delete guard = %v, want append-only sentinel", err)
	}
}
