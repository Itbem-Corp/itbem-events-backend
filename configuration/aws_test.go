package configuration

import (
	"context"
	"errors"
	"testing"

	"events-stocks/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAWSConfigCredentialResolution(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "standard-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "standard-secret-key")
	t.Setenv("AWS_SESSION_TOKEN", "standard-session-token")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	tests := []struct {
		name             string
		legacyAccessKey  string
		legacySecretKey  string
		wantAccessKey    string
		wantSessionToken string
	}{
		{
			name:             "default chain when legacy values are empty",
			wantAccessKey:    "standard-access-key",
			wantSessionToken: "standard-session-token",
		},
		{
			name:             "default chain when only legacy access key is present",
			legacyAccessKey:  "partial-legacy-key",
			wantAccessKey:    "standard-access-key",
			wantSessionToken: "standard-session-token",
		},
		{
			name:             "default chain when only legacy secret is present",
			legacySecretKey:  "partial-legacy-secret",
			wantAccessKey:    "standard-access-key",
			wantSessionToken: "standard-session-token",
		},
		{
			name:            "complete legacy pair remains compatible",
			legacyAccessKey: "legacy-access-key",
			legacySecretKey: "legacy-secret-key",
			wantAccessKey:   "legacy-access-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadAWSConfig(context.Background(), "us-east-1", tt.legacyAccessKey, tt.legacySecretKey)
			if err != nil {
				t.Fatalf("LoadAWSConfig returned an error: %v", err)
			}
			if cfg.Region != "us-east-1" {
				t.Fatalf("expected region us-east-1, got %q", cfg.Region)
			}

			credentials, err := cfg.Credentials.Retrieve(context.Background())
			if err != nil {
				t.Fatalf("retrieve credentials: %v", err)
			}
			if credentials.AccessKeyID != tt.wantAccessKey {
				t.Fatalf("expected access key %q, got %q", tt.wantAccessKey, credentials.AccessKeyID)
			}
			if credentials.SessionToken != tt.wantSessionToken {
				t.Fatalf("expected session token %q, got %q", tt.wantSessionToken, credentials.SessionToken)
			}
		})
	}
}

func TestBuildS3ClientDiscoversBucketRegionWhenS3RegionIsOmitted(t *testing.T) {
	previous := discoverS3BucketRegion
	t.Cleanup(func() { discoverS3BucketRegion = previous })
	discoverS3BucketRegion = func(_ context.Context, _ *s3.Client, bucket string) (string, error) {
		assert.Equal(t, "event-media", bucket)
		return "us-east-2", nil
	}

	client, region, err := BuildS3Client(context.Background(), &models.Config{
		AwsRegion:     "us-east-1",
		AwsBucketName: "event-media",
	})
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "us-east-2", region)
}

func TestBuildS3ClientCorrectsExplicitS3RegionFromBucketDiscovery(t *testing.T) {
	previous := discoverS3BucketRegion
	t.Cleanup(func() { discoverS3BucketRegion = previous })
	discoverS3BucketRegion = func(_ context.Context, _ *s3.Client, bucket string) (string, error) {
		assert.Equal(t, "event-media", bucket)
		return "us-east-2", nil
	}

	_, region, err := BuildS3Client(context.Background(), &models.Config{
		AwsRegion:     "us-east-1",
		S3Region:      "us-west-2",
		AwsBucketName: "event-media",
	})
	require.NoError(t, err)
	assert.Equal(t, "us-east-2", region)
}

func TestBuildS3ClientForBucketDiscoversTheTargetBucketRegion(t *testing.T) {
	previous := discoverS3BucketRegion
	t.Cleanup(func() { discoverS3BucketRegion = previous })
	seen := ""
	discoverS3BucketRegion = func(_ context.Context, _ *s3.Client, bucket string) (string, error) {
		seen = bucket
		return "us-east-2", nil
	}
	_, region, err := BuildS3ClientForBucket(context.Background(), &models.Config{
		AwsRegion:     "us-east-1",
		AwsBucketName: "primary-media-bucket",
	}, "itbem-ai-outputs-prod-752279076974-us-east-2")
	if err != nil {
		t.Fatalf("BuildS3ClientForBucket returned error: %v", err)
	}
	if seen != "itbem-ai-outputs-prod-752279076974-us-east-2" || region != "us-east-2" {
		t.Fatalf("target bucket region = (%q, %q), want automation bucket in us-east-2", seen, region)
	}
}

func TestBuildS3ClientForWorkloadIdentityBucketIgnoresLegacyStaticCredentials(t *testing.T) {
	previous := discoverS3BucketRegion
	t.Cleanup(func() { discoverS3BucketRegion = previous })
	discoverS3BucketRegion = func(_ context.Context, _ *s3.Client, bucket string) (string, error) {
		if bucket != "itbem-ai-outputs-prod-752279076974-us-east-2" {
			t.Fatalf("unexpected bucket discovery for %q", bucket)
		}
		return "us-east-2", nil
	}

	// Environment credentials are intentionally present. Gateway storage must
	// select the EC2 workload provider instead of either environment or legacy
	// configuration credentials.
	t.Setenv("AWS_ACCESS_KEY_ID", "workload-identity-test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "workload-identity-test-secret-key")
	cfg := &models.Config{
		AwsRegion:      "us-east-1",
		S3ClientId:     "legacy-media-access-key",
		S3ClientSecret: "legacy-media-secret-key",
		AwsBucketName:  "primary-media-bucket",
	}
	if scoped := workloadIdentityS3Config(cfg); scoped == cfg || scoped.S3ClientId != "" || scoped.S3ClientSecret != "" || cfg.S3ClientId == "" || cfg.S3ClientSecret == "" {
		t.Fatal("workload identity configuration must clear only the copied legacy S3 credentials")
	}
	client, _, err := BuildS3ClientForWorkloadIdentityBucket(context.Background(), cfg, "itbem-ai-outputs-prod-752279076974-us-east-2")
	if err != nil || client == nil {
		t.Fatalf("workload identity bucket client = (%v, %v), want client without legacy credentials", client, err)
	}
}

func TestLoadEC2InstanceRoleConfigOverridesAmbientEnvironmentCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret-key")

	previous := newEC2WorkloadCredentials
	t.Cleanup(func() { newEC2WorkloadCredentials = previous })
	newEC2WorkloadCredentials = func(aws.Config) aws.CredentialsProvider {
		return credentials.NewStaticCredentialsProvider("instance-profile-access-key", "instance-profile-secret-key", "")
	}

	cfg, err := LoadEC2InstanceRoleConfig(context.Background(), "us-east-2", "legacy-access-key", "legacy-secret-key")
	require.NoError(t, err)
	resolved, err := cfg.Credentials.Retrieve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "instance-profile-access-key", resolved.AccessKeyID)
}

func TestBuildS3ClientSkipsAWSDiscoveryForCustomEndpoint(t *testing.T) {
	previous := discoverS3BucketRegion
	t.Cleanup(func() { discoverS3BucketRegion = previous })
	discoverS3BucketRegion = func(context.Context, *s3.Client, string) (string, error) {
		t.Fatal("AWS bucket discovery should not run for custom endpoints")
		return "", nil
	}

	client, region, err := BuildS3Client(context.Background(), &models.Config{
		AwsRegion:      "us-east-1",
		AwsBucketName:  "event-media",
		S3Endpoint:     "http://localhost:4566",
		S3UsePathStyle: "true",
	})
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "us-east-1", region)
}

func TestBuildS3ClientSurfacesDiscoveryFailure(t *testing.T) {
	previous := discoverS3BucketRegion
	t.Cleanup(func() { discoverS3BucketRegion = previous })
	discoverS3BucketRegion = func(context.Context, *s3.Client, string) (string, error) {
		return "", errors.New("bucket not found")
	}

	_, _, err := BuildS3Client(context.Background(), &models.Config{AwsRegion: "us-east-1", AwsBucketName: "missing"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discover S3 bucket region")
}

func TestBuildS3ClientFallsBackToExplicitRegionWhenDiscoveryIsForbidden(t *testing.T) {
	previous := discoverS3BucketRegion
	t.Cleanup(func() { discoverS3BucketRegion = previous })
	discoverS3BucketRegion = func(context.Context, *s3.Client, string) (string, error) {
		return "", errors.New("access denied")
	}

	client, region, err := BuildS3Client(context.Background(), &models.Config{
		AwsRegion:     "us-east-1",
		S3Region:      "us-west-2",
		AwsBucketName: "restricted",
	})
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "us-west-2", region)
}
