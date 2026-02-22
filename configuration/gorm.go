package configuration

import (
	"events-stocks/models"
	"events-stocks/seeds"
	"fmt"
	"log/slog"
	"os"
	"time"

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
	&models.User{},
	&models.EventMember{},
	&models.ClientMember{},
}

var modelSeedList = []ModelSeed{
	{Model: &models.EventType{}, SeedFunc: seeds.SeedEventType},
	{Model: &models.MomentType{}, SeedFunc: seeds.SeedMomentType},
	{Model: &models.GuestStatus{}, SeedFunc: seeds.SeedGuestStatus},
	{Model: &models.ResourceType{}, SeedFunc: seeds.SeedResourceTypes},
	{Model: &models.ClientType{}, SeedFunc: seeds.SeedClientTypes},
	{Model: &models.ClientRole{}, SeedFunc: seeds.SeedClientRoles},
	{Model: &models.Client{}, SeedFunc: seeds.SeedClientEventiAppSeed},
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
		slog.Error("postgresql open failed", "error", err)
		os.Exit(1)
	}

	sqlDB, err := DB.DB()
	if err != nil {
		slog.Error("postgresql sql.DB failed", "error", err)
		os.Exit(1)
	}

	if err := sqlDB.Ping(); err != nil {
		slog.Error("postgresql ping failed", "error", err)
		os.Exit(1)
	}

	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetMaxIdleConns(15)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	slog.Info("postgresql connected")
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
	// Ensure uuid-ossp extension exists for uuid_generate_v4()
	DB.Exec("CREATE EXTENSION IF NOT EXISTS \"uuid-ossp\"")
	if err := DB.AutoMigrate(GetAllModels()...); err != nil {
		slog.Error("model migration failed", "error", err)
		os.Exit(1)
	}
	// Allow invitation_id to be NULL (needed for shared QR uploads without a personal token)
	DB.Exec("ALTER TABLE IF EXISTS moments ALTER COLUMN invitation_id DROP NOT NULL")
	slog.Info("models migrated")
}

func SeedBaseData() {
	for _, item := range modelSeedList {
		if item.SeedFunc != nil && isModelEmpty(DB, item.Model) {
			item.SeedFunc(DB)
		}
	}
	// SDUI: always run — idempotent, only updates sections with empty component_type
	seeds.SeedEventSectionSDUI(DB)
}

func isModelEmpty(db *gorm.DB, model interface{}) bool {
	var count int64
	db.Model(model).Count(&count)
	return count == 0
}
