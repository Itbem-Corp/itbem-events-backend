package clienttypes

import (
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/services/clients"
	"events-stocks/services/clienttypes"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"strings"
)

// creatableTypes is the seam to the organization service; tests replace it.
var creatableTypes = func(requesterID uuid.UUID, parentID *uuid.UUID, primaryRoot bool) ([]string, error) {
	return clients.CreatableClientTypeCodes(requesterID, parentID, primaryRoot)
}

// ListClientTypes returns the organization types the caller may create.
//
//	GET /catalogs/client-types                    → types the caller can create anywhere
//	GET /catalogs/client-types?parent_id=<uuid>   → types allowed under that parent (Owner required)
//	GET /catalogs/client-types?parent_type_code=X → static catalog rule for a parent type
//
// Without parameters the answer depends on the caller: Root 1 gets every type,
// anybody else gets the types allowed under the organizations they own (an
// Agency owner gets "Customer", never "Platform").
func ListClientTypes(c echo.Context) error {
	if parentTypeCode := strings.ToUpper(strings.TrimSpace(c.QueryParam("parent_type_code"))); parentTypeCode != "" {
		types, err := clienttypes.GetAllowedClientTypes(parentTypeCode)
		if err != nil {
			if errors.Is(err, clienttypes.ErrUnknownParentType) {
				return utils.Error(c, http.StatusBadRequest, "El tipo de organización superior no existe", "CLIENT_TYPE_INVALID")
			}
			return utils.Error(c, http.StatusInternalServerError, "Error fetching client types", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Allowed client types", dtos.NewClientTypeResponses(types))
	}

	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	var parentID *uuid.UUID
	if raw := strings.TrimSpace(c.QueryParam("parent_id")); raw != "" && raw != "null" {
		parsed, parseErr := uuid.FromString(raw)
		if parseErr != nil {
			return utils.Error(c, http.StatusBadRequest, "parent_id debe ser un UUID válido", "INVALID_PARENT_ID")
		}
		parentID = &parsed
	}
	codes, err := creatableTypes(user.ID, parentID, user.IsPrimaryRoot())
	if err != nil {
		switch {
		case errors.Is(err, clients.ErrClientPermissionDenied):
			return utils.Error(c, http.StatusForbidden, "No tienes permiso para crear organizaciones dentro de la organización seleccionada", "CLIENT_PERMISSION_DENIED")
		case errors.Is(err, clients.ErrClientParentNotFound):
			return utils.Error(c, http.StatusNotFound, "La organización superior no existe", "CLIENT_PARENT_NOT_FOUND")
		default:
			return utils.Error(c, http.StatusInternalServerError, "Error fetching client types", "No se pudieron cargar los tipos de organización")
		}
	}
	types, err := clienttypes.TypesByCodes(codes)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching client types", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Allowed client types", dtos.NewClientTypeResponses(types))
}
