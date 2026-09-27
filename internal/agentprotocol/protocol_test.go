package agentprotocol

import "testing"

func TestSupportsRequiresRecognizedUniqueProtocol(t *testing.T) {
	for _, test := range []struct {
		name      string
		protocols []string
		want      bool
	}{
		{name: "required protocol", protocols: []string{ProtocolDeliveryPlanStepsV1}, want: true},
		{name: "whitespace is normalized", protocols: []string{" " + ProtocolDeliveryPlanStepsV1 + " "}, want: true},
		{name: "legacy empty heartbeat", protocols: []string{}, want: false},
		{name: "unknown protocol", protocols: []string{"delivery.plan_steps.v2"}, want: false},
		{name: "unknown alongside required protocol", protocols: []string{ProtocolDeliveryPlanStepsV1, "future.protocol"}, want: false},
		{name: "duplicate protocol", protocols: []string{ProtocolDeliveryPlanStepsV1, ProtocolDeliveryPlanStepsV1}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Supports(test.protocols, ProtocolDeliveryPlanStepsV1); got != test.want {
				t.Fatalf("Supports(%q) = %v, want %v", test.protocols, got, test.want)
			}
		})
	}
}
