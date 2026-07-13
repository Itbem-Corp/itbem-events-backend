package configuration

import (
	"testing"

	"gorm.io/gorm/logger"
)

func TestDatabaseLogLevel(t *testing.T) {
	tests := []struct {
		name        string
		configured  string
		environment string
		want        logger.LogLevel
		valid       bool
	}{
		{name: "local default stays verbose", want: logger.Info, valid: true},
		{name: "production default avoids per-query info logs", environment: "production", want: logger.Warn, valid: true},
		{name: "explicit silent", configured: "silent", environment: "production", want: logger.Silent, valid: true},
		{name: "explicit error", configured: " ERROR ", environment: "production", want: logger.Error, valid: true},
		{name: "explicit info in production", configured: "info", environment: "production", want: logger.Info, valid: true},
		{name: "warning alias", configured: "warning", want: logger.Warn, valid: true},
		{name: "invalid local falls back safely", configured: "trace", want: logger.Info, valid: false},
		{name: "invalid production falls back safely", configured: "trace", environment: "production", want: logger.Warn, valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, valid := databaseLogLevel(tt.configured, tt.environment)
			if got != tt.want || valid != tt.valid {
				t.Fatalf("databaseLogLevel(%q, %q) = (%v, %v), want (%v, %v)", tt.configured, tt.environment, got, valid, tt.want, tt.valid)
			}
		})
	}
}
