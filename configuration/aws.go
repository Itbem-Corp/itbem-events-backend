package configuration

import (
	"context"
	"events-stocks/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"log/slog"
	"os"
	"time"
)

var (
	s3ClientGlobal      *s3.Client
	cognitoClientGlobal *cognitoidentityprovider.Client
)

func InitAwsServices(cfg *models.Config) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s3Config, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(cfg.AwsRegion),
		config.WithCredentialsProvider(aws.CredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.S3ClientId, cfg.S3ClientSecret, ""),
		)),
	)
	if err != nil {
		slog.Error("aws s3 config failed", "error", err)
		os.Exit(1)
	}
	SetS3Client(s3.NewFromConfig(s3Config))

	cognitoConfig, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(cfg.CognitoAwsRegion),
		config.WithCredentialsProvider(aws.CredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.CognitoClientId, cfg.CognitoClientSecret, ""),
		)),
	)
	if err != nil {
		slog.Error("aws cognito config failed", "error", err)
		os.Exit(1)
	}
	SetCognitoClient(cognitoidentityprovider.NewFromConfig(cognitoConfig))
}

// --- S3 Getters/Setters ---
func SetS3Client(client *s3.Client) {
	s3ClientGlobal = client
}

func GetS3Client(_ *models.Config) *s3.Client {
	return s3ClientGlobal
}

// --- Cognito Getters/Setters ---
func SetCognitoClient(client *cognitoidentityprovider.Client) {
	cognitoClientGlobal = client
}

func GetCognitoClient() *cognitoidentityprovider.Client {
	return cognitoClientGlobal
}
