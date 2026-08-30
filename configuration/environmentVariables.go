package configuration

import (
	"context"
	"fmt"
	"github.com/joho/godotenv"
	"log/slog"
	"os"
	"reflect"
	"strconv"
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
		envName := configFieldEnvVar(field)
		envValue, exists := os.LookupEnv(envName)

		if exists && strings.ToLower(envValue) == "none" {
			envValue = ""
		}

		if !exists && field.Tag.Get("required") == "true" {
			slog.Error("missing required environment variable", "name", envName)
			os.Exit(1)
		}

		if err := setConfigField(v.Field(i), envName, envValue); err != nil {
			slog.Error("invalid environment variable", "name", envName, "error", err)
			os.Exit(1)
		}
	}

	return cfg
}

func configFieldEnvVar(field reflect.StructField) string {
	if explicit := strings.TrimSpace(field.Tag.Get("env")); explicit != "" {
		return explicit
	}
	return fieldToEnvVar(field.Name)
}

// setConfigField centralizes the reflection boundary used by LoadConfig. Most
// configuration remains string-backed for backwards compatibility, but numeric
// reserves are intentionally typed so an environment such as PORT cannot
// crash startup merely because a later Config field is an int.
func setConfigField(field reflect.Value, envName, raw string) error {
	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if strings.TrimSpace(raw) == "" {
			field.SetInt(0)
			return nil
		}
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("must be an integer: %w", err)
		}
		field.SetInt(value)
		return nil
	case reflect.Bool:
		if strings.TrimSpace(raw) == "" {
			field.SetBool(false)
			return nil
		}
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("must be a boolean: %w", err)
		}
		field.SetBool(value)
		return nil
	default:
		return fmt.Errorf("unsupported configuration field type %s", field.Type())
	}
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
