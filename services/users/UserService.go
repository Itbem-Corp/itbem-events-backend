package users

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"golang.org/x/sync/singleflight"
)

// _userSvc is the package-level singleton set by internal/app.
var _userSvc *UserService

// SetDefaultUserService wires the package-level functions to the DI instance.
func SetDefaultUserService(svc *UserService) { _userSvc = svc }

func RegisterUser(email, password, firstName, lastName string) (*models.User, error) {
	return _userSvc.RegisterUser(email, password, firstName, lastName)
}
func SyncUser(cognitoSub string) (*models.User, error) { return _userSvc.SyncUser(cognitoSub) }
func DeleteFullAccount(cognitoSub string) error        { return _userSvc.DeleteFullAccount(cognitoSub) }
func GetUserByEmail(email string) (*models.User, error) {
	return _userSvc.GetUserByEmail(email)
}
func UpdateProfileImage(userID uuid.UUID, newImagePath string) error {
	return _userSvc.UpdateProfileImage(userID, newImagePath)
}
func UpdateUserInformation(userID uuid.UUID, firstName, lastName string) (*models.User, error) {
	return _userSvc.UpdateUserInformation(userID, firstName, lastName)
}
func DeleteUserAvatar(user *models.User)              { _userSvc.DeleteUserAvatar(user) }
func ClearProfileImage(userID uuid.UUID) error        { return _userSvc.ClearProfileImage(userID) }
func SetUserRoot(userID uuid.UUID, isRoot bool) error { return _userSvc.SetUserRoot(userID, isRoot) }
func SetUserRootLevel(userID uuid.UUID, level int) error {
	return _userSvc.SetUserRootLevel(userID, level)
}

type storedObjectDeleter interface {
	DeleteObjectByPath(fullPath string) error
}

const userProviderSyncInterval = 5 * time.Minute

// UserService is the injectable, struct-based user service.
type UserService struct {
	userRepo       ports.UserRepository
	authRepo       ports.AuthProviderRepository
	cfg            *models.Config
	objectDeleter  storedObjectDeleter
	providerSync   singleflight.Group
	providerSyncAt sync.Map
}

func NewUserService(userRepo ports.UserRepository, authRepo ports.AuthProviderRepository, cfg *models.Config, objectDeleter ...storedObjectDeleter) *UserService {
	var deleter storedObjectDeleter
	if len(objectDeleter) > 0 {
		deleter = objectDeleter[0]
	}
	return &UserService{userRepo: userRepo, authRepo: authRepo, cfg: cfg, objectDeleter: deleter}
}

func (s *UserService) deleteAvatarObject(path string) {
	if s.objectDeleter == nil || path == "" {
		return
	}
	if err := s.objectDeleter.DeleteObjectByPath(path); err != nil {
		slog.Warn("failed to delete avatar object", "path", path, "error", err)
	}
}

func (s *UserService) RegisterUser(email, password, firstName, lastName string) (*models.User, error) {
	req := dtos.CreateAuthUserRequest{
		Email:     email,
		Password:  password,
		FirstName: firstName,
		LastName:  lastName,
	}
	authUser, err := s.authRepo.CreateUser(req, "cognito")
	if err != nil {
		return nil, err
	}
	newUser := &models.User{
		CognitoSub: authUser.Sub,
		Email:      authUser.Email,
		FirstName:  authUser.FirstName,
		LastName:   authUser.LastName,
		IsActive:   true,
		IsRoot:     false,
	}
	if err := s.userRepo.CreateUser(newUser); err != nil {
		_ = s.authRepo.DeleteUser(authUser.Sub, "cognito")
		return nil, err
	}
	return newUser, nil
}

func (s *UserService) SyncUser(cognitoSub string) (*models.User, error) {
	user, err := s.userRepo.GetUserByCognitoSub(cognitoSub)
	if err != nil && err.Error() != "record not found" {
		return nil, err
	}
	if user != nil && s.providerUserSyncedRecently(cognitoSub, time.Now()) {
		return user, nil
	}

	value, err, _ := s.providerSync.Do(cognitoSub, func() (interface{}, error) {
		currentUser, currentErr := s.userRepo.GetUserByCognitoSub(cognitoSub)
		if currentErr != nil && currentErr.Error() != "record not found" {
			return nil, currentErr
		}
		if currentUser != nil && s.providerUserSyncedRecently(cognitoSub, time.Now()) {
			return currentUser, nil
		}

		authUser, authErr := s.authRepo.GetUser(cognitoSub, "cognito")
		if authErr != nil {
			return nil, authErr
		}

		if currentUser == nil {
			currentUser = &models.User{
				CognitoSub: cognitoSub,
				Email:      authUser.Email,
				FirstName:  authUser.FirstName,
				LastName:   authUser.LastName,
				IsActive:   authUser.IsActive,
			}
			if createErr := s.userRepo.CreateUser(currentUser); createErr != nil {
				return nil, createErr
			}
		} else {
			dirty := false
			if currentUser.Email != authUser.Email {
				currentUser.Email = authUser.Email
				dirty = true
			}
			if currentUser.FirstName != authUser.FirstName {
				currentUser.FirstName = authUser.FirstName
				dirty = true
			}
			if currentUser.LastName != authUser.LastName {
				currentUser.LastName = authUser.LastName
				dirty = true
			}
			if dirty {
				if updateErr := s.userRepo.UpdateUserFields(currentUser.ID, map[string]interface{}{
					"email":      currentUser.Email,
					"first_name": currentUser.FirstName,
					"last_name":  currentUser.LastName,
				}); updateErr != nil {
					slog.Warn("failed to sync user fields", "userID", currentUser.ID, "error", updateErr)
				}
			}
		}

		s.providerSyncAt.Store(cognitoSub, time.Now())
		return currentUser, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*models.User), nil
}

func (s *UserService) providerUserSyncedRecently(cognitoSub string, now time.Time) bool {
	value, ok := s.providerSyncAt.Load(cognitoSub)
	if !ok {
		return false
	}
	syncedAt, ok := value.(time.Time)
	return ok && now.Sub(syncedAt) < userProviderSyncInterval
}

func (s *UserService) GetUserByEmail(email string) (*models.User, error) {
	normalizedEmail := strings.TrimSpace(email)
	if normalizedEmail == "" {
		return nil, fmt.Errorf("email is required")
	}
	return s.userRepo.GetUserByEmail(normalizedEmail)
}

func (s *UserService) DeleteFullAccount(cognitoSub string) error {
	user, err := s.userRepo.GetUserByCognitoSub(cognitoSub)
	if err != nil {
		return err
	}
	if user.ProfileImage != "" {
		s.deleteAvatarObject(user.ProfileImage)
	}
	if err := s.authRepo.DeleteUser(cognitoSub, "cognito"); err != nil {
		return fmt.Errorf("no se pudo eliminar la identidad en la nube, operación cancelada: %w", err)
	}
	if err := s.userRepo.DeleteUser(user.ID); err != nil {
		slog.Error("CRITICAL: user deleted from cognito but local delete failed", "userID", user.ID, "cognitoSub", cognitoSub, "error", err)
		return fmt.Errorf("cuenta cerrada, pero error limpiando datos locales: %w", err)
	}
	return nil
}

func (s *UserService) UpdateProfileImage(userID uuid.UUID, newImagePath string) error {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return err
	}
	if user.ProfileImage != "" && user.ProfileImage != newImagePath {
		s.deleteAvatarObject(user.ProfileImage)
	}
	user.ProfileImage = newImagePath
	return s.userRepo.UpdateUser(user)
}

func (s *UserService) UpdateUserInformation(userID uuid.UUID, firstName, lastName string) (*models.User, error) {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	if firstName == "" || lastName == "" {
		return nil, fmt.Errorf("nombre y apellido son requeridos")
	}
	attrs := map[string]string{
		"given_name":  firstName,
		"family_name": lastName,
	}
	if err := s.authRepo.UpdateUser(user.CognitoSub, attrs, "cognito"); err != nil {
		return nil, fmt.Errorf("error actualizando cognito: %w", err)
	}
	user.FirstName = firstName
	user.LastName = lastName
	if err := s.userRepo.UpdateUser(user); err != nil {
		return nil, fmt.Errorf("error actualizando base de datos local: %w", err)
	}
	return user, nil
}

func (s *UserService) DeleteUserAvatar(user *models.User) {
	if user.ProfileImage == "" {
		return
	}
	s.deleteAvatarObject(user.ProfileImage)
}

func (s *UserService) ClearProfileImage(userID uuid.UUID) error {
	return s.userRepo.ClearProfileImage(userID)
}

func (s *UserService) SetUserRoot(userID uuid.UUID, isRoot bool) error {
	return s.userRepo.SetUserRoot(userID, isRoot)
}

// SetUserRootLevel is deliberately narrow: level 1 cannot be delegated from
// the dashboard; a primary root may only grant or revoke operational level 2.
func (s *UserService) SetUserRootLevel(userID uuid.UUID, level int) error {
	if level != models.RootLevelNone && level != models.RootLevelOperational {
		return fmt.Errorf("only operational root level can be assigned")
	}
	if _, err := s.userRepo.GetUserByID(userID); err != nil {
		return err
	}
	return s.userRepo.UpdateUserFields(userID, map[string]interface{}{
		"root_level": level,
		"is_root":    false,
	})
}
