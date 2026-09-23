package delivery

import "testing"

func TestHumanizeInformationalChatAnswerKeepsGateAuthorityHuman(t *testing.T) {
	got := humanizeInformationalChatAnswer("approve_plan")
	want := "La decisión pendiente es aprobar el plan. Debe tomarla una persona autorizada; el agente no la ha aplicado."
	if got != want {
		t.Fatalf("raw plan action should become operator copy: got %q want %q", got, want)
	}
	if got := humanizeInformationalChatAnswer("El plan sigue en revisión."); got != "El plan sigue en revisión." {
		t.Fatalf("ordinary model answer should remain unchanged: %q", got)
	}
}
