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
	for i, r := range fieldName {
		// Si es mayúscula y no es el primer carácter
		if i > 0 && r >= 'A' && r <= 'Z' {
			// Solo añade "_" si el carácter anterior era minúscula
			// Esto evita separar A_W_S
			prev := fieldName[i-1]
			if !(prev >= 'A' && prev <= 'Z') {
				env.WriteRune('_')
			}
		}
		env.WriteRune(r)
	}
	return strings.ToUpper(env.String())
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
