package clienttyperepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

// GetByID busca un ClientType por su ID único (UUID)
func GetByID(id uuid.UUID) (*models.ClientType, error) {
	var t models.ClientType

	err := gormrepository.DB().
		Where("id = ?", id).
		First(&t).Error

	return &t, err
}

// GetChildTypes devuelve tipos con un nivel numérico MAYOR al nivel padre.
// Ej: Si parentLevel es 1 (Plataforma), devuelve 2 (Agencia) y 3 (Cliente).
// Ej: Si parentLevel es 2 (Agencia), devuelve 3 (Cliente).
func GetChildTypes(parentLevel int) ([]models.ClientType, error) {
	var types []models.ClientType

	err := gormrepository.DB().
		Where("level > ?", parentLevel). // 👈 LA MAGIA ESTÁ AQUÍ
		Where("is_active = ?", true).
		Order("level asc"). // Ordenados por jerarquía
		Find(&types).Error

	return types, err
}

// GetRootType devuelve el tipo con el nivel más bajo (1), para cuando no hay padre
func GetRootType() ([]models.ClientType, error) {
	var types []models.ClientType
	err := gormrepository.DB().
		Where("level = ?", 1). // Asumimos que 1 es el Root
		Where("is_active = ?", true).
		Find(&types).Error
	return types, err
}

// Necesitamos GetByCode para saber el nivel del padre actual
func GetByCode(code string) (*models.ClientType, error) {
	var t models.ClientType
	err := gormrepository.DB().Where("code = ?", code).First(&t).Error
	return &t, err
}

// ClientTypeRepo implements ports.ClientTypeRepository.
type ClientTypeRepo struct{}

func NewClientTypeRepo() *ClientTypeRepo { return &ClientTypeRepo{} }

func (r *ClientTypeRepo) GetByID(id uuid.UUID) (*models.ClientType, error) { return GetByID(id) }
func (r *ClientTypeRepo) GetByCode(code string) (*models.ClientType, error) { return GetByCode(code) }
func (r *ClientTypeRepo) GetChildTypes(parentLevel int) ([]models.ClientType, error) {
	return GetChildTypes(parentLevel)
}
func (r *ClientTypeRepo) GetRootType() ([]models.ClientType, error) { return GetRootType() }
