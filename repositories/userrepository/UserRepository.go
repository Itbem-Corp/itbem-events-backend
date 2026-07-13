package userrepository

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"strings"
)

// GetUserByCognitoSub busca un usuario usando el ID de AWS
func GetUserByCognitoSub(sub string) (*models.User, error) {
	var user models.User
	// Buscamos por el campo único CognitoSub
	err := gormrepository.DB().Where("cognito_sub = ?", sub).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByEmail(email string) (*models.User, error) {
	var user models.User
	normalizedEmail := strings.TrimSpace(email)
	err := gormrepository.DB().
		Where("LOWER(email) = LOWER(?)", normalizedEmail).
		First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByID busca por el UUID interno
func GetUserByID(id uuid.UUID) (*models.User, error) {
	var user models.User
	err := gormrepository.GetByID(&user, id)
	return &user, err
}

// CreateUser inserta el usuario en la DB
func CreateUser(user *models.User) error {
	return gormrepository.Insert(user)
}

// DeleteUser elimina el usuario y sus relaciones de forma ATÓMICA
func DeleteUser(id uuid.UUID) error {
	// Usamos Transaction para que sea "Todo o Nada"
	return gormrepository.DB().Transaction(func(tx *gorm.DB) error {

		// 1. Desvincular de Eventos
		// Nota: Usamos 'tx' en lugar de 'db' o 'gormrepository.DB()' dentro de la transacción
		if err := tx.Where("user_id = ?", id).Delete(&models.EventMember{}).Error; err != nil {
			return err
		}

		// 2. Desvincular de Clientes
		if err := tx.Where("user_id = ?", id).Delete(&models.ClientMember{}).Error; err != nil {
			return err
		}

		// 3. Borrar Usuario (Soft Delete)
		// Aquí debemos llamar al Delete de GORM manualmente sobre 'tx'
		// porque gormrepository.Delete usa la instancia global DB, no la transacción.
		if err := tx.Delete(&models.User{}, id).Error; err != nil {
			return err
		}

		// Si todo sale bien, retorna nil y hace Commit automático.
		return nil
	})
}

// UpdateUser actualiza datos (ej. si cambió el nombre en el frontend)
func UpdateUser(user *models.User) error {
	return gormrepository.Update(user, user.ID)
}

func ClearProfileImage(userID uuid.UUID) error {
	fields := map[string]interface{}{
		"profile_image": "",
	}

	return gormrepository.UpdateFieldsByID(
		userID,
		fields,
		&models.User{},
	)
}

func UpdateUserFields(userID uuid.UUID, fields map[string]interface{}) error {
	return gormrepository.UpdateFieldsByID(
		userID,
		fields,
		&models.User{},
	)
}

func SetUserRoot(userID uuid.UUID, isRoot bool) error {
	fields := map[string]interface{}{
		"is_root": isRoot,
	}

	return gormrepository.UpdateFieldsByID(
		userID,
		fields,
		&models.User{},
	)
}

// UserRepo implements ports.UserRepository.
type UserRepo struct{}

func NewUserRepo() *UserRepo { return &UserRepo{} }

func (r *UserRepo) CreateUser(user *models.User) error { return CreateUser(user) }
func (r *UserRepo) UpdateUser(user *models.User) error { return UpdateUser(user) }
func (r *UserRepo) DeleteUser(id uuid.UUID) error      { return DeleteUser(id) }
func (r *UserRepo) GetUserByID(id uuid.UUID) (*models.User, error) {
	return GetUserByID(id)
}
func (r *UserRepo) GetUserByCognitoSub(sub string) (*models.User, error) {
	return GetUserByCognitoSub(sub)
}
func (r *UserRepo) GetUserByEmail(email string) (*models.User, error) {
	return GetUserByEmail(email)
}
func (r *UserRepo) UpdateUserFields(userID uuid.UUID, fields map[string]interface{}) error {
	return UpdateUserFields(userID, fields)
}
func (r *UserRepo) ClearProfileImage(userID uuid.UUID) error { return ClearProfileImage(userID) }
func (r *UserRepo) SetUserRoot(userID uuid.UUID, isRoot bool) error {
	return SetUserRoot(userID, isRoot)
}
func (r *UserRepo) ListAllUsers() ([]models.User, error) { return ListAllUsers() }
func (r *UserRepo) ListAllUsersPaginated(query dtos.AdminUsersListQuery) ([]models.User, int64, error) {
	return ListAllUsersPaginated(query)
}
func (r *UserRepo) SetUserActive(userID uuid.UUID, active bool) error {
	return SetUserActive(userID, active)
}
