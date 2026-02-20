package clientroles

import (
	"events-stocks/repositories/clientrepository" // 👈 Necesario para verificar tu rol actual
	"events-stocks/services/clientroles"
	"events-stocks/services/users"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
)

// ListClientRoles devuelve los roles que el usuario puede asignar.
// GET /catalogs/roles?client_id=UUID
func ListClientRoles(c echo.Context) error {

	// 1. Identificar al Usuario
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	user, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	// 2. Identificar el Contexto (La Empresa)
	clientIDStr := c.QueryParam("client_id")

	// Si no mandan client_id, no podemos calcular la jerarquía.
	// (A menos que sea un SuperAdmin global, pero asumamos flujo normal).
	if clientIDStr == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing client_id", "You must provide client_id to filter allowed roles")
	}

	clientID, err := uuid.FromString(clientIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	// 3. Averiguar MI Rol en esa empresa
	// IMPORTANTE: clientrepository.IsMember debe devolver (bool, roleCode)
	isMember, myRoleCode := clientrepository.IsMember(user.ID, clientID)

	if !isMember {
		return utils.Error(c, http.StatusForbidden, "Access Denied", "You are not a member of this organization")
	}

	// 4. Llamar al Servicio Inteligente (Filtrado por Jerarquía)
	roles, err := clientroles.GetAllowedRolesToAssign(myRoleCode)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching roles", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Allowed roles", roles)
}
