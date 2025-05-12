package configuration

import (
	"context"
	"github.com/joho/godotenv"
	"log"
	"os"
	"reflect"
	"strings"

	"events-stocks/models"
)

func LoadConfig() *models.Config {
	wd, _ := os.Getwd()
	log.Printf("Current working directory: %s", wd)

	if os.Getenv("ENV") == "" {
		if err := godotenv.Load("./.env"); err != nil {
			log.Println("No se pudo cargar el archivo .env o no existe")
		}
	} else {

	}

	cfg := &models.Config{}
	v := reflect.ValueOf(cfg).Elem()
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		envName := fieldToEnvVar(field.Name)
		envValue, exists := os.LookupEnv(envName)

		if !exists && field.Tag.Get("required") == "true" {
			log.Fatalf("Missing required environment variable: %s", envName)
		}

		v.Field(i).SetString(envValue)
	}

	return cfg
}

// Convierte CamelCase -> UPPER_SNAKE_CASE (ej. CognitoClientID -> COGNITO_CLIENT_ID)
func fieldToEnvVar(fieldName string) string {
	var env string
	for i, c := range fieldName {
		if i > 0 && c >= 'A' && c <= 'Z' {
			env += "_"
		}
		env += string(c)
	}
	return strings.ToUpper(env)
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
		panic("Config not found in context")
	}
	return cfg
}
