package clientroles

import (
	"events-stocks/models"
	"events-stocks/repositories/clientrolerepository"
	"fmt"
)

func GetAllowedRolesToAssign(myRoleCode string) ([]models.ClientRole, error) {

	// 1. Averiguar quién soy y qué nivel tengo
	myRole, err := clientrolerepository.GetByCode(myRoleCode)
	if err != nil {
		return nil, fmt.Errorf("role not found: %s", myRoleCode)
	}

	// Regla especial (Opcional): Si soy 'Member' o 'Guest', no puedo invitar a nadie.
	// Podrías controlar esto por jerarquía (ej: > 2 no invita) o por código.
	if myRole.Hierarchy > 2 {
		return []models.ClientRole{}, nil
	}

	// 2. La DB hace el filtro
	return clientrolerepository.GetAssignableRoles(myRole.Hierarchy)
}
