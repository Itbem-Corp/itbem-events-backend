package seeds

import (
	"log/slog"

	"gorm.io/gorm"
)

// sectionSDUI maps a known EventSection UUID to its SDUI component_type and starter config.
// These UUIDs are the stable section IDs documented in the frontend's docs/api.md.
//
// Run on every startup (idempotent — skips sections that already have component_type set).
// Config values marked with TODO should be updated via the admin panel once available.
type sectionSDUI struct {
	ID            string
	ComponentType string
	Config        string
}

var knownSections = []sectionSDUI{
	// ── Andres & Ivanna Wedding ───────────────────────────────────────────
	{
		ID:            "8c1600fd-f6d3-494c-9542-2dc4a0897954",
		ComponentType: "RSVPConfirmation",
		Config:        `{}`,
	},

	// ── Izapa Graduation 2022-2025 ────────────────────────────────────────
	{
		ID:            "76a8d7d9-d83f-472b-9fcb-a75e96b6bcc5",
		ComponentType: "GraduationHero",
		Config:        `{"title":"NOS GRADUAMOS","years":"2022 - 2025","school":"PREPARATORIA"}`,
	},
	{
		ID:            "78acb1bb-bbc8-44de-afc9-a79eb22de2db",
		ComponentType: "EventVenue",
		// TODO: replace mapUrl with the actual Google Maps embed URL from the admin panel
		Config: `{"text":"Los invitamos a acompañarnos en la Santa Misa de Acción de Gracias","date":"este 22 de junio del 2025","venueText":"Santuario la Villita de Guadalupe\na las 5:00 pm","mapUrl":"https://maps.google.com/maps?q=14.897417,-92.253839&hl=es&z=16&output=embed"}`,
	},
	{
		ID:            "dc87ac12-7ca1-4aca-9e07-02b687c4ecb1",
		ComponentType: "Reception",
		// TODO: replace mapUrl with the actual Google Maps embed URL from the admin panel
		Config: `{"venueText":"posteriormente la recepción será en\nHotel Holiday Inn — Salón Barista","mapUrl":"https://maps.google.com/maps?q=14.874759,-92.286864&hl=es&z=16&output=embed"}`,
	},
	{
		ID:            "af03cf82-72d3-4d8c-8838-4cfcc6bf287b",
		ComponentType: "GraduatesList",
		// Names are fetched from DB via GET /api/events/section/:sectionId/attendees
		Config: `{"closing":"celebremos juntos"}`,
	},
	{
		ID:            "61202ab3-adaf-405f-8ff4-7bc75d1afc52",
		ComponentType: "PhotoGrid",
		Config:        `{}`,
	},
}

// SeedEventSectionSDUI sets the component_type and starter config on known EventSection records.
// Only updates sections where component_type is still empty (safe to re-run).
func SeedEventSectionSDUI(db *gorm.DB) {
	for _, s := range knownSections {
		result := db.Exec(
			`UPDATE event_sections SET component_type = ?, config = ? WHERE id = ? AND (component_type IS NULL OR component_type = '')`,
			s.ComponentType, s.Config, s.ID,
		)
		if result.Error != nil {
			slog.Warn("SeedEventSectionSDUI: failed to update section", "id", s.ID, "error", result.Error)
		} else if result.RowsAffected > 0 {
			slog.Info("SeedEventSectionSDUI: section updated", "id", s.ID[:8], "component_type", s.ComponentType)
		}
	}
}
