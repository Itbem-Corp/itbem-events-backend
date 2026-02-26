package awsrepository_test

import (
	"os"
	"testing"
	"events-stocks/repositories/awsrepository"
)

func TestRewriteToCDN(t *testing.T) {
	tests := []struct {
		name     string
		cdnBase  string
		input    string
		expected string
	}{
		{
			name:     "no CDN set returns original",
			cdnBase:  "",
			input:    "https://itbem-events-bucket-prod.s3.amazonaws.com/moments/123/raw/abc.mp4",
			expected: "https://itbem-events-bucket-prod.s3.amazonaws.com/moments/123/raw/abc.mp4",
		},
		{
			name:     "virtual-hosted S3 URL rewritten",
			cdnBase:  "https://cdn.eventiapp.com.mx",
			input:    "https://itbem-events-bucket-prod.s3.amazonaws.com/moments/123/raw/abc.mp4",
			expected: "https://cdn.eventiapp.com.mx/moments/123/raw/abc.mp4",
		},
		{
			name:     "regional S3 URL rewritten",
			cdnBase:  "https://cdn.eventiapp.com.mx",
			input:    "https://itbem-events-bucket-prod.s3.us-east-2.amazonaws.com/moments/123/raw/photo.jpg",
			expected: "https://cdn.eventiapp.com.mx/moments/123/raw/photo.jpg",
		},
		{
			name:     "already CDN URL left unchanged",
			cdnBase:  "https://cdn.eventiapp.com.mx",
			input:    "https://cdn.eventiapp.com.mx/moments/123/raw/abc.jpg",
			expected: "https://cdn.eventiapp.com.mx/moments/123/raw/abc.jpg",
		},
		{
			name:     "empty URL returns empty",
			cdnBase:  "https://cdn.eventiapp.com.mx",
			input:    "",
			expected: "",
		},
		{
			name:     "path-style S3 URL rewritten without bucket prefix",
			cdnBase:  "https://cdn.eventiapp.com.mx",
			input:    "https://s3.amazonaws.com/itbem-events-bucket-prod/moments/123/raw/photo.jpg",
			expected: "https://cdn.eventiapp.com.mx/moments/123/raw/photo.jpg",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cdnBase != "" {
				os.Setenv("CDN_BASE_URL", tc.cdnBase)
				defer os.Unsetenv("CDN_BASE_URL")
			} else {
				os.Unsetenv("CDN_BASE_URL")
			}
			got := awsrepository.RewriteToCDN(tc.input)
			if got != tc.expected {
				t.Errorf("got %q, want %q", got, tc.expected)
			}
		})
	}
}
