package configuration

import (
	"reflect"
	"testing"

	"events-stocks/models"
)

func TestFieldToEnvVarHandlesAcronyms(t *testing.T) {
	tests := map[string]string{
		"AwsRegion":                 "AWS_REGION",
		"CognitoClientId":           "COGNITO_CLIENT_ID",
		"S3ClientId":                "S3_CLIENT_ID",
		"S3Region":                  "S3_REGION",
		"S3Endpoint":                "S3_ENDPOINT",
		"S3UsePathStyle":            "S3_USE_PATH_STYLE",
		"CDNBaseURL":                "CDN_BASE_URL",
		"CorsAllowOrigins":          "CORS_ALLOW_ORIGINS",
		"SQSImageQueueURL":          "SQS_IMAGE_QUEUE_URL",
		"SQSVideoQueueURL":          "SQS_VIDEO_QUEUE_URL",
		"SQSWorkerQueueURL":         "SQS_WORKER_QUEUE_URL",
		"SNSWorkerTopicARN":         "SNS_WORKER_TOPIC_ARN",
		"InternalAPISecret":         "INTERNAL_API_SECRET",
		"InternalAPISecretPrevious": "INTERNAL_API_SECRET_PREVIOUS",
		"DbLogLevel":                "DB_LOG_LEVEL",
	}

	for field, expected := range tests {
		t.Run(field, func(t *testing.T) {
			if got := fieldToEnvVar(field); got != expected {
				t.Fatalf("expected %s, got %s", expected, got)
			}
		})
	}
}

func TestLegacyAWSCredentialFieldsAreOptional(t *testing.T) {
	configType := reflect.TypeOf(models.Config{})
	for _, fieldName := range []string{
		"CognitoClientId",
		"CognitoClientSecret",
		"S3ClientId",
		"S3ClientSecret",
		"S3Region",
		"S3Endpoint",
		"S3UsePathStyle",
		"CDNBaseURL",
	} {
		field, ok := configType.FieldByName(fieldName)
		if !ok {
			t.Fatalf("models.Config field %s not found", fieldName)
		}
		if got := field.Tag.Get("required"); got != "false" {
			t.Errorf("expected %s to be optional, got required=%q", fieldName, got)
		}
	}
}
