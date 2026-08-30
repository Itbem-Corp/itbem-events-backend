package delivery

import "testing"

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
