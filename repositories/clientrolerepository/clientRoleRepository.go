package clientrolerepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

// GetByID busca un ClientType por su UUID
func GetByID(id uuid.UUID) (*models.ClientRole, error) {
	var role models.ClientRole

	if err := gormrepository.DB().First(&role, "id = ?", id).Error; err != nil {
		return nil, err
	}

	return &role, nil
}

// GetAssignableRoles devuelve roles con jerarquía igual o inferior (número mayor)
func GetAssignableRoles(myHierarchyLevel int) ([]models.ClientRole, error) {
	var roles []models.ClientRole

	// Si soy nivel 2 (Admin), traigo todo lo que sea >= 2 (Admin, Member, Guest)
	// El nivel 1 (Owner) queda excluido.
	err := gormrepository.DB().
		Where("hierarchy >= ?", myHierarchyLevel).
		Where("is_active = ?", true).
		Order("hierarchy asc"). // Para que salgan ordenaditos en el select
		Find(&roles).Error

	return roles, err
}

// GetByCode sigue siendo necesario para saber MI nivel actual
func GetByCode(code string) (*models.ClientRole, error) {
	var role models.ClientRole
	err := gormrepository.DB().Where("code = ?", code).First(&role).Error
	return &role, err
}

// ClientRoleRepo implements ports.ClientRoleRepository.
type ClientRoleRepo struct{}

func NewClientRoleRepo() *ClientRoleRepo { return &ClientRoleRepo{} }

func (r *ClientRoleRepo) GetByCode(code string) (*models.ClientRole, error) { return GetByCode(code) }
func (r *ClientRoleRepo) GetByID(id uuid.UUID) (*models.ClientRole, error)  { return GetByID(id) }
func (r *ClientRoleRepo) GetAssignableRoles(myHierarchyLevel int) ([]models.ClientRole, error) {
	return GetAssignableRoles(myHierarchyLevel)
}
