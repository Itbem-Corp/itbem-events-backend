package productmetrics

import (
	"events-stocks/middleware/applicationaccess"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func TestOrganizationIDPrefersExplicitScope(t *testing.T) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/api/events?client_id="+uuid.Must(uuid.NewV4()).String(), nil)
	context := e.NewContext(request, httptest.NewRecorder())
	explicit := uuid.Must(uuid.NewV4())
	ScopeOrganization(context, explicit)

	assert.Equal(t, explicit, organizationID(context))
}

func TestOrganizationIDReadsValidatedApplicationContext(t *testing.T) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	context := e.NewContext(request, httptest.NewRecorder())
	expected := uuid.Must(uuid.NewV4())
	context.Set(applicationaccess.ContextOrganizationID, expected)

	assert.Equal(t, expected, organizationID(context))
}

func TestOrganizationIDPrefersExplicitQueryTargetOverWorkspaceContext(t *testing.T) {
	e := echo.New()
	target := uuid.Must(uuid.NewV4())
	workspace := uuid.Must(uuid.NewV4())
	request := httptest.NewRequest(http.MethodGet, "/api/events?client_id="+target.String(), nil)
	context := e.NewContext(request, httptest.NewRecorder())
	context.Set(applicationaccess.ContextOrganizationID, workspace)

	assert.Equal(t, target, organizationID(context))
}

func TestOrganizationIDReadsClientRoute(t *testing.T) {
	e := echo.New()
	expected := uuid.Must(uuid.NewV4())
	request := httptest.NewRequest(http.MethodGet, "/api/clients/"+expected.String(), nil)
	context := e.NewContext(request, httptest.NewRecorder())
	context.SetPath("/api/clients/:id")
	context.SetParamNames("id")
	context.SetParamValues(expected.String())

	assert.Equal(t, expected, organizationID(context))
}

func TestMutationClassification(t *testing.T) {
	assert.True(t, isMutation(http.MethodPost))
	assert.True(t, isMutation(http.MethodPut))
	assert.True(t, isMutation(http.MethodPatch))
	assert.True(t, isMutation(http.MethodDelete))
	assert.False(t, isMutation(http.MethodGet))
}
