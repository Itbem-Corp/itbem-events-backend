package delivery

import (
	"events-stocks/models"
	"testing"
)

func TestAutomationClientAllowedUsesProductBoundary(t *testing.T) {
	tests := []struct {
		name string
		code string
		want bool
	}{
		{name: "ITBEM", code: "itbem", want: true},
		{name: "EventiApp protected product", code: "eventiapp", want: false},
		{name: "Cafetton House client product", code: "cafettonhouse", want: false},
		{name: "unknown product", code: "legacy-customer", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := automationClientAllowed(models.Client{Code: tt.code}); got != tt.want {
				t.Fatalf("automationClientAllowed(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}
