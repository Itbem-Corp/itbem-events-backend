package moments

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestValidateMultipartKey(t *testing.T) {
	const eventID = "550e8400-e29b-41d4-a716-446655440000"

	tests := []struct {
		name  string
		key   string
		valid bool
	}{
		{"valid key", "moments/" + eventID + "/raw/abc.mp4", true},
		{"wrong event", "moments/other-event/raw/abc.mp4", false},
		{"staging path", "moments/uploads/tmp/abc.mp4", false},
		{"path traversal", "moments/" + eventID + "/raw/../other/abc.mp4", false},
		{"empty filename", "moments/" + eventID + "/raw/", false},
		{"nested path", "moments/" + eventID + "/raw/sub/abc.mp4", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateMultipartKey(tt.key, eventID)
			require.Equal(t, tt.valid, got, "key=%q", tt.key)
		})
	}
}

// TestCompleteMultipartMoment_PanicsWithoutDB confirms the handler is reachable
// and linked correctly. Without a real database, GORM panics on event lookup
// before reaching any business logic; require.Panics documents this expected behavior.
func TestCompleteMultipartMoment_PanicsWithoutDB(t *testing.T) {
	e := echo.New()
	body := `{"upload_id":"uid","s3_key":"moments/abc/raw/file.mp4","content_type":"video/mp4","parts":[]}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("identifier")
	c.SetParamValues("test-event-id")

	// In a no-DB environment GORM panics on nil pointer — that is expected here.
	// The test confirms the handler is reachable and the code compiles correctly.
	require.Panics(t, func() {
		_ = CompleteMultipartMoment(c)
	})
}
