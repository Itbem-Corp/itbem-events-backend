package aicredentials

import (
	"context"
	"errors"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

type secretsManagerClient interface {
	GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
	PutSecretValue(context.Context, *secretsmanager.PutSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
}

type SecretsManagerStore struct{ client secretsManagerClient }

func NewSecretsManagerStore(ctx context.Context, region string) (*SecretsManagerStore, error) {
	options := []func(*awsconfig.LoadOptions) error{}
	if region = strings.TrimSpace(region); region != "" {
		options = append(options, awsconfig.WithRegion(region))
	}
	config, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, errors.New("AWS credentials for AI provider storage are unavailable")
	}
	return &SecretsManagerStore{client: secretsmanager.NewFromConfig(config)}, nil
}

func (s *SecretsManagerStore) Get(ctx context.Context, id string) ([]byte, error) {
	if s == nil || s.client == nil || strings.TrimSpace(id) == "" {
		return nil, errors.New("AI provider storage is not configured")
	}
	response, err := s.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &id})
	if err != nil || response == nil || response.SecretString == nil {
		return nil, errors.New("AI provider credential bundle is unavailable")
	}
	return []byte(*response.SecretString), nil
}

func (s *SecretsManagerStore) Put(ctx context.Context, id string, value []byte) error {
	if s == nil || s.client == nil || strings.TrimSpace(id) == "" || len(value) == 0 || len(value) > maxBundleBytes {
		return errors.New("AI provider storage input is invalid")
	}
	secret := string(value)
	_, err := s.client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: &id, SecretString: &secret})
	if err != nil {
		return errors.New("AI provider credential could not be stored")
	}
	return nil
}
