package delivery

import (
	"encoding/json"
	"testing"
)

func TestClassifyDeliveryMessageSeparatesQuestionsFromEffects(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		continuing bool
		intent     string
		effect     string
		gate       bool
	}{
		{name: "question", body: "¿Cómo va la ejecución?", intent: "question", effect: "informational"},
		{name: "hypothetical", body: "¿Qué pasa si publicamos ahora?", intent: "question", effect: "informational"},
		{name: "polite question sensitive", body: "¿Puedes publicar ahora?", intent: "question", effect: "informational"},
		{name: "polite question implementation", body: "¿Podrías arreglar el foco?", intent: "question", effect: "informational"},
		{name: "context", body: "El CTA debe conservar el texto aprobado.", intent: "context", effect: "stored_context"},
		{name: "instruction", body: "Arregla el foco del modal en el siguiente paso.", intent: "action_request", effect: "stored_instruction"},
		{name: "polite instruction sensitive", body: "Puedes publicar ahora", intent: "action_request", effect: "stored_instruction", gate: true},
		{name: "polite instruction implementation", body: "Podrías arreglar el foco", intent: "action_request", effect: "stored_instruction"},
		{name: "continuation", body: "Ya revisé el bloqueo", continuing: true, intent: "action_request", effect: "continuation_queued"},
		{name: "sensitive", body: "Publica cuando termines", intent: "action_request", effect: "stored_instruction", gate: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := classifyDeliveryMessage(test.body, test.continuing)
			if actual.Intent != test.intent || actual.Effect != test.effect || actual.RequiresHumanGate != test.gate {
				t.Fatalf("classification = %#v, want intent=%q effect=%q gate=%v", actual, test.intent, test.effect, test.gate)
			}
			var receipt map[string]any
			if err := json.Unmarshal([]byte(deliveryMessageReceipt(actual)), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt["status"] != "received" || receipt["classification"] != test.intent {
				t.Fatalf("receipt = %#v", receipt)
			}
		})
	}
}
