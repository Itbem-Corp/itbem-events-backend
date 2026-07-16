package configuration

import (
	"events-stocks/models"
	"events-stocks/seeds"
	"fmt"
	"log/slog"
	"os"
	"strings"
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
	&models.EventTable{},
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
	&models.EventAnalyticsRollup{},
	&models.EventPerformanceDaily{},
	&models.EventPerformanceBucketDaily{},
	&models.PublicPerformanceWindowBucket{},
	&models.User{},
	&models.EventMember{},
	&models.ClientMember{},
	&models.Application{},
	&models.ClientApplication{},
	&models.ClientMemberApplication{},
	&models.AuditLog{},
	&models.ProductMetricDaily{},
	&models.ProductActiveUserDaily{},
	&models.IdempotencyRecord{},
	&models.OutboxEvent{},
	&models.EventPhrase{},
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
	dbLogLevel, validLogLevel := databaseLogLevel(cfg.DbLogLevel, os.Getenv("ENV"))
	if !validLogLevel {
		slog.Warn("invalid DB_LOG_LEVEL; using environment default", "value", cfg.DbLogLevel)
	}
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(dbLogLevel),
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

// databaseLogLevel keeps verbose SQL tracing available during local
// development without paying its formatting and output cost in production.
// Warn mode still reports slow queries and database errors.
func databaseLogLevel(configured, environment string) (logger.LogLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "silent":
		return logger.Silent, true
	case "error":
		return logger.Error, true
	case "warn", "warning":
		return logger.Warn, true
	case "info":
		return logger.Info, true
	case "":
		if strings.TrimSpace(environment) == "" {
			return logger.Info, true
		}
		return logger.Warn, true
	default:
		if strings.TrimSpace(environment) == "" {
			return logger.Info, false
		}
		return logger.Warn, false
	}
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
	if err := migrateModels(DB); err != nil {
		slog.Error("model migration failed", "error", err)
		os.Exit(1)
	}
	slog.Info("models migrated")
}

// migrateModels makes startup DDL atomic and bounded. The advisory transaction
// lock prevents two candidate deployments from migrating the same schema at
// once, while lock_timeout ensures a busy production table fails the candidate
// instead of stalling the active service. A failed transaction leaves the
// previous schema intact for the still-running backend.
func migrateModels(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		statements := []string{
			"SET LOCAL lock_timeout = '5s'",
			"SET LOCAL statement_timeout = '120s'",
			"SELECT pg_advisory_xact_lock(hashtext('eventiapp-schema-migration'))",
			"CREATE EXTENSION IF NOT EXISTS \"uuid-ossp\"",
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("migration preflight %q: %w", statement, err)
			}
		}

		if err := tx.AutoMigrate(GetAllModels()...); err != nil {
			return fmt.Errorf("auto migrate: %w", err)
		}
		// Early worker prototypes created this table and let Postgres choose the
		// foreign-key name. The backend now owns the shared schema and GORM creates
		// fk_event_analytics_rollups_event with the intended update/delete policy.
		// Remove only the redundant legacy constraint after AutoMigrate has ensured
		// the canonical one exists; the surrounding transaction keeps this atomic.
		if err := tx.Exec("ALTER TABLE IF EXISTS event_analytics_rollups DROP CONSTRAINT IF EXISTS event_analytics_rollups_event_id_fkey").Error; err != nil {
			return fmt.Errorf("remove legacy analytics rollup constraint: %w", err)
		}
		// Allow invitation_id to be NULL (needed for shared QR uploads without a personal token).
		if err := tx.Exec("ALTER TABLE IF EXISTS moments ALTER COLUMN invitation_id DROP NOT NULL").Error; err != nil {
			return fmt.Errorf("relax moments invitation constraint: %w", err)
		}
		// Security audit rows are append-only. Even application code running
		// with the normal database owner cannot rewrite or delete history
		// accidentally; retention must use an explicit privileged migration.
		auditStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_audit_log_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'audit_logs are append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS audit_logs_append_only ON audit_logs",
			`CREATE TRIGGER audit_logs_append_only
			 BEFORE UPDATE OR DELETE ON audit_logs
			 FOR EACH ROW EXECUTE FUNCTION prevent_audit_log_mutation()`,
		}
		for _, statement := range auditStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect audit log: %w", err)
			}
		}
		return nil
	})
}

func SeedBaseData() {
	for _, item := range modelSeedList {
		if item.SeedFunc != nil && isModelEmpty(DB, item.Model) {
			item.SeedFunc(DB)
		}
	}
	// First-party tenant definitions are additive and must also be reconciled on
	// established databases where the clients table is already populated.
	seeds.SeedClientEventiAppSeed(DB)
	// Authorization roles are policy, not optional catalog data. This seed is
	// idempotent and must run for existing installations as new roles are added.
	seeds.SeedClientRoles(DB)
	// Applications and their memberships are an authorization boundary. The
	// seed is additive and backfills existing organization memberships.
	if err := seeds.SeedApplications(DB); err != nil {
		slog.Error("required application catalog seed failed", "error", err)
		os.Exit(1)
	}
	// Versioned product catalogs use stable IDs and preserve custom entries.
	if err := seeds.SeedDesignCatalog(DB); err != nil {
		slog.Error("required design catalog seed failed", "error", err)
		os.Exit(1)
	}
	// Phrase publication is additive and must run even when production already
	// contains rows. Gating it behind isModelEmpty would leave a partially
	// published corpus incomplete forever.
	if err := seeds.SeedEventPhrases(DB); err != nil {
		slog.Error("required event phrase seed failed", "error", err)
		os.Exit(1)
	}
	// SDUI: always run — idempotent, only updates sections with empty component_type
	seeds.SeedEventSectionSDUI(DB)
}

func isModelEmpty(db *gorm.DB, model interface{}) bool {
	var count int64
	db.Model(model).Count(&count)
	return count == 0
}
