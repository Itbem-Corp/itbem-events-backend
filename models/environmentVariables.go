package models

type Config struct {
	// Server
	Port string `required:"false"`

	// AWS
	AwsRegion           string `required:"true"`
	CognitoAwsRegion    string `required:"true"`
	CognitoUserPoolId   string `required:"true"`
	CognitoClientId     string `required:"true"`
	CognitoClientSecret string `required:"true"`
	S3ClientId          string `required:"true"`
	S3ClientSecret      string `required:"true"`
	AwsBucketName       string `required:"true"`

	// Base de datos
	DbHost     string `required:"true"`
	DbUser     string `required:"true"`
	DbPassword string `required:"true"`
	DbName     string `required:"true"`
	DbPort     string `required:"true"`
	DbTimezone string `required:"true"`

	// Redis
	RedisHost     string `required:"true"`
	RedisPassword string `required:"true"`
	RedisDb       string `required:"true"`
	RedisTls      string `required:"true"`

	// Google OAuth
	GoogleClientId     string `required:"true"`
	GoogleClientSecret string `required:"true"`

	// CORS — comma-separated extra origins (e.g. local dev: http://localhost:4321,http://localhost:3000)
	CorsAllowOrigins string `required:"false"`
}
