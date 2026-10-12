package clienttypes

import (
	"errors"
	"fmt"

	"events-stocks/models"
	"events-stocks/services/ports"
)

var _clientTypeSvc *ClientTypeService

// ErrUnknownParentType is returned for a parent_type_code that is not in the
// catalog, so the controller can answer 400 instead of 500.
var ErrUnknownParentType = errors.New("unknown parent type code")

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

	if _, err := s.repo.GetByCode(parentTypeCode); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownParentType, parentTypeCode)
	}
	return s.TypesByCodes(models.AllowedChildClientTypeCodes(parentTypeCode))
}

// TypesByCodes resolves type codes to catalog rows, keeping the given order
// and skipping unknown or inactive types.
func (s *ClientTypeService) TypesByCodes(codes []string) ([]models.ClientType, error) {
	types := make([]models.ClientType, 0, len(codes))
	for _, code := range codes {
		clientType, err := s.repo.GetByCode(code)
		if err != nil || clientType == nil || !clientType.IsActive {
			continue
		}
		types = append(types, *clientType)
	}
	return types, nil
}

// TypesByCodes is the package-level wrapper over the DI instance.
func TypesByCodes(codes []string) ([]models.ClientType, error) {
	if _clientTypeSvc == nil {
		return nil, clientTypeServiceUnavailable()
	}
	return _clientTypeSvc.TypesByCodes(codes)
}
