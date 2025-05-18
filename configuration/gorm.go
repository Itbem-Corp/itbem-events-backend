package configuration

import (
	"events-stocks/models"
	"events-stocks/seeds"
	"fmt"
	"log"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB es la instancia global de GORM
var DB *gorm.DB

type ModelSeed struct {
	Model    interface{}
	SeedFunc func(*gorm.DB) // nil si no hay seed
}

var modelsWithoutSeed = []interface{}{
	&models.Event{},
	&models.Invitation{},
	&models.Moment{},
	&models.EventConfig{},
	&models.DesignTemplate{},
	&models.Color{},
	&models.ColorPalette{},
	&models.ColorPalettePattern{},
	&models.Font{},
	&models.FontSet{},
	&models.FontSetPattern{},
	&models.Guest{},
	&models.Resource{},
	&models.EventSection{},
	&models.InvitationLog{},
	&models.InvitationAccessToken{},
	&models.EventAnalytics{},
}

var modelSeedList = []ModelSeed{
	{Model: &models.EventType{}, SeedFunc: seeds.SeedEventType},
	{Model: &models.MomentType{}, SeedFunc: seeds.SeedMomentType},
	{Model: &models.GuestStatus{}, SeedFunc: seeds.SeedGuestStatus},
	{Model: &models.ResourceType{}, SeedFunc: seeds.SeedResourceTypes},
}

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

func GetAllModels() []interface{} {
	models := make([]interface{}, 0, len(modelSeedList)+len(modelsWithoutSeed))

	for _, s := range modelSeedList {
		models = append(models, s.Model)
	}

	models = append(models, modelsWithoutSeed...)
	return models
}

func MigrarModelos() {
	if err := DB.AutoMigrate(GetAllModels()...); err != nil {
		log.Fatalf("Error al migrar modelos: %v", err)
	}
	log.Println("Migración completada")
}

func SeedBaseData() {
	for _, item := range modelSeedList {
		if item.SeedFunc != nil && isModelEmpty(DB, item.Model) {
			item.SeedFunc(DB)
		}
	}
}

func isModelEmpty(db *gorm.DB, model interface{}) bool {
	var count int64
	db.Model(model).Count(&count)
	return count == 0
}
