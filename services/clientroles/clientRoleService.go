package clientroles

import (
	"fmt"

	"events-stocks/models"
	"events-stocks/services/ports"
)

var _clientRoleSvc *ClientRoleService

func SetDefaultClientRoleService(svc *ClientRoleService) { _clientRoleSvc = svc }

func GetAllowedRolesToAssign(myRoleCode string) ([]models.ClientRole, error) {
	return _clientRoleSvc.GetAllowedRolesToAssign(myRoleCode)
}

type ClientRoleService struct {
	repo ports.ClientRoleRepository
}

func NewClientRoleService(repo ports.ClientRoleRepository) *ClientRoleService {
	return &ClientRoleService{repo: repo}
}

func (s *ClientRoleService) GetAllowedRolesToAssign(myRoleCode string) ([]models.ClientRole, error) {
	myRole, err := s.repo.GetByCode(myRoleCode)
	if err != nil {
		return nil, fmt.Errorf("role not found: %s", myRoleCode)
	}

	if myRole.Hierarchy > 2 {
		return []models.ClientRole{}, nil
	}

	return s.repo.GetAssignableRoles(myRole.Hierarchy)
}
