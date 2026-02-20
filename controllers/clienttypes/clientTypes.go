package clienttypes

import (
	"events-stocks/services/clienttypes"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"net/http"
	"strings"
)

// ListClientTypes devuelve el catálogo permitido basado en el código enviado por el cliente.
// GET /catalogs/client-types?parent_type_code=AGENCY
func ListClientTypes(c echo.Context) error {

	// 1. Recibimos el CÓDIGO directamente del request (Sin ir a la BD)
	// Puede venir vacío (para creación Root) o con valores como "PLATFORM", "AGENCY".
	parentTypeCode := c.QueryParam("parent_type_code")

	// Normalizamos a mayúsculas por si el frontend lo manda en minúsculas
	parentTypeCode = strings.ToUpper(parentTypeCode)

	// 2. Llamamos al servicio (que es pura lógica, sin BD)
	types, err := clienttypes.GetAllowedClientTypes(parentTypeCode)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching client types", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Allowed client types", types)
}
