package configuration

import (
	"context"
	"events-stocks/models"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	s3ClientGlobal       *s3.Client
	s3RegionGlobal       string
	s3EndpointGlobal     string
	s3UsePathStyleGlobal bool
	cognitoClientGlobal  *cognitoidentityprovider.Client
)

var discoverS3BucketRegion = func(ctx context.Context, client *s3.Client, bucket string) (string, error) {
	return manager.GetBucketRegion(ctx, client, bucket)
}

// newEC2WorkloadCredentials is replaceable only for bounded unit tests. The
// production implementation is deliberately an EC2 instance-profile provider:
// private automation objects must never fall back to ambient environment keys.
var newEC2WorkloadCredentials = func(cfg aws.Config) aws.CredentialsProvider {
	return aws.NewCredentialsCache(ec2rolecreds.New(func(options *ec2rolecreds.Options) {
		options.Client = imds.NewFromConfig(cfg)
	}))
}

func InitAwsServices(cfg *models.Config) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s3Client, region, err := BuildS3Client(ctx, cfg)
	if err != nil {
		slog.Error("aws s3 config failed", "error", err)
		os.Exit(1)
	}
	SetS3Client(s3Client)
	SetS3Region(region)
	SetS3Endpoint(cfg.S3Endpoint, parseS3UsePathStyle(cfg.S3UsePathStyle))
	slog.Info("aws s3 client configured", "bucket", cfg.AwsBucketName, "region", region, "custom_endpoint", strings.TrimSpace(cfg.S3Endpoint) != "")

	cognitoConfig, err := LoadAWSConfig(ctx, cfg.CognitoAwsRegion, cfg.CognitoClientId, cfg.CognitoClientSecret)
	if err != nil {
		slog.Error("aws cognito config failed", "error", err)
		os.Exit(1)
	}
	SetCognitoClient(cognitoidentityprovider.NewFromConfig(cognitoConfig))

}

// BuildS3Client creates a region-correct S3 client. AWS returns PermanentRedirect
// for PutObject when a client signs against a region other than the bucket's.
// The bucket region is discovered once during startup for AWS S3, even when
// S3_REGION is explicit. This prevents stale configuration from producing
// PermanentRedirect responses after a bucket is created or moved elsewhere.
func BuildS3Client(ctx context.Context, cfg *models.Config) (*s3.Client, string, error) {
	return buildS3Client(ctx, cfg, LoadAWSConfig)
}

type awsConfigLoader func(context.Context, string, string, string) (aws.Config, error)

func buildS3Client(ctx context.Context, cfg *models.Config, loadConfig awsConfigLoader) (*s3.Client, string, error) {
	if cfg == nil {
		return nil, "", fmt.Errorf("S3 config is required")
	}
	explicitS3Region := strings.TrimSpace(cfg.S3Region) != ""
	region := strings.TrimSpace(cfg.S3Region)
	if region == "" {
		region = strings.TrimSpace(cfg.AwsRegion)
	}
	if region == "" {
		return nil, "", fmt.Errorf("AWS_REGION or S3_REGION is required")
	}

	awsCfg, err := loadConfig(ctx, region, cfg.S3ClientId, cfg.S3ClientSecret)
	if err != nil {
		return nil, "", err
	}
	configureS3Transport(&awsCfg)
	clientOptions := s3ClientOptions(cfg)
	client := s3.NewFromConfig(awsCfg, clientOptions)

	// A custom endpoint may not implement AWS bucket-region discovery. Native
	// AWS S3 is always checked so an explicit but stale S3_REGION is corrected.
	if strings.TrimSpace(cfg.S3Endpoint) == "" && strings.TrimSpace(cfg.AwsBucketName) != "" {
		detected, discoverErr := discoverS3BucketRegion(ctx, client, strings.TrimSpace(cfg.AwsBucketName))
		if discoverErr != nil {
			if explicitS3Region {
				slog.Warn("could not verify explicit S3 bucket region; using configured region", "bucket", cfg.AwsBucketName, "region", region, "error", discoverErr)
				return client, region, nil
			}
			return nil, "", fmt.Errorf("discover S3 bucket region for %q: %w", cfg.AwsBucketName, discoverErr)
		}
		detected = strings.TrimSpace(detected)
		if detected != "" && detected != region {
			region = detected
			awsCfg.Region = detected
			client = s3.NewFromConfig(awsCfg, clientOptions)
		}
	}

	return client, region, nil
}

// BuildS3ClientForBucket resolves a client against the actual target bucket.
// The primary media bucket and the private automation buckets may live in
// different regions, so reusing the process-global media client for a sealed
// automation object can produce a redirect or a failed signature. This helper
// preserves the same credential chain and endpoint policy while discovering
// the region for the exact bucket being accessed.
func BuildS3ClientForBucket(ctx context.Context, cfg *models.Config, bucket string) (*s3.Client, string, error) {
	if cfg == nil {
		return nil, "", fmt.Errorf("S3 config is required")
	}
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return nil, "", fmt.Errorf("S3 bucket is required")
	}
	scoped := *cfg
	scoped.AwsBucketName = bucket
	return BuildS3Client(ctx, &scoped)
}

// BuildS3ClientForWorkloadIdentityBucket is for server-mediated private
// automation objects. It deliberately ignores legacy static S3 credentials
// from application configuration so this narrow gateway surface uses the
// workload identity attached to the production host. That role is the audited
// authority for the dedicated automation bucket; leaving a historical media
// credential in the environment must not silently override it.
func BuildS3ClientForWorkloadIdentityBucket(ctx context.Context, cfg *models.Config, bucket string) (*s3.Client, string, error) {
	scoped := workloadIdentityS3Config(cfg)
	if scoped == nil {
		return nil, "", fmt.Errorf("S3 config is required")
	}
	scoped.AwsBucketName = strings.TrimSpace(bucket)
	if scoped.AwsBucketName == "" {
		return nil, "", fmt.Errorf("S3 bucket is required")
	}
	return buildS3Client(ctx, scoped, LoadEC2InstanceRoleConfig)
}

func workloadIdentityS3Config(cfg *models.Config) *models.Config {
	if cfg == nil {
		return nil
	}
	scoped := *cfg
	scoped.S3ClientId = ""
	scoped.S3ClientSecret = ""
	return &scoped
}

func configureS3Transport(cfg *aws.Config) {
	cfg.Retryer = func() aws.Retryer {
		return retry.NewStandard(func(options *retry.StandardOptions) {
			options.MaxAttempts = 4
			options.Backoff = retry.BackoffDelayerFunc(func(attempt int, err error) (time.Duration, error) {
				delay := time.Duration(1<<min(attempt, 2)) * 200 * time.Millisecond
				return delay, nil
			})
		})
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	transport.IdleConnTimeout = 90 * time.Second
	transport.ResponseHeaderTimeout = 20 * time.Second
	cfg.HTTPClient = &http.Client{Transport: transport, Timeout: 2 * time.Minute}
}

func s3ClientOptions(cfg *models.Config) func(*s3.Options) {
	return func(options *s3.Options) {
		if endpoint := strings.TrimSpace(cfg.S3Endpoint); endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
		options.UsePathStyle = parseS3UsePathStyle(cfg.S3UsePathStyle)
	}
}

func parseS3UsePathStyle(value string) bool {
	usePathStyle, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && usePathStyle
}

// LoadAWSConfig uses the AWS SDK default credential chain. The legacy static
// credential aliases are retained only for existing deployments and only when
// both values are present, so a partial legacy configuration cannot shadow a
// valid profile, environment, workload identity, or instance role.
func LoadAWSConfig(ctx context.Context, region, legacyAccessKeyID, legacySecretAccessKey string) (aws.Config, error) {
	options := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if provider := legacyStaticCredentialsProvider(legacyAccessKeyID, legacySecretAccessKey); provider != nil {
		options = append(options, config.WithCredentialsProvider(provider))
	}
	return config.LoadDefaultConfig(ctx, options...)
}

// LoadEC2InstanceRoleConfig is reserved for the backend gateway that mediates
// private automation inputs and outputs. Loading the base configuration keeps
// the normal region, endpoint and HTTP settings, but replacing Credentials
// prevents an accidental AWS_ACCESS_KEY_ID in a container environment from
// shadowing the audited EC2 instance role.
func LoadEC2InstanceRoleConfig(ctx context.Context, region, _, _ string) (aws.Config, error) {
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return aws.Config{}, err
	}
	awsCfg.Credentials = newEC2WorkloadCredentials(awsCfg)
	return awsCfg, nil
}

func SQSClientOptions(endpoint string) func(*sqs.Options) {
	return func(options *sqs.Options) {
		if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	}
}

func legacyStaticCredentialsProvider(accessKeyID, secretAccessKey string) aws.CredentialsProvider {
	accessKeyID = strings.TrimSpace(accessKeyID)
	secretAccessKey = strings.TrimSpace(secretAccessKey)
	if accessKeyID == "" || secretAccessKey == "" {
		return nil
	}
	return credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")
}

// --- S3 Getters/Setters ---
func SetS3Client(client *s3.Client) {
	s3ClientGlobal = client
}

func GetS3Client(_ *models.Config) *s3.Client {
	return s3ClientGlobal
}

func SetS3Region(region string) {
	s3RegionGlobal = strings.TrimSpace(region)
}

func GetS3Region() string {
	return s3RegionGlobal
}

func SetS3Endpoint(endpoint string, usePathStyle bool) {
	s3EndpointGlobal = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	s3UsePathStyleGlobal = usePathStyle
}

func GetS3Endpoint() (string, bool) {
	return s3EndpointGlobal, s3UsePathStyleGlobal
}

// --- Cognito Getters/Setters ---
func SetCognitoClient(client *cognitoidentityprovider.Client) {
	cognitoClientGlobal = client
}

func GetCognitoClient() *cognitoidentityprovider.Client {
	return cognitoClientGlobal
}
