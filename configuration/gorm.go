package configuration

import (
	"events-stocks/models"
	"fmt"
	"log"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB es la instancia global de GORM
var DB *gorm.DB

// InicializarPostgreSQL inicializa la conexión con PostgreSQL usando GORM
func InicializarPostgreSQL(cfg *models.Config) {
	// Usa las variables del cfg
	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s TimeZone=%s",
		cfg.DbHost,
		cfg.DbUser,
		cfg.DbPassword,
		cfg.DbName,
		cfg.DbPort,
		cfg.DbTimezone,
	)

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		log.Fatalf("Error al conectar con PostgreSQL (gorm.Open): %v", err)
	}

	sqlDB, err := DB.DB()
	if err != nil {
		log.Fatalf("Error al obtener instancia *sql.DB desde GORM: %v", err)
	}

	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("Error al hacer ping a PostgreSQL: %v", err)
	}

	log.Println("Conectado a PostgreSQL con GORM")
}

func MigrarModelos() {
	err := DB.AutoMigrate(
		&models.Event{},
		// Agrega más modelos aquí
	)
	if err != nil {
		log.Fatalf("Error al migrar modelos: %v", err)
	}

	log.Println("Migración completada")
}
