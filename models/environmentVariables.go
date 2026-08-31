package models

type Config struct {
	// Server
	Port string `required:"false"`
	// AllowLocalUserSyncFallback is deliberately opt-in and only honored when
	// ENV=local. It keeps an already authenticated local user usable when the
	// development control plane has no AWS administrative credentials to call
	// Cognito AdminGetUser after a restart.
	AllowLocalUserSyncFallback string `required:"false"`
	// LocalBootstrapRootEmails is an explicit, comma-separated allow-list used
	// only with ENV=local. It can provision the first authenticated local
	// platform administrator without putting an identity, password or privilege
	// grant in source control. It is ignored in every deployed environment.
	LocalBootstrapRootEmails string `required:"false"`

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
	// OIDCIssuerURL and OIDCJWKSURL permit a disposable, signed identity
	// provider during isolated local qualification. They are accepted only
	// when ENV=local and both URLs resolve to loopback. Deployed environments
	// always derive the issuer and JWKS endpoint from the Cognito user pool.
	OIDCIssuerURL string `required:"false" env:"OIDC_ISSUER_URL"`
	OIDCJWKSURL   string `required:"false" env:"OIDC_JWKS_URL"`
	// TenantHostMap binds branded API hosts to the Cognito tenant audience.
	// Use explicit product bindings in deployed environments, for example
	// "api.eventiapp.com.mx=eventiapp,api.itbem.com.mx=itbem". A wildcard is
	// only appropriate for an intentional local-development platform host.
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
	RedisHost string `required:"true"`
	// The supported local Valkey compose service deliberately has no password.
	// Production may set one, but startup must not reject the documented local
	// environment simply because this optional value is absent.
	RedisPassword string `required:"false"`
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
	// SQSAutomationQueueURL is the ITBEM-only pull queue consumed by the local
	// AI agent. It must never point to an EventiApp worker or media queue.
	SQSAutomationQueueURL string `required:"false"`
	// SQSAutomationQueueLanesJSON atomically switches publication from the
	// retained combined queue to five role-isolated queues. It must contain the
	// complete orchestration, engineering, review, qa and release map; a partial
	// map is rejected at startup rather than falling back per message.
	SQSAutomationQueueLanesJSON string `required:"false"`
	// SQSAutomationDeadLetterQueueURL is the dedicated failure queue paired
	// with SQSAutomationQueueURL. It is read for operator health only; the API
	// never consumes or republishes its messages automatically.
	SQSAutomationDeadLetterQueueURL string `required:"false"`
	// SQSAutomationRoleDeadLetterQueueURL is the shared failure queue for the
	// five role lanes. The API reads its depth only and never replays it.
	SQSAutomationRoleDeadLetterQueueURL string `required:"false"`
	AutomationInputBucket               string `required:"false"`
	// AutomationOutputBucket stores local-agent results independently from inputs.
	AutomationOutputBucket string `required:"false"`
	// AutomationPricingJSON is a server-owned, versioned price catalog. It may
	// be empty for subscription-backed environments, where execution usage is
	// still recorded but cost is intentionally marked unpriced rather than guessed.
	AutomationPricingJSON string `required:"false"`
	// AutomationBudgetProvider and AutomationBudgetModel declare the non-secret
	// provider/model pair used for conservative admission reservations. They
	// must mirror the local worker deployment; an enforced budget fails closed
	// when that pair is absent from the server-owned pricing catalog.
	AutomationBudgetProvider string `required:"false"`
	AutomationBudgetModel    string `required:"false"`
	// AutomationQASemanticInputTokenReserve and
	// AutomationQASemanticOutputTokenReserve are conservative upper bounds for
	// the separate Stagehand semantic/browser QA model call. They are added to
	// delivery.qa admission holds before a task is queued. Zero uses safe
	// defaults so a missing non-secret setting cannot under-reserve a QA run.
	AutomationQASemanticInputTokenReserve  int `required:"false"`
	AutomationQASemanticOutputTokenReserve int `required:"false"`
	// SQSEndpoint is only for isolated SQS-compatible integration environments.
	// Leave empty in AWS deployments so the SDK resolves the normal AWS endpoint.
	SQSEndpoint       string `required:"false"`
	SNSWorkerTopicARN string `required:"false"`

	// Internal API secret — used by Lambda to call PUT /api/moments/:id/content
	// Generate with: openssl rand -hex 32
	// Must match INTERNAL_API_SECRET in Lambda environment variables.
	InternalAPISecret         string `required:"false"`
	InternalAPISecretPrevious string `required:"false"`
	// AutomationCallbackSecret authenticates the locally operated AI agent when
	// it reports a task result. Rotate via the previous value without downtime.
	AutomationCallbackSecret         string `required:"false"`
	AutomationCallbackSecretPrevious string `required:"false"`
	// GitHubReviewWebhookSecret authenticates the optional pull-request review
	// ingress. It is independent from the GitHub App private key and from the
	// worker callback secret. When empty, the ingress is disabled.
	GitHubReviewWebhookSecret string `required:"false" env:"GITHUB_REVIEW_WEBHOOK_SECRET"`
	// GitHubReviewRepositories is an explicit comma-separated allow-list such
	// as "itbem/backend,itbem/dashboard". A GitHub App installation alone never
	// authorizes every repository to spend local review capacity.
	GitHubReviewRepositories string `required:"false" env:"GITHUB_REVIEW_REPOSITORIES"`
}
