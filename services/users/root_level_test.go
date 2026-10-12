package users

import (
	"errors"
	"events-stocks/models"
	"fmt"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rootLevelRepo(target *models.User, writes *[]map[string]interface{}) *mockUserRepo {
	return &mockUserRepo{
		GetUserByIDFunc: func(id uuid.UUID) (*models.User, error) {
			if target == nil || id != target.ID {
				return nil, gorm.ErrRecordNotFound
			}
			copied := *target
			return &copied, nil
		},
		UpdateUserFieldsFunc: func(id uuid.UUID, fields map[string]interface{}) error {
			*writes = append(*writes, fields)
			return nil
		},
	}
}

// A primary root (level 1, or a historical is_root account without a level)
// can never be demoted through the API: SetUserRootLevel refuses to assign
// level 1, so a demotion could not be undone and the platform could lose its
// last primary root. Because only a primary root may call this, the same rule
// also covers self-demotion.
func TestSetUserRootLevelRefusesPrimaryRootTargets(t *testing.T) {
	targets := []struct {
		name   string
		target models.User
	}{
		{name: "legacy is_root account", target: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}},
		{name: "explicit level 1 account", target: models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelPrimary}},
		{name: "the requesting primary root itself", target: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelPrimary}},
	}
	for _, target := range targets {
		for _, level := range []int{models.RootLevelNone, models.RootLevelOperational} {
			t.Run(fmt.Sprintf("%s to level %d", target.name, level), func(t *testing.T) {
				var writes []map[string]interface{}
				svc := newUserService(rootLevelRepo(&target.target, &writes), nil)

				err := svc.SetUserRootLevel(target.target.ID, level)

				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrProtectedRootAccount), "got %v", err)
				assert.Empty(t, writes, "a protected account must never be written")
			})
		}
	}
}

func TestSetUserRootLevelGrantsAndRevokesOperationalLevel(t *testing.T) {
	tests := []struct {
		name   string
		target models.User
		level  int
	}{
		{name: "grant level 2 to a standard user", target: models.User{ID: uuid.Must(uuid.NewV4())}, level: models.RootLevelOperational},
		{name: "revoke level 2", target: models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational}, level: models.RootLevelNone},
		{name: "normalize a legacy is_root account already at level 2", target: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelOperational}, level: models.RootLevelNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var writes []map[string]interface{}
			svc := newUserService(rootLevelRepo(&test.target, &writes), nil)

			require.NoError(t, svc.SetUserRootLevel(test.target.ID, test.level))

			require.Len(t, writes, 1)
			assert.Equal(t, test.level, writes[0]["root_level"])
			assert.Equal(t, false, writes[0]["is_root"])
		})
	}
}

func TestSetUserRootLevelRejectsInvalidLevelsAndUnknownUsers(t *testing.T) {
	target := models.User{ID: uuid.Must(uuid.NewV4())}
	var writes []map[string]interface{}
	svc := newUserService(rootLevelRepo(&target, &writes), nil)

	err := svc.SetUserRootLevel(target.ID, models.RootLevelPrimary)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrProtectedRootAccount))

	err = svc.SetUserRootLevel(uuid.Must(uuid.NewV4()), models.RootLevelOperational)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "got %v", err)
	assert.Empty(t, writes)
}
