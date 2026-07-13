package invitations

import (
	"errors"
	"net/http"

	"events-stocks/internal/authz"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// POST /api/invitations/:id/resend
func ResendInvitation(c echo.Context) error {
	idStr := c.Param("id")
	invitationID, err := uuid.FromString(idStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid invitation ID", err.Error())
	}
	if _, _, authErr := authz.RequireInvitationCapability(c, invitationID, authz.CapabilityGuestManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	if err := invitationSvc.ResendInvitation(invitationID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Invitation not found", err.Error())
		}
		return utils.Error(c, http.StatusInternalServerError, "Error logging resend", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Invitation marked as resent", nil)
}
