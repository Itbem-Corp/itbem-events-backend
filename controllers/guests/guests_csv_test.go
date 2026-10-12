package guests

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strings"
	"testing"

	"events-stocks/models"
	guestsService "events-stocks/services/guests"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCSVSafeCell_NeutralizesFormulaTriggers(t *testing.T) {
	cases := map[string]string{
		`=HYPERLINK("http://x","y")`: `'=HYPERLINK("http://x","y")`,
		"@SUM(A1)":                   "'@SUM(A1)",
		"-2+3":                       "'-2+3",
		"+52 55 1234 5678":           "'+52 55 1234 5678",
		"\t=1":                       "'\t=1",
		"\r=1":                       "'\r=1",
		" =1+1":                      "' =1+1",
		"   @cmd":                    "'   @cmd",
	}
	for input, want := range cases {
		assert.Equal(t, want, csvSafeCell(input), "input %q", input)
	}
}

func TestCSVSafeCell_LeavesOrdinaryTextUnchanged(t *testing.T) {
	for _, input := range []string{"", "   ", "Ana, María", "Ñandú", "＝1", "ana@example.com", "Mesa 4", "2026-07-12T00:30:00Z", "3", "O'Brien"} {
		assert.Equal(t, input, csvSafeCell(input), "input %q", input)
	}
}

func TestWriteGuestCSVRow_RoundTripsThroughCSVReader(t *testing.T) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	writeGuestCSVRow(writer, "Ana, María", `=HYPERLINK("http://x","y")`, "+52 555", `say "hi"`, "Ñandú")
	writer.Flush()
	require.NoError(t, writer.Error())

	record, err := csv.NewReader(strings.NewReader(buf.String())).Read()
	require.NoError(t, err)
	assert.Equal(t, []string{"Ana, María", `'=HYPERLINK("http://x","y")`, "'+52 555", `say "hi"`, "Ñandú"}, record)
}

func exportGuestCSVForTest(t *testing.T, guest models.Guest, query string) [][]string {
	t.Helper()
	repo := &checkinSummaryGuestRepo{guest: guest}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	t.Cleanup(func() { guestSvc = orig })

	c, rec := newEchoCtx(http.MethodGet, "/events/"+guest.EventID.String()+"/guests/export"+query, "")
	c.SetParamNames("id")
	c.SetParamValues(guest.EventID.String())
	setAuthzContext(t, c, guest.EventID, nil)

	require.NoError(t, ExportGuestsCSV(c))
	require.Equal(t, http.StatusOK, rec.Code)
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(rec.Body.String(), "\xEF\xBB\xBF"))).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2)
	return records
}

func hostileExportGuest() models.Guest {
	eventID := uuid.Must(uuid.NewV4())
	return models.Guest{
		ID: uuid.Must(uuid.NewV4()), EventID: eventID,
		FirstName: `=HYPERLINK("http://x","y")`, LastName: "@SUM(A1)",
		Email: "-2+3@example.com", Phone: "+52 555",
		RSVPStatus: "=1+1", RSVPMethod: "@web", TableNumber: "-1",
		DietaryRestrictions: "\t=1", RSVPNotes: "\r=1", Notes: " =1+1",
		GuestsCount: 1,
	}
}

func TestExportGuestsCSV_DefaultViewNeutralizesFormulaCells(t *testing.T) {
	records := exportGuestCSVForTest(t, hostileExportGuest(), "")
	assert.Equal(t, "Nombre", records[0][0])
	assert.Equal(t, []string{
		`'=HYPERLINK("http://x","y")`, "'@SUM(A1)", "'-2+3@example.com", "'+52 555", "Mesa -1", "1", "0",
		"'=1+1", "'\t=1", "'\r=1", "' =1+1",
	}, records[1])
}

func TestExportGuestsCSV_InvitationViewNeutralizesFormulaCells(t *testing.T) {
	records := exportGuestCSVForTest(t, hostileExportGuest(), "?view=invitations")
	assert.Equal(t, "Estado RSVP", records[0][3])
	assert.Equal(t, `'=HYPERLINK("http://x","y") @SUM(A1)`, records[1][0])
	assert.Equal(t, "'-2+3@example.com", records[1][1])
	assert.Equal(t, "'+52 555", records[1][2])
	assert.Equal(t, "'=1+1", records[1][3])
	assert.Equal(t, "'@web", records[1][5])
}

func TestExportGuestsCSV_RSVPViewNeutralizesFormulaCells(t *testing.T) {
	records := exportGuestCSVForTest(t, hostileExportGuest(), "?view=rsvp")
	assert.Equal(t, "+1s", records[0][5], "constant headers are written unchanged")
	assert.Equal(t, `'=HYPERLINK("http://x","y") @SUM(A1)`, records[1][0])
	assert.Equal(t, "'-2+3@example.com", records[1][1])
	assert.Equal(t, "'+52 555", records[1][2])
	assert.Equal(t, "'=1+1", records[1][3])
	assert.Equal(t, "'@web", records[1][4])
}
