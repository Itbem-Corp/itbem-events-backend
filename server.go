package main

import (
	"context"
	"events-stocks/configuration"
	"events-stocks/controllers/resources"
	"events-stocks/middleware/token"
	"events-stocks/routes"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"log"
	"time"
)

func main() {

	// Cargar configuración
	cfg := configuration.LoadConfig()

	// Inicializar Redis
	configuration.InicializarRedis(cfg)

	// Inicializar PostgreSQL
	configuration.InicializarPostgreSQL(cfg)

	// Ejecutar migraciones automáticas
	configuration.MigrarModelos()

	// Ejecutar Seeds Automaticos
	configuration.SeedBaseData()

	// Ejecutar Cloud Services AWS
	configuration.InitAwsServices(cfg)

	// Crear instancia de Echo
	e := echo.New()

	// Middlewares básicos
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())
	e.Use(token.Autenticacion(cfg))
	resources.InitResourceController(cfg)
	// Configurar rutas
	routes.ConfigurarRutas(e, cfg)

	// Prueba de Redis
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := configuration.RedisClient.Set(ctx, "test_key", "Hello Redis!", 10*time.Second).Err()
	if err != nil {
		log.Fatalf("Error al escribir en Redis: %v", err)
	}

	// Iniciar el servidor
	log.Println("Servidor iniciado en el puerto 8080")
	if err := e.Start(":8080"); err != nil {
		log.Fatalf("Error al iniciar el servidor: %v", err)
	}
}
