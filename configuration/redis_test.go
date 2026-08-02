package configuration

import "testing"

func TestValidateRedisAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
		valid   bool
	}{
		{name: "hostname with port", address: "master.example.cache.amazonaws.com:6379", valid: true},
		{name: "ipv6 with port", address: "[::1]:6379", valid: true},
		{name: "missing port", address: "master.example.cache.amazonaws.com", valid: false},
		{name: "empty host", address: ":6379", valid: false},
		{name: "out of range port", address: "cache.example.com:65536", valid: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRedisAddress(tc.address)
			if tc.valid && err != nil {
				t.Fatalf("expected %q to be valid: %v", tc.address, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("expected %q to be invalid", tc.address)
			}
		})
	}
}
