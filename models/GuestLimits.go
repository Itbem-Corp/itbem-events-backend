package models

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Length limits for the free-text guest columns, counted in characters
// (runes), which is how Postgres measures varchar(n). The two varchar columns
// (dietary_restrictions, table_number) must match their column size so a long
// value is refused with a 400 instead of failing the write with SQLSTATE
// 22001; the text columns get generous bounds so no request can store an
// arbitrarily large string. Every write path (public RSVP, guest create,
// batch create and update) checks them before touching the database.
const (
	GuestNameMaxChars                = 200
	GuestNicknameMaxChars            = 120
	GuestEmailMaxChars               = 254
	GuestPhoneMaxChars               = 50
	GuestRoleMaxChars                = 120
	GuestHeadlineMaxChars            = 300
	GuestSignatureMaxChars           = 300
	GuestBioMaxChars                 = 4000
	GuestImageURLMaxChars            = 2048
	GuestTableNumberMaxChars         = 60  // varchar(60)
	GuestDietaryRestrictionsMaxChars = 255 // varchar(255)
	GuestRSVPNotesMaxChars           = 1000
	GuestNotesMaxChars               = 2000
	GuestRSVPMethodMaxChars          = 20
)

// GuestFieldTooLongError names the field that is over its limit. Message is a
// Spanish sentence safe to show to a guest or an organizer.
type GuestFieldTooLongError struct {
	Field string
	Label string
	Max   int
}

func (e *GuestFieldTooLongError) Error() string {
	return fmt.Sprintf("%s exceeds %d characters", e.Field, e.Max)
}

// Message is the Spanish text for the response.
func (e *GuestFieldTooLongError) Message() string {
	return fmt.Sprintf("El campo «%s» admite máximo %d caracteres.", e.Label, e.Max)
}

type guestTextLimit struct {
	field string
	label string
	max   int
	value func(*Guest) string
}

var guestTextLimits = []guestTextLimit{
	{"first_name", "Nombre", GuestNameMaxChars, func(g *Guest) string { return g.FirstName }},
	{"last_name", "Apellido", GuestNameMaxChars, func(g *Guest) string { return g.LastName }},
	{"nickname", "Apodo", GuestNicknameMaxChars, func(g *Guest) string { return g.Nickname }},
	{"email", "Correo", GuestEmailMaxChars, func(g *Guest) string { return g.Email }},
	{"phone", "Teléfono", GuestPhoneMaxChars, func(g *Guest) string { return g.Phone }},
	{"role", "Rol", GuestRoleMaxChars, func(g *Guest) string { return g.Role }},
	{"headline", "Encabezado", GuestHeadlineMaxChars, func(g *Guest) string { return g.Headline }},
	{"signature", "Firma", GuestSignatureMaxChars, func(g *Guest) string { return g.Signature }},
	{"bio", "Biografía", GuestBioMaxChars, func(g *Guest) string { return g.Bio }},
	{"image_url", "Imagen", GuestImageURLMaxChars, func(g *Guest) string { return g.ImageURL }},
	{"image1_url", "Imagen 1", GuestImageURLMaxChars, func(g *Guest) string { return g.Image1URL }},
	{"image2_url", "Imagen 2", GuestImageURLMaxChars, func(g *Guest) string { return g.Image2URL }},
	{"image3_url", "Imagen 3", GuestImageURLMaxChars, func(g *Guest) string { return g.Image3URL }},
	{"table_number", "Número de mesa", GuestTableNumberMaxChars, func(g *Guest) string { return g.TableNumber }},
	{"dietary_restrictions", "Restricciones alimentarias", GuestDietaryRestrictionsMaxChars, func(g *Guest) string { return g.DietaryRestrictions }},
	{"rsvp_notes", "Notas de confirmación", GuestRSVPNotesMaxChars, func(g *Guest) string { return g.RSVPNotes }},
	{"notes", "Notas", GuestNotesMaxChars, func(g *Guest) string { return g.Notes }},
	{"rsvp_method", "Método de confirmación", GuestRSVPMethodMaxChars, func(g *Guest) string { return g.RSVPMethod }},
}

// CheckGuestTextLengths returns a *GuestFieldTooLongError for the first
// free-text field of after that is over its limit. With before set, only the
// fields the write changed are checked, so a legacy row that already holds a
// long text column can still have its other fields edited.
func CheckGuestTextLengths(before, after *Guest) error {
	if after == nil {
		return nil
	}
	for _, limit := range guestTextLimits {
		value := limit.value(after)
		if before != nil && limit.value(before) == value {
			continue
		}
		if err := CheckTextLength(limit.field, limit.label, value, limit.max); err != nil {
			return err
		}
	}
	return nil
}

// CheckTextLength reports a *GuestFieldTooLongError when the trimmed value has
// more than max characters.
func CheckTextLength(field, label, value string, max int) error {
	if utf8.RuneCountInString(strings.TrimSpace(value)) > max {
		return &GuestFieldTooLongError{Field: field, Label: label, Max: max}
	}
	return nil
}
