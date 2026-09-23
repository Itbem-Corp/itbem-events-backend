package delivery

import (
	"testing"

	"github.com/gofrs/uuid"
)

func TestDeliveryProjectTitleUsesAConciseIntentPrefix(t *testing.T) {
	got := deliveryProjectTitle("Necesito que el equipo pueda revisar entregas desde móvil con evidencia clara. La IA no debe publicar nada.")
	if got != "Necesito que el equipo pueda revisar entregas desde móvil" {
		t.Fatalf("deliveryProjectTitle() = %q", got)
	}
}

func TestDeliveryProjectSlugIsSafeAndBounded(t *testing.T) {
	got := deliveryProjectSlug("  Delivery: revisión móvil & QA visual  ")
	if got != "delivery-revisi-n-m-vil-qa-visual" {
		t.Fatalf("deliveryProjectSlug() = %q", got)
	}
}

func TestDeliveryProjectSlugHasFallback(t *testing.T) {
	if got := deliveryProjectSlug("¡¿?!"); got != "delivery" {
		t.Fatalf("deliveryProjectSlug() = %q", got)
	}
}

func TestDeliveryProjectCreationFingerprintChangesWithPayload(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	first := deliveryProjectCreationFingerprint(clientID, "Portal", "", "Preparar entrega", "Preparar entrega")
	second := deliveryProjectCreationFingerprint(clientID, "Portal", "", "Preparar entrega", "Otro objetivo")
	if len(first) != 64 || first == second {
		t.Fatalf("idempotency fingerprint must be stable and payload-bound: first=%q second=%q", first, second)
	}
	if again := deliveryProjectCreationFingerprint(clientID, "Portal", "", "Preparar entrega", "Preparar entrega"); again != first {
		t.Fatalf("idempotency fingerprint is not deterministic: %q != %q", again, first)
	}
}
