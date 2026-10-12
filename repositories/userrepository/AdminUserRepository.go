package userrepository

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"strings"

	"github.com/gofrs/uuid"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

func ListAllUsers() ([]models.User, error) {
	var users []models.User
	err := gormrepository.DB().
		Order("created_at DESC").
		Find(&users).
		Error

	return users, err
}

func applyAdminUsersListFilters(db *gorm.DB, query dtos.AdminUsersListQuery) *gorm.DB {
	// Collapse inner whitespace so "Juan  Pérez" matches the full name, and
	// escape LIKE wildcards so "%" or "_" are searched literally.
	search := strings.ToLower(strings.Join(strings.Fields(query.Search), " "))
	if search != "" {
		like := "%" + escapeLikePattern(search) + "%"
		db = db.Where(
			`LOWER(email) LIKE ? ESCAPE '\' OR LOWER(first_name) LIKE ? ESCAPE '\' OR LOWER(last_name) LIKE ? ESCAPE '\' OR LOWER(CONCAT_WS(' ', first_name, last_name)) LIKE ? ESCAPE '\'`,
			like,
			like,
			like,
			like,
		)
	}

	switch strings.ToLower(strings.TrimSpace(query.Status)) {
	case "active":
		db = db.Where("is_active = ?", true)
	case "inactive":
		db = db.Where("is_active = ?", false)
	case "root":
		db = db.Where("is_root = ? OR root_level > ?", true, models.RootLevelNone)
	case "non_root":
		// Keep the support directory free of both new root-level accounts and
		// legacy records that still rely on is_root.
		db = db.Where("(is_root = ? OR is_root IS NULL) AND (root_level = ? OR root_level IS NULL)", false, models.RootLevelNone)
	}

	return db
}

// escapeLikePattern escapes the LIKE metacharacters (backslash first) for use
// with ESCAPE '\'.
func escapeLikePattern(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func ListAllUsersPaginated(query dtos.AdminUsersListQuery) ([]models.User, int64, error) {
	var users []models.User
	var total int64
	offset := (query.Page - 1) * query.PageSize
	group := new(errgroup.Group)
	group.Go(func() error {
		return applyAdminUsersListFilters(gormrepository.DB().Model(&models.User{}), query).Count(&total).Error
	})
	group.Go(func() error {
		return applyAdminUsersListFilters(gormrepository.DB().Model(&models.User{}), query).
			Order("created_at DESC").Offset(offset).Limit(query.PageSize).Find(&users).Error
	})
	if err := group.Wait(); err != nil {
		return nil, 0, err
	}
	return users, total, nil
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
