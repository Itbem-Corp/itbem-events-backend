package users

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/bucketrepository"
	"events-stocks/services/ports"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gofrs/uuid"
)

// _userSvc is the package-level singleton set by server.go.
var _userSvc *UserService

// SetDefaultUserService wires the package-level functions to the DI instance.
func SetDefaultUserService(svc *UserService) { _userSvc = svc }

func RegisterUser(email, password, firstName, lastName string) (*models.User, error) {
	return _userSvc.RegisterUser(email, password, firstName, lastName)
}
func SyncUser(cognitoSub string) (*models.User, error) { return _userSvc.SyncUser(cognitoSub) }
func DeleteFullAccount(cognitoSub string) error        { return _userSvc.DeleteFullAccount(cognitoSub) }
func UpdateProfileImage(userID uuid.UUID, newImagePath string) error {
	return _userSvc.UpdateProfileImage(userID, newImagePath)
}
func UpdateUserInformation(userID uuid.UUID, firstName, lastName string) (*models.User, error) {
	return _userSvc.UpdateUserInformation(userID, firstName, lastName)
}
func DeleteUserAvatar(user *models.User)            { _userSvc.DeleteUserAvatar(user) }
func ClearProfileImage(userID uuid.UUID) error      { return _userSvc.ClearProfileImage(userID) }
func SetUserRoot(userID uuid.UUID, isRoot bool) error { return _userSvc.SetUserRoot(userID, isRoot) }

// UserService is the injectable, struct-based user service.
type UserService struct {
	userRepo ports.UserRepository
	authRepo ports.AuthProviderRepository
	cfg      *models.Config
}

func NewUserService(userRepo ports.UserRepository, authRepo ports.AuthProviderRepository, cfg *models.Config) *UserService {
	return &UserService{userRepo: userRepo, authRepo: authRepo, cfg: cfg}
}

func (s *UserService) deleteAvatarFromS3(path string) {
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return
	}
	filename := parts[len(parts)-1]
	folder := strings.Join(parts[:len(parts)-1], "/")
	err := bucketrepository.DeleteFile(filename, folder, s.cfg.AwsBucketName, "aws")
	if err != nil {
		slog.Warn("failed to delete avatar from s3", "path", path, "error", err)
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
	authUser, err := s.authRepo.GetUser(cognitoSub, "cognito")
	if err != nil {
		return nil, err
	}
	user, err := s.userRepo.GetUserByCognitoSub(cognitoSub)
	if err != nil && err.Error() != "record not found" {
		return nil, err
	}
	if user == nil {
		newUser := &models.User{
			CognitoSub: cognitoSub,
			Email:      authUser.Email,
			FirstName:  authUser.FirstName,
			LastName:   authUser.LastName,
			IsActive:   authUser.IsActive,
		}
		if err := s.userRepo.CreateUser(newUser); err != nil {
			return nil, err
		}
		return newUser, nil
	}
	dirty := false
	if user.Email != authUser.Email {
		user.Email = authUser.Email
		dirty = true
	}
	if user.FirstName != authUser.FirstName {
		user.FirstName = authUser.FirstName
		dirty = true
	}
	if user.LastName != authUser.LastName {
		user.LastName = authUser.LastName
		dirty = true
	}
	if dirty {
		if err := s.userRepo.UpdateUserFields(user.ID, map[string]interface{}{
			"email":      user.Email,
			"first_name": user.FirstName,
			"last_name":  user.LastName,
		}); err != nil {
			slog.Warn("failed to sync user fields", "userID", user.ID, "error", err)
		}
	}
	return user, nil
}

func (s *UserService) DeleteFullAccount(cognitoSub string) error {
	user, err := s.userRepo.GetUserByCognitoSub(cognitoSub)
	if err != nil {
		return err
	}
	if user.ProfileImage != "" {
		s.deleteAvatarFromS3(user.ProfileImage)
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
		s.deleteAvatarFromS3(user.ProfileImage)
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
	s.deleteAvatarFromS3(user.ProfileImage)
}

func (s *UserService) ClearProfileImage(userID uuid.UUID) error {
	return s.userRepo.ClearProfileImage(userID)
}

func (s *UserService) SetUserRoot(userID uuid.UUID, isRoot bool) error {
	return s.userRepo.SetUserRoot(userID, isRoot)
}
