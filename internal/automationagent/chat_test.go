package automationagent

import "testing"

func TestParseDeliveryChatRequiresInformationalBoundedShape(t *testing.T) {
	valid := `{"answer":"La ejecución sigue en validación.","next_steps":["Revisar el resultado"],"questions":[]}`
	parsed, err := ParseDeliveryChat(valid)
	if err != nil || parsed["answer"] != "La ejecución sigue en validación." {
		t.Fatalf("valid chat response rejected: %#v / %v", parsed, err)
	}
	for _, invalid := range []string{
		`{"answer":"","next_steps":[],"questions":[]}`,
		`not json`,
	} {
		if _, err := ParseDeliveryChat(invalid); err == nil {
			t.Fatalf("invalid chat response accepted: %s", invalid)
		}
	}
}

func TestParseDeliveryChatDropsMalformedOptionalSuggestionsWithoutDroppingAnswer(t *testing.T) {
	parsed, err := ParseDeliveryChat(`{"answer":"El gate requiere revisión humana.","next_steps":"no es una lista","questions":[{"text":"¿aprobar?"}]}`)
	if err != nil {
		t.Fatalf("a valid answer with malformed optional suggestions should be repaired, not discarded: %v", err)
	}
	if parsed["answer"] != "El gate requiere revisión humana." {
		t.Fatalf("answer was lost during repair: %#v", parsed)
	}
	for _, field := range []string{"next_steps", "questions"} {
		items, ok := parsed[field].([]any)
		if !ok || len(items) != 0 {
			t.Fatalf("%s must be dropped rather than coerced: %#v", field, parsed[field])
		}
	}
	repairs, ok := parsed["_harness_repairs"].([]any)
	if !ok || len(repairs) != 2 {
		t.Fatalf("repair must remain observable in the result: %#v", parsed)
	}
}
