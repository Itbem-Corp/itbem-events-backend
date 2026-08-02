package models

type Config struct {
	// Server
	Port string `required:"false"`

	// AWS
	AwsRegion         string `required:"true"`
	CognitoAwsRegion  string `required:"true"`
	CognitoUserPoolId string `required:"true"`
	// Deprecated static-credential aliases. New environments use the AWS SDK
	// default credential chain; a legacy pair is used only when both values exist.
	CognitoClientId     string `required:"false"`
	CognitoClientSecret string `required:"false"`
	// Signed ID-token audiences accepted by the API and their tenant binding.
	// Map format: "client-id=eventiapp,other-client-id=itbem".
	CognitoAllowedClientIds string `required:"false"`
	CognitoTenantClientMap  string `required:"false"`
	// TenantHostMap binds branded API hosts to the Cognito tenant audience.
	// Use "api.eventiapp.com.mx=*" only for the migration-compatible platform host.
	TenantHostMap string `required:"false"`
	// JwtClockSkewSeconds is an explicit deployment-level JWT leeway. Keep it
	// empty in production; local environments with a skewed host clock may opt in.
	JwtClockSkewSeconds string `required:"false"`
	S3ClientId          string `required:"false"`
	S3ClientSecret      string `required:"false"`
	AwsBucketName       string `required:"true"`
	// TenantBucketMap is the server-owned physical storage routing table.
	// Format: "eventiapp=bucket-a,itbem=bucket-b,cafettonhouse=bucket-c".
	TenantBucketMap string `required:"false"`
	// S3Region may differ from the region used by Cognito/SQS. When omitted,
	// startup discovers the bucket region before constructing the S3 client.
	S3Region       string `required:"false"`
	S3Endpoint     string `required:"false"`
	S3UsePathStyle string `required:"false"`
	// CDNBaseURL identifies legacy object URLs that must be converted back to
	// canonical S3 keys before private presigning or worker publication. It is
	// not an authorization boundary and is never used to expose private media.
	CDNBaseURL string `required:"false"`

	// Base de datos
	DbHost     string `required:"true"`
	DbUser     string `required:"true"`
	DbPassword string `required:"true"`
	DbName     string `required:"true"`
	DbPort     string `required:"true"`
	DbTimezone string `required:"true"`
	// DbLogLevel accepts silent, error, warn, or info. When empty, local
	// development uses info and deployed environments use warn.
	DbLogLevel string `required:"false"`

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
	// TrustedProxyCidrs is the comma-separated list of reverse-proxy networks
	// allowed to supply X-Forwarded-For. When empty, only loopback proxies are
	// trusted. Never add broad client networks such as 0.0.0.0/0.
	TrustedProxyCidrs string `required:"false"`

	// Media processing — separate SQS queues for images and videos (Lambda).
	// Leave empty to disable async processing for that type.
	// Recommended: route to separate queues for independent concurrency control.
	SQSImageQueueURL string `required:"false"` // itbem-media-images queue
	SQSVideoQueueURL string `required:"false"` // itbem-media-videos queue
	// Business/data jobs consumed by itbem-events-workers (never media jobs).
	SQSWorkerQueueURL string `required:"false"`
	// SQSEndpoint is only for isolated SQS-compatible integration environments.
	// Leave empty in AWS deployments so the SDK resolves the normal AWS endpoint.
	SQSEndpoint       string `required:"false"`
	SNSWorkerTopicARN string `required:"false"`

	// Internal API secret — used by Lambda to call PUT /api/moments/:id/content
	// Generate with: openssl rand -hex 32
	// Must match INTERNAL_API_SECRET in Lambda environment variables.
	InternalAPISecret         string `required:"false"`
	InternalAPISecretPrevious string `required:"false"`
}
