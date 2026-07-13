package configuration

import (
	"context"
	"github.com/joho/godotenv"
	"log/slog"
	"os"
	"reflect"
	"strings"

	"events-stocks/models"
)

func LoadConfig() *models.Config {
	wd, _ := os.Getwd()
	slog.Info("loading config", "cwd", wd)

	if os.Getenv("ENV") == "" {
		if err := godotenv.Load("./.env"); err != nil {
			slog.Warn(".env file not found or could not be loaded")
		}
	}

	cfg := &models.Config{}
	v := reflect.ValueOf(cfg).Elem()
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		envName := fieldToEnvVar(field.Name)
		envValue, exists := os.LookupEnv(envName)

		if exists && strings.ToLower(envValue) == "none" {
			envValue = ""
		}

		if !exists && field.Tag.Get("required") == "true" {
			slog.Error("missing required environment variable", "name", envName)
			os.Exit(1)
		}

		v.Field(i).SetString(envValue)
	}

	return cfg
}

// Convierte CamelCase -> UPPER_SNAKE_CASE (ej. CognitoClientID -> COGNITO_CLIENT_ID)
func fieldToEnvVar(fieldName string) string {
	var env strings.Builder
	runes := []rune(fieldName)
	for i, r := range runes {
		if i > 0 && isUpperASCII(r) {
			prev := runes[i-1]
			var next rune
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			if !isUpperASCII(prev) || isLowerASCII(next) {
				env.WriteRune('_')
			}
		}
		env.WriteRune(r)
	}
	return strings.ToUpper(env.String())
}

func isUpperASCII(r rune) bool {
	return r >= 'A' && r <= 'Z'
}

func isLowerASCII(r rune) bool {
	return r >= 'a' && r <= 'z'
}

// Context key
type contextKey string

const configKey = contextKey("appConfig")

func WithConfig(ctx context.Context, cfg *models.Config) context.Context {
	return context.WithValue(ctx, configKey, cfg)
}

func FromContext(ctx context.Context) *models.Config {
	cfg, ok := ctx.Value(configKey).(*models.Config)
	if !ok {
		slog.Warn("config not found in context")
		return nil
	}
	return cfg
}
