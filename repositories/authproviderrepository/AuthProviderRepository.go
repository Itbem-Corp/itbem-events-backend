package authproviderrepository

import (
	"context"
	"events-stocks/dtos"
	"events-stocks/repositories/awsrepository"
	"fmt"
	"strings"
)

const DefaultProvider = "cognito"

func GetUser(sub string, provider string) (*dtos.AuthUser, error) {
	ctx := context.Background()
	if provider == "" {
		provider = DefaultProvider
	}

	switch strings.ToLower(provider) {
	case "cognito", "aws":
		return awsrepository.GetCognitoUser(ctx, sub)
	default:
		return nil, fmt.Errorf("unsupported auth provider: %s", provider)
	}
}

func UpdateUser(sub string, attrs map[string]string, provider string) error {
	ctx := context.Background()
	if provider == "" {
		provider = DefaultProvider
	}

	switch strings.ToLower(provider) {
	case "cognito", "aws":
		return awsrepository.UpdateCognitoUserAttributes(ctx, sub, attrs)
	default:
		return fmt.Errorf("unsupported auth provider: %s", provider)
	}
}

func DeleteUser(sub string, provider string) error {
	ctx := context.Background()
	if provider == "" {
		provider = DefaultProvider
	}

	switch strings.ToLower(provider) {
	case "cognito", "aws":
		return awsrepository.DeleteCognitoUser(ctx, sub)
	default:
		return fmt.Errorf("unsupported auth provider: %s", provider)
	}
}

func CreateUser(req dtos.CreateAuthUserRequest, provider string) (*dtos.AuthUser, error) {
	ctx := context.Background()
	if provider == "" {
		provider = DefaultProvider
	}

	switch strings.ToLower(provider) {
	case "cognito", "aws":
		return awsrepository.CreateCognitoUser(ctx, req)
	default:
		return nil, fmt.Errorf("unsupported auth provider: %s", provider)
	}
}

func SetUserEnabled(sub string, enabled bool, provider string) error {
	ctx := context.Background()
	if provider == "" {
		provider = DefaultProvider
	}

	switch strings.ToLower(provider) {
	case "cognito", "aws":
		if enabled {
			return awsrepository.EnableCognitoUser(ctx, sub)
		}
		return awsrepository.DisableCognitoUser(ctx, sub)
	default:
		return fmt.Errorf("unsupported auth provider: %s", provider)
	}
}

func InviteUser(email, firstName, lastName, provider string) (*dtos.AuthUser, error) {
	ctx := context.Background()
	if provider == "" {
		provider = DefaultProvider
	}

	switch strings.ToLower(provider) {
	case "cognito", "aws":
		return awsrepository.InviteCognitoUser(ctx, email, firstName, lastName)
	default:
		return nil, fmt.Errorf("unsupported auth provider: %s", provider)
	}
}

// AuthProviderRepo implements ports.AuthProviderRepository.
type AuthProviderRepo struct{}

func NewAuthProviderRepo() *AuthProviderRepo { return &AuthProviderRepo{} }

func (r *AuthProviderRepo) GetUser(sub string, provider string) (*dtos.AuthUser, error) {
	return GetUser(sub, provider)
}
func (r *AuthProviderRepo) UpdateUser(sub string, attrs map[string]string, provider string) error {
	return UpdateUser(sub, attrs, provider)
}
func (r *AuthProviderRepo) DeleteUser(sub string, provider string) error {
	return DeleteUser(sub, provider)
}
func (r *AuthProviderRepo) CreateUser(req dtos.CreateAuthUserRequest, provider string) (*dtos.AuthUser, error) {
	return CreateUser(req, provider)
}
func (r *AuthProviderRepo) SetUserEnabled(sub string, enabled bool, provider string) error {
	return SetUserEnabled(sub, enabled, provider)
}
func (r *AuthProviderRepo) InviteUser(email, firstName, lastName, provider string) (*dtos.AuthUser, error) {
	return InviteUser(email, firstName, lastName, provider)
}
