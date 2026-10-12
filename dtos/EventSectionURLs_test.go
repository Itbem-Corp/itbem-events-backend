package dtos

import (
	"encoding/json"
	"errors"
	"testing"

	"events-stocks/models"
)

// STU-N4: link fields of a section config only accept safe schemes.
func TestValidateEventSectionConfigURLs(t *testing.T) {
	rejected := []string{
		`{"mapUrl":"javascript:alert(document.domain)"}`,
		`{"mapUrl":"  JaVaScRiPt:alert(1)"}`,
		`{"mapUrl":"java\tscript:alert(1)"}`,
		`{"items":[{"buttonLink":"data:text/html,<script>alert(1)</script>"}]}`,
		`{"venue":{"href":"vbscript:msgbox(1)"}}`,
		// Plural and other link keys (security review 2026-10-12 #4).
		`{"urls":["https://ok.mx","javascript:alert(1)"]}`,
		`{"links":["javascript:alert(1)"]}`,
		`{"imageUrls":["data:text/html,x"]}`,
		`{"socialLinks":{"instagram":"javascript:alert(1)"}}`,
		`{"links":[{"label":"Mapa","target":"javascript:alert(1)"}]}`,
		`{"src":"javascript:alert(1)"}`,
		`{"imgSrc":"data:text/html,x"}`,
		`{"website":"data:text/html,x"}`,
		`{"hrefs":["vbscript:x"]}`,
		`{"gallery":{"items":[{"photoSrc":"file:///etc/passwd"}]}}`,
	}
	for _, config := range rejected {
		err := ValidateEventSectionConfigURLs(config)
		var urlErr InvalidSectionURLError
		if !errors.As(err, &urlErr) {
			t.Fatalf("%s: expected InvalidSectionURLError, got %v", config, err)
		}
	}
	accepted := []string{
		`{"mapUrl":"https://maps.google.com/maps?q=14.89,-92.25&output=embed"}`,
		`{"mapUrl":"http://example.com/a:b"}`,
		`{"imageUrl":"events/abc/cover.jpg"}`,
		`{"rsvpUrl":"/e/mi-boda#rsvp"}`,
		`{"contactLink":"tel:+525512345678","mailLink":"mailto:a@b.mx","mapsLink":"geo:19.4,-99.1"}`,
		`{"mapUrl":""}`,
		`{"title":"javascript: no es un enlace"}`,
		`{"timeUrl":"10:30"}`,
		`{"links":["https://a.mx","/e/boda","mailto:a@b.mx"]}`,
		`{"socialLinks":{"instagram":"https://instagram.com/x","label":"Síguenos: aquí"}}`,
		`{"links":[{"label":"Nota: llegar temprano","url":"geo:19.4,-99.1"}]}`,
		`{"website":"https://boda.mx","src":"events/abc/foto.jpg"}`,
		`{"title":"Data: lo que sigue"}`,
		`{}`,
	}
	for _, config := range accepted {
		if err := ValidateEventSectionConfigURLs(config); err != nil {
			t.Fatalf("%s: unexpected error %v", config, err)
		}
	}
}

func TestApplyEventSectionPayloadRejectsJavascriptMapURL(t *testing.T) {
	section := models.EventSection{Config: `{"mapUrl":"javascript:alert(1)"}`}
	// Stored data is left alone when the payload has no config.
	if err := ApplyEventSectionPayload(&section, EventSectionPayload{Title: "Ubicación"}, false); err != nil {
		t.Fatalf("payload without config must not validate stored data: %v", err)
	}
	payload := EventSectionPayload{Config: json.RawMessage(`{"mapUrl":"javascript:alert(1)"}`)}
	err := ApplyEventSectionPayload(&section, payload, false)
	var urlErr InvalidSectionURLError
	if !errors.As(err, &urlErr) || urlErr.Field != "mapUrl" {
		t.Fatalf("expected mapUrl rejection, got %v", err)
	}
}
