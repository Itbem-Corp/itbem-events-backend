package clienttypes

import (
	"fmt"

	"events-stocks/models"
	"events-stocks/services/ports"
)

var _clientTypeSvc *ClientTypeService

func SetDefaultClientTypeService(svc *ClientTypeService) { _clientTypeSvc = svc }

func clientTypeServiceUnavailable() error {
	return fmt.Errorf("client type service not initialized")
}

func GetAllowedClientTypes(parentTypeCode string) ([]models.ClientType, error) {
	if _clientTypeSvc == nil {
		return nil, clientTypeServiceUnavailable()
	}
	return _clientTypeSvc.GetAllowedClientTypes(parentTypeCode)
}

type ClientTypeService struct {
	repo ports.ClientTypeRepository
}

func NewClientTypeService(repo ports.ClientTypeRepository) *ClientTypeService {
	return &ClientTypeService{repo: repo}
}

func (s *ClientTypeService) GetAllowedClientTypes(parentTypeCode string) ([]models.ClientType, error) {
	if parentTypeCode == "" {
		return s.repo.GetRootType()
	}

	parentType, err := s.repo.GetByCode(parentTypeCode)
	if err != nil {
		return nil, fmt.Errorf("unknown parent type code: %s", parentTypeCode)
	}

	return s.repo.GetChildTypes(parentType.Level)
}
