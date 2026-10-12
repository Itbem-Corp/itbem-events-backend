package resources

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// EVT-9: section resources of a deleted event answered 500 "Error loading
// event access"; the event is simply not readable (403, like inactive).
func TestRequirePublicResourceSectionAccess_DeletedEventIsNotA500(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	withFullResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{ID: sectionID, EventID: eventID}},
		&mockResourceConfigRepo{cfg: &models.EventConfig{IsPublic: true}},
		nil,
		nil,
		&mockResourceEventRepo{err: gorm.ErrRecordNotFound},
	)

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/resources/section/"+sectionID.String(), nil), rec)

	require.NoError(t, requirePublicResourceSectionAccess(c, sectionID))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
