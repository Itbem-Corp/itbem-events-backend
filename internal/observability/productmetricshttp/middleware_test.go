package productmetricshttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/middleware/applicationaccess"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func TestOrganizationIDPrefersExplicitScope(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/events?client_id="+uuid.Must(uuid.NewV4()).String(), nil), httptest.NewRecorder())
	expected := uuid.Must(uuid.NewV4())
	ScopeOrganization(c, expected)
	assert.Equal(t, expected, organizationID(c))
}
func TestOrganizationIDReadsApplicationContext(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/events", nil), httptest.NewRecorder())
	expected := uuid.Must(uuid.NewV4())
	c.Set(applicationaccess.ContextOrganizationID, expected)
	assert.Equal(t, expected, organizationID(c))
}
