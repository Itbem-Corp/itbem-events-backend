package configuration

import (
	"context"
	"events-stocks/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func InitAwsServices(cfg *models.Config) {
	initS3(cfg)
}

func initS3(cfg *models.Config) {
	awsCfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(cfg.AwsRegion),
		config.WithCredentialsProvider(aws.CredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.S3ClientId, cfg.S3ClientSecret, ""),
		)),
	)
	if err != nil {
		panic("failed to load AWS config: " + err.Error())
	}

	// Pasarlo al repo global
	SetS3Client(s3.NewFromConfig(awsCfg))
}

// Esto lo consume AwsRepository.InitS3
var s3ClientGlobal *s3.Client

func SetS3Client(client *s3.Client) {
	s3ClientGlobal = client
}

func GetS3Client(_ *models.Config) *s3.Client {
	return s3ClientGlobal
}
