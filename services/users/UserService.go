package users

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	"fmt"
	"log/slog"
	"os"
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
func GetUserByID(userID uuid.UUID) (*models.User, error) { return _userSvc.GetUserByID(userID) }
func InviteUser(email, firstName, lastName string) (*models.User, error) {
	return _userSvc.InviteUser(email, firstName, lastName)
}
func InviteUserForTenant(email, firstName, lastName, tenantCode string) (*models.User, error) {
	return _userSvc.InviteUserForTenant(email, firstName, lastName, tenantCode)
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

// BootstrapConfiguredLocalRoot is intentionally narrower than registration:
// it is callable only after the token middleware has verified a Cognito ID
// token and only provisions an email explicitly listed for ENV=local. It
// exists so a fresh local database does not depend on Cognito AdminGetUser
// credentials merely to create its first developer administrator.
func BootstrapConfiguredLocalRoot(cognitoSub, email string) (*models.User, error) {
	if _userSvc == nil {
		return nil, fmt.Errorf("user service is not configured")
	}
	return _userSvc.BootstrapConfiguredLocalRoot(cognitoSub, email)
}

type storedObjectDeleter interface {
	DeleteObjectByPath(fullPath string) error
}

type bucketStoredObjectDeleter interface {
	DeleteObjectByPathFromBucket(fullPath, bucket string) error
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

func (s *UserService) deleteAvatarObject(path, bucket string) {
	if s.objectDeleter == nil || path == "" {
		return
	}
	var err error
	if scoped, ok := s.objectDeleter.(bucketStoredObjectDeleter); ok {
		err = scoped.DeleteObjectByPathFromBucket(path, bucket)
	} else {
		err = s.objectDeleter.DeleteObjectByPath(path)
	}
	if err != nil {
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

func (s *UserService) GetUserByID(userID uuid.UUID) (*models.User, error) {
	return s.userRepo.GetUserByID(userID)
}

// InviteUser creates the identity in Cognito and mirrors it locally. Cognito
// owns the temporary credential and delivery flow; the dashboard never
// generates, stores, displays, or transmits a password for another user.
func (s *UserService) InviteUser(email, firstName, lastName string) (*models.User, error) {
	return s.InviteUserForTenant(email, firstName, lastName, "eventiapp")
}

type tenantInvitationAuthProvider interface {
	InviteUserForTenant(email, firstName, lastName, tenantCode, provider string) (*dtos.AuthUser, error)
}

func (s *UserService) InviteUserForTenant(email, firstName, lastName, tenantCode string) (*models.User, error) {
	email, firstName, lastName, err := normalizeInviteIdentity(email, firstName, lastName)
	if err != nil {
		return nil, err
	}
	authUser, err := s.inviteAuthUser(email, firstName, lastName, tenantCode)
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

func (s *UserService) inviteAuthUser(email, firstName, lastName, tenantCode string) (*dtos.AuthUser, error) {
	if tenantRepo, ok := s.authRepo.(tenantInvitationAuthProvider); ok {
		return tenantRepo.InviteUserForTenant(email, firstName, lastName, tenantCode, "cognito")
	}
	return s.authRepo.InviteUser(email, firstName, lastName, "cognito")
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
			if currentUser != nil && s.allowLocalUserSyncFallback() {
				slog.Warn("using configured local identity sync fallback", "cognito_sub", cognitoSub)
				return currentUser, nil
			}
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
			if s.isConfiguredLocalBootstrapRoot(authUser.Email) {
				currentUser.IsRoot = true
				currentUser.RootLevel = models.RootLevelPrimary
			}
			if createErr := s.userRepo.CreateUser(currentUser); createErr != nil {
				return nil, createErr
			}
		} else {
			fields := map[string]interface{}{}
			if currentUser.Email != authUser.Email {
				currentUser.Email = authUser.Email
				fields["email"] = currentUser.Email
			}
			if currentUser.FirstName != authUser.FirstName {
				currentUser.FirstName = authUser.FirstName
				fields["first_name"] = currentUser.FirstName
			}
			if currentUser.LastName != authUser.LastName {
				currentUser.LastName = authUser.LastName
				fields["last_name"] = currentUser.LastName
			}
			if s.isConfiguredLocalBootstrapRoot(authUser.Email) && !currentUser.IsPrimaryRoot() {
				currentUser.IsRoot = true
				currentUser.RootLevel = models.RootLevelPrimary
				currentUser.IsActive = true
				fields["is_root"] = true
				fields["root_level"] = models.RootLevelPrimary
				fields["is_active"] = true
			}
			if len(fields) > 0 {
				if updateErr := s.userRepo.UpdateUserFields(currentUser.ID, fields); updateErr != nil {
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

// allowLocalUserSyncFallback is intentionally narrower than a generic
// provider-failure fallback: it requires an explicit local-development flag,
// a pre-existing user, and a token that has already passed the API's normal
// authentication middleware. Production therefore continues to fail closed
// whenever Cognito cannot be consulted.
func (s *UserService) allowLocalUserSyncFallback() bool {
	return s != nil && s.cfg != nil && strings.EqualFold(strings.TrimSpace(s.cfg.AllowLocalUserSyncFallback), "true") && strings.EqualFold(strings.TrimSpace(os.Getenv("ENV")), "local")
}

func (s *UserService) isConfiguredLocalBootstrapRoot(email string) bool {
	if s == nil || s.cfg == nil || !strings.EqualFold(strings.TrimSpace(os.Getenv("ENV")), "local") {
		return false
	}
	normalizedEmail := strings.TrimSpace(email)
	if normalizedEmail == "" {
		return false
	}
	for _, candidate := range strings.Split(s.cfg.LocalBootstrapRootEmails, ",") {
		if strings.EqualFold(normalizedEmail, strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func (s *UserService) BootstrapConfiguredLocalRoot(cognitoSub, email string) (*models.User, error) {
	if !s.isConfiguredLocalBootstrapRoot(email) {
		return nil, fmt.Errorf("local bootstrap identity is not allow-listed")
	}
	cognitoSub = strings.TrimSpace(cognitoSub)
	if cognitoSub == "" {
		return nil, fmt.Errorf("local bootstrap identity is missing a Cognito subject")
	}
	user, err := s.userRepo.GetUserByCognitoSub(cognitoSub)
	if err != nil && err.Error() != "record not found" {
		return nil, err
	}
	if user == nil {
		user = &models.User{
			CognitoSub: cognitoSub,
			Email:      strings.TrimSpace(email),
			IsActive:   true,
			IsRoot:     true,
			RootLevel:  models.RootLevelPrimary,
		}
		if err := s.userRepo.CreateUser(user); err != nil {
			return nil, fmt.Errorf("create configured local bootstrap user: %w", err)
		}
		return user, nil
	}
	if user.IsPrimaryRoot() && user.IsActive {
		return user, nil
	}
	if err := s.userRepo.UpdateUserFields(user.ID, map[string]interface{}{
		"is_active":  true,
		"is_root":    true,
		"root_level": models.RootLevelPrimary,
	}); err != nil {
		return nil, fmt.Errorf("promote configured local bootstrap user: %w", err)
	}
	user.IsActive = true
	user.IsRoot = true
	user.RootLevel = models.RootLevelPrimary
	return user, nil
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
		s.deleteAvatarObject(user.ProfileImage, user.ProfileImageBucket)
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
		s.deleteAvatarObject(user.ProfileImage, user.ProfileImageBucket)
	}
	user.ProfileImage = newImagePath
	return s.userRepo.UpdateUser(user)
}

func (s *UserService) UpdateProfileImageInBucket(userID uuid.UUID, newImagePath, bucket string) error {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return err
	}
	user.ProfileImage = newImagePath
	user.ProfileImageBucket = strings.TrimSpace(bucket)
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
	s.deleteAvatarObject(user.ProfileImage, user.ProfileImageBucket)
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
