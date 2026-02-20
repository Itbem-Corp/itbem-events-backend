package userrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

func ListAllUsers() ([]models.User, error) {
	var users []models.User
	err := gormrepository.DB().
		Order("created_at DESC").
		Find(&users).
		Error

	return users, err
}

func ListAllUsersPaginated(page, pageSize int) ([]models.User, int64, error) {
	var users []models.User
	var total int64
	db := gormrepository.DB()
	if err := db.Model(&models.User{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * pageSize
	err := db.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&users).Error
	return users, total, err
}

func SetUserActive(userID uuid.UUID, active bool) error {
	fields := map[string]interface{}{
		"is_active": active,
	}

	return gormrepository.UpdateFieldsByID(
		userID,
		fields,
		&models.User{},
	)
}
