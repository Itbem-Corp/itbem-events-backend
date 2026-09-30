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
	&models.UserApplicationPolicy{},
	&models.AuditLog{},
	&models.ProductMetricDaily{},
	&models.ProductActiveUserDaily{},
	&models.IdempotencyRecord{},
	&models.OutboxEvent{},
	&models.OutboxDispatchCursor{},
	&models.AutomationTask{},
	&models.AutomationModelEvaluation{},
	&models.AutomationModelEvaluationCall{},
	&models.AutomationTaskEvent{},
	&models.AutomationExecution{},
	&models.AutomationCodeReviewPublication{},
	&models.AutomationToolExecution{},
	&models.AutomationAgentProfile{},
	&models.AutomationAgentInstance{},
	&models.AutomationAgentCallbackNonce{},
	&models.AutomationAgentHeartbeat{},
	&models.AutomationAIActionPolicy{},
	&models.AutomationAIActionPolicyRevision{},
	&models.AutomationInferenceAttemptPolicy{},
	&models.AutomationInferenceReceipt{},
	&models.AutomationProviderModelSnapshot{},
	&models.AutomationProviderUsageSnapshot{},
	&models.DeliveryClientProfile{},
	&models.DeliveryProject{},
	&models.DeliveryProjectMember{},
	&models.DeliveryContextSource{},
	&models.DeliveryRepositoryOnboarding{},
	&models.DeliveryProjectVaultRevision{},
	&models.DeliveryRepositoryCapabilityProbe{},
	&models.DeliveryPolicyRevision{},
	&models.DeliveryPolicyDecision{},
	&models.DeliveryRequest{},
	&models.DeliveryDecomposition{},
	&models.DeliveryWorkItem{},
	&models.DeliveryAutomationSchedule{},
	&models.DeliveryAutomationScheduleOccurrence{},
	&models.DeliveryAutomationScheduleEvent{},
	&models.DeliveryEpic{},
	&models.DeliveryEpicWorkItem{},
	&models.DeliveryContinuation{},
	&models.DeliveryWorkItemDependency{},
	&models.DeliveryContextSnapshot{},
	&models.DeliveryPlan{},
	&models.DeliveryPlanStep{},
	&models.DeliveryPlanStepDependency{},
	&models.DeliveryPlanStepEvent{},
	&models.DeliveryPlanStepActivityEvent{},
	&models.DeliveryPlanStepPatchArtifact{},
	&models.DeliveryPlanStepEvidence{},
	&models.DeliveryPlanStepDependencyPatch{},
	&models.DeliveryChangeSet{},
	&models.DeliveryPublicationGrant{},
	&models.DeliveryGate{},
	&models.DeliveryPlanExecution{},
	&models.DeliveryPlanStepAssignment{},
	&models.DeliveryPlanStepAssignmentEvent{},
	&models.DeliveryEvidence{},
	&models.DeliveryEvent{},
	&models.DeliveryMessage{},
	&models.DeliveryRelease{},
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

// MigrateModelsForTest runs the same ordered, transactional migration used by
// the service without terminating the process. Integration suites use this on
// a fresh database so schema bootstrap is tested as an actual startup path
// rather than through GORM's variadic AutoMigrate (which can resolve
// cross-model associations in an unsafe order).
func MigrateModelsForTest(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	return migrateModels(db)
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

		// Run models one by one so a failed deployment names the exact schema
		// owner. This retains the surrounding all-or-nothing transaction while
		// making local and production migration failures diagnosable.
		for _, model := range GetAllModels() {
			if err := tx.AutoMigrate(model); err != nil {
				return fmt.Errorf("auto migrate %T: %w", model, err)
			}
		}
		// Existing step rows are deliberately classified as implementation, not
		// integration. Old approved plans therefore remain frozen but fail the
		// new execution-graph validation until a human creates and approves a new
		// version with an explicit integration node.
		if err := tx.Exec(`UPDATE delivery_plan_steps SET role = ? WHERE role IS NULL OR role = ''`, models.DeliveryPlanStepRoleImplementation).Error; err != nil {
			return fmt.Errorf("backfill delivery plan step role: %w", err)
		}
		// Capture task lifecycle and worker reassignment changes at the database
		// boundary. This covers conditional Updates/map writes and future writers
		// that do not pass through a particular controller helper.
		taskEventStatements := []string{
			`CREATE OR REPLACE FUNCTION capture_automation_task_event()
			 RETURNS trigger AS $$
			 DECLARE
			   event_type varchar(32);
			   event_sequence bigint;
			   previous_status varchar(16) := '';
			   previous_run_id varchar(64) := '';
			   previous_worker_id varchar(64) := '';
			   previous_agent_key varchar(64) := '';
			   previous_machine_id varchar(64) := '';
			   previous_agent_instance_id uuid := NULL;
			 BEGIN
			   IF TG_OP = 'UPDATE' THEN
			     previous_status := COALESCE(OLD.status, '');
			     previous_run_id := COALESCE(OLD.run_id, '');
			     previous_worker_id := COALESCE(OLD.worker_id, '');
			     previous_agent_key := COALESCE(OLD.agent_key, '');
			     previous_machine_id := COALESCE(OLD.machine_id, '');
			     previous_agent_instance_id := OLD.agent_instance_id;
			     IF NEW.status IS DISTINCT FROM OLD.status THEN
			       IF NEW.status = 'running' THEN
			         event_type := 'claimed';
			       ELSE
			         event_type := 'status_transition';
			       END IF;
			     ELSIF NEW.run_id IS DISTINCT FROM OLD.run_id AND NEW.status = 'running' THEN
			       event_type := 'lease_reclaimed';
			     ELSIF NEW.worker_id IS DISTINCT FROM OLD.worker_id
			        OR NEW.agent_key IS DISTINCT FROM OLD.agent_key
			        OR NEW.machine_id IS DISTINCT FROM OLD.machine_id
			        OR NEW.agent_instance_id IS DISTINCT FROM OLD.agent_instance_id THEN
			       event_type := 'assignment_changed';
			     ELSIF NEW.attempt_count IS DISTINCT FROM OLD.attempt_count THEN
			       event_type := 'attempt_updated';
			     ELSE
			       RETURN NEW;
			     END IF;
			   ELSE
			     event_type := 'created';
			   END IF;

			   SELECT COALESCE(MAX(sequence), 0) + 1 INTO event_sequence
			     FROM automation_task_events WHERE automation_task_id = NEW.id;
			   INSERT INTO automation_task_events (
			     id, automation_task_id, sequence, event_type, previous_status, status,
			     previous_run_id, run_id, previous_worker_id, worker_id,
			     previous_agent_key, agent_key, previous_machine_id, machine_id,
			     previous_agent_instance_id, agent_instance_id, attempt_count,
			     occurred_at, created_at
			   ) VALUES (
			     uuid_generate_v4(), NEW.id, event_sequence, event_type, previous_status, COALESCE(NEW.status, ''),
			     previous_run_id, COALESCE(NEW.run_id, ''), previous_worker_id, COALESCE(NEW.worker_id, ''),
			     previous_agent_key, COALESCE(NEW.agent_key, ''), previous_machine_id, COALESCE(NEW.machine_id, ''),
			     previous_agent_instance_id, NEW.agent_instance_id, COALESCE(NEW.attempt_count, 0),
			     COALESCE(NEW.updated_at, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP
			   );
			   RETURN NEW;
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS automation_tasks_capture_lifecycle ON automation_tasks",
			`CREATE TRIGGER automation_tasks_capture_lifecycle
			 AFTER INSERT OR UPDATE ON automation_tasks
			 FOR EACH ROW EXECUTE FUNCTION capture_automation_task_event()`,
			`CREATE OR REPLACE FUNCTION prevent_automation_task_event_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'automation task events are append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS automation_task_events_append_only ON automation_task_events",
			`CREATE TRIGGER automation_task_events_append_only
			 BEFORE UPDATE OR DELETE ON automation_task_events
			 FOR EACH ROW EXECUTE FUNCTION prevent_automation_task_event_mutation()`,
			"DROP TRIGGER IF EXISTS automation_task_events_no_truncate ON automation_task_events",
			`CREATE TRIGGER automation_task_events_no_truncate
			 BEFORE TRUNCATE ON automation_task_events
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_automation_task_event_mutation()`,
		}
		for _, statement := range taskEventStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect and record automation task events: %w", err)
			}
		}
		if err := migrateDeliveryEpicMembershipIntegrity(tx); err != nil {
			return fmt.Errorf("migrate delivery epic membership integrity: %w", err)
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
		// The original ledger deduplicated by an optional provider response ID.
		// Some providers legitimately omit that value, and a task can make more
		// than one billable call. AutoMigrate creates automation_execution_run;
		// remove the legacy index only after that replacement exists.
		if err := tx.Exec("DROP INDEX IF EXISTS automation_execution_response").Error; err != nil {
			return fmt.Errorf("remove legacy automation execution response index: %w", err)
		}
		// The first Stagehand ledger shape allowed only one tool call per run.
		// Keep the immutable ledger, but let a pinned tool account for each
		// individual provider call by replacing that old triplet key with the
		// AutoMigrate-created call-key index.
		if err := tx.Exec("DROP INDEX IF EXISTS automation_tool_execution_run").Error; err != nil {
			return fmt.Errorf("remove legacy automation tool execution index: %w", err)
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
			"DROP TRIGGER IF EXISTS audit_logs_no_truncate ON audit_logs",
			`CREATE TRIGGER audit_logs_no_truncate
			 BEFORE TRUNCATE ON audit_logs
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_audit_log_mutation()`,
		}
		for _, statement := range auditStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect audit log: %w", err)
			}
		}
		// Both provider ledgers are append-only financial records. A task may be
		// retried, but it may never rewrite the cost, usage, model, or private
		// evidence linkage of a completed primary or tool call.
		ledgerStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_automation_ledger_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'automation execution ledgers are append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS automation_executions_append_only ON automation_executions",
			`CREATE TRIGGER automation_executions_append_only
			 BEFORE UPDATE OR DELETE ON automation_executions
			 FOR EACH ROW EXECUTE FUNCTION prevent_automation_ledger_mutation()`,
			"DROP TRIGGER IF EXISTS automation_executions_no_truncate ON automation_executions",
			`CREATE TRIGGER automation_executions_no_truncate
			 BEFORE TRUNCATE ON automation_executions
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_automation_ledger_mutation()`,
			"DROP TRIGGER IF EXISTS automation_tool_executions_append_only ON automation_tool_executions",
			`CREATE TRIGGER automation_tool_executions_append_only
			 BEFORE UPDATE OR DELETE ON automation_tool_executions
			 FOR EACH ROW EXECUTE FUNCTION prevent_automation_ledger_mutation()`,
			"DROP TRIGGER IF EXISTS automation_tool_executions_no_truncate ON automation_tool_executions",
			`CREATE TRIGGER automation_tool_executions_no_truncate
			 BEFORE TRUNCATE ON automation_tool_executions
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_automation_ledger_mutation()`,
		}
		for _, statement := range ledgerStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect automation ledger: %w", err)
			}
		}
		providerUsageStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_automation_provider_usage_snapshot_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'automation provider usage snapshots are append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS automation_provider_usage_snapshots_append_only ON automation_provider_usage_snapshots",
			`CREATE TRIGGER automation_provider_usage_snapshots_append_only
			 BEFORE UPDATE OR DELETE ON automation_provider_usage_snapshots
			 FOR EACH ROW EXECUTE FUNCTION prevent_automation_provider_usage_snapshot_mutation()`,
			"DROP TRIGGER IF EXISTS automation_provider_usage_snapshots_no_truncate ON automation_provider_usage_snapshots",
			`CREATE TRIGGER automation_provider_usage_snapshots_no_truncate
			 BEFORE TRUNCATE ON automation_provider_usage_snapshots
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_automation_provider_usage_snapshot_mutation()`,
		}
		for _, statement := range providerUsageStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect automation provider usage snapshots: %w", err)
			}
		}
		inferenceReceiptStatements := []string{
			`CREATE OR REPLACE FUNCTION protect_automation_inference_receipt()
			 RETURNS trigger AS $$
			 BEGIN
			   IF TG_OP IN ('DELETE', 'TRUNCATE') THEN
			     RAISE EXCEPTION 'automation inference receipts are durable';
			   END IF;
			   IF OLD.status <> 'reserved'
			      OR NEW.id IS DISTINCT FROM OLD.id
			      OR NEW.automation_task_id IS DISTINCT FROM OLD.automation_task_id
			      OR NEW.run_id IS DISTINCT FROM OLD.run_id
			      OR NEW.call_id IS DISTINCT FROM OLD.call_id
			      OR NEW.plan_step_id IS DISTINCT FROM OLD.plan_step_id
			      OR NEW.operation IS DISTINCT FROM OLD.operation
			      OR NEW.worker_id IS DISTINCT FROM OLD.worker_id
			      OR NEW.agent_key IS DISTINCT FROM OLD.agent_key
			      OR NEW.machine_id IS DISTINCT FROM OLD.machine_id
			      OR NEW.policy_snapshot_hash IS DISTINCT FROM OLD.policy_snapshot_hash
			      OR NEW.quota_limit IS DISTINCT FROM OLD.quota_limit
			      OR NEW.created_at IS DISTINCT FROM OLD.created_at
			      OR NEW.status NOT IN ('accepted', 'rejected', 'ambiguous') THEN
			     RAISE EXCEPTION 'automation inference receipt transition is invalid';
			   END IF;
			   RETURN NEW;
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS automation_inference_receipts_durable ON automation_inference_receipts",
			`CREATE TRIGGER automation_inference_receipts_durable
			 BEFORE UPDATE OR DELETE ON automation_inference_receipts
			 FOR EACH ROW EXECUTE FUNCTION protect_automation_inference_receipt()`,
			"DROP TRIGGER IF EXISTS automation_inference_receipts_no_truncate ON automation_inference_receipts",
			`CREATE TRIGGER automation_inference_receipts_no_truncate
			 BEFORE TRUNCATE ON automation_inference_receipts
			 FOR EACH STATEMENT EXECUTE FUNCTION protect_automation_inference_receipt()`,
		}
		for _, statement := range inferenceReceiptStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect automation inference receipts: %w", err)
			}
		}
		stepEventStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_delivery_plan_step_event_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'delivery plan step events are append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_events_append_only ON delivery_plan_step_events",
			`CREATE TRIGGER delivery_plan_step_events_append_only
			 BEFORE UPDATE OR DELETE ON delivery_plan_step_events
			 FOR EACH ROW EXECUTE FUNCTION prevent_delivery_plan_step_event_mutation()`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_events_no_truncate ON delivery_plan_step_events",
			`CREATE TRIGGER delivery_plan_step_events_no_truncate
			 BEFORE TRUNCATE ON delivery_plan_step_events
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_delivery_plan_step_event_mutation()`,
		}
		for _, statement := range stepEventStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect delivery plan step events: %w", err)
			}
		}
		stepActivityStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_delivery_plan_step_activity_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'delivery plan step activity is append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_activity_events_append_only ON delivery_plan_step_activity_events",
			`CREATE TRIGGER delivery_plan_step_activity_events_append_only
			 BEFORE UPDATE OR DELETE ON delivery_plan_step_activity_events
			 FOR EACH ROW EXECUTE FUNCTION prevent_delivery_plan_step_activity_mutation()`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_activity_events_no_truncate ON delivery_plan_step_activity_events",
			`CREATE TRIGGER delivery_plan_step_activity_events_no_truncate
			 BEFORE TRUNCATE ON delivery_plan_step_activity_events
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_delivery_plan_step_activity_mutation()`,
		}
		stepPatchArtifactStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_delivery_plan_step_patch_artifact_mutation()
		 RETURNS trigger AS $$
		 BEGIN
		   RAISE EXCEPTION 'delivery plan step patch artifacts are append-only';
		 END;
		 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_patch_artifacts_append_only ON delivery_plan_step_patch_artifacts",
			`CREATE TRIGGER delivery_plan_step_patch_artifacts_append_only
		 BEFORE UPDATE OR DELETE ON delivery_plan_step_patch_artifacts
		 FOR EACH ROW EXECUTE FUNCTION prevent_delivery_plan_step_patch_artifact_mutation()`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_patch_artifacts_no_truncate ON delivery_plan_step_patch_artifacts",
			`CREATE TRIGGER delivery_plan_step_patch_artifacts_no_truncate
			 BEFORE TRUNCATE ON delivery_plan_step_patch_artifacts
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_delivery_plan_step_patch_artifact_mutation()`,
		}
		for _, statement := range stepPatchArtifactStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect delivery plan step patch artifacts: %w", err)
			}
		}
		stepEvidenceStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_delivery_plan_step_evidence_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'delivery plan step evidence is append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_evidence_append_only ON delivery_plan_step_evidences",
			`CREATE TRIGGER delivery_plan_step_evidence_append_only
			 BEFORE UPDATE OR DELETE ON delivery_plan_step_evidences
			 FOR EACH ROW EXECUTE FUNCTION prevent_delivery_plan_step_evidence_mutation()`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_evidences_no_truncate ON delivery_plan_step_evidences",
			`CREATE TRIGGER delivery_plan_step_evidences_no_truncate
			 BEFORE TRUNCATE ON delivery_plan_step_evidences
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_delivery_plan_step_evidence_mutation()`,
		}
		for _, statement := range stepEvidenceStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect delivery plan step evidence: %w", err)
			}
		}
		stepDependencyPatchStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_delivery_plan_step_dependency_patch_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'delivery plan step dependency patch receipts are append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_dependency_patches_append_only ON delivery_plan_step_dependency_patches",
			`CREATE TRIGGER delivery_plan_step_dependency_patches_append_only
			 BEFORE UPDATE OR DELETE ON delivery_plan_step_dependency_patches
			 FOR EACH ROW EXECUTE FUNCTION prevent_delivery_plan_step_dependency_patch_mutation()`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_dependency_patches_no_truncate ON delivery_plan_step_dependency_patches",
			`CREATE TRIGGER delivery_plan_step_dependency_patches_no_truncate
			 BEFORE TRUNCATE ON delivery_plan_step_dependency_patches
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_delivery_plan_step_dependency_patch_mutation()`,
		}
		for _, statement := range stepDependencyPatchStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect delivery plan step dependency patch receipts: %w", err)
			}
		}
		for _, statement := range stepActivityStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect delivery plan step activity: %w", err)
			}
		}
		policyRevisionStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_automation_ai_policy_revision_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'automation AI action policy revisions are append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS automation_ai_action_policy_revisions_append_only ON automation_ai_action_policy_revisions",
			`CREATE TRIGGER automation_ai_action_policy_revisions_append_only
			 BEFORE UPDATE OR DELETE ON automation_ai_action_policy_revisions
			 FOR EACH ROW EXECUTE FUNCTION prevent_automation_ai_policy_revision_mutation()`,
			"DROP TRIGGER IF EXISTS automation_ai_action_policy_revisions_no_truncate ON automation_ai_action_policy_revisions",
			`CREATE TRIGGER automation_ai_action_policy_revisions_no_truncate
			 BEFORE TRUNCATE ON automation_ai_action_policy_revisions
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_automation_ai_policy_revision_mutation()`,
		}
		for _, statement := range policyRevisionStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect automation AI policy revisions: %w", err)
			}
		}
		inferenceAttemptPolicyStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_automation_inference_attempt_policy_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'automation inference attempt policies are append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS automation_inference_attempt_policies_append_only ON automation_inference_attempt_policies",
			`CREATE TRIGGER automation_inference_attempt_policies_append_only
			 BEFORE UPDATE OR DELETE ON automation_inference_attempt_policies
			 FOR EACH ROW EXECUTE FUNCTION prevent_automation_inference_attempt_policy_mutation()`,
			"DROP TRIGGER IF EXISTS automation_inference_attempt_policies_no_truncate ON automation_inference_attempt_policies",
			`CREATE TRIGGER automation_inference_attempt_policies_no_truncate
			 BEFORE TRUNCATE ON automation_inference_attempt_policies
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_automation_inference_attempt_policy_mutation()`,
		}
		for _, statement := range inferenceAttemptPolicyStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect automation inference attempt policies: %w", err)
			}
		}
		if err := protectModelEvaluationSnapshots(tx); err != nil {
			return err
		}
		// A plan execution may advance through its lifecycle, but its approved
		// plan, approval, concurrency ceiling, and parent-task idempotency binding
		// are immutable. Step assignments may advance status but cannot be
		// rebound to a different execution, step, or child task.
		planExecutionStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_delivery_plan_execution_rebinding()
			 RETURNS trigger AS $$
			 BEGIN
			   IF TG_OP IN ('DELETE', 'TRUNCATE') THEN
			     RAISE EXCEPTION 'delivery plan execution snapshots cannot be deleted or truncated';
			   END IF;
			   IF NEW.automation_task_id IS DISTINCT FROM OLD.automation_task_id
			      OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
			      OR NEW.plan_id IS DISTINCT FROM OLD.plan_id
			      OR NEW.plan_version IS DISTINCT FROM OLD.plan_version
			      OR NEW.approved_gate_id IS DISTINCT FROM OLD.approved_gate_id
			      OR NEW.plan_hash IS DISTINCT FROM OLD.plan_hash
			      OR NEW.max_concurrency IS DISTINCT FROM OLD.max_concurrency THEN
			     RAISE EXCEPTION 'delivery plan execution snapshot is immutable';
			   END IF;
			   RETURN NEW;
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_plan_executions_snapshot_immutable ON delivery_plan_executions",
			`CREATE TRIGGER delivery_plan_executions_snapshot_immutable
			 BEFORE UPDATE OR DELETE ON delivery_plan_executions
			 FOR EACH ROW EXECUTE FUNCTION prevent_delivery_plan_execution_rebinding()`,
			"DROP TRIGGER IF EXISTS delivery_plan_executions_no_truncate ON delivery_plan_executions",
			`CREATE TRIGGER delivery_plan_executions_no_truncate
			 BEFORE TRUNCATE ON delivery_plan_executions
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_delivery_plan_execution_rebinding()`,
			`CREATE OR REPLACE FUNCTION prevent_delivery_plan_step_assignment_rebinding()
			 RETURNS trigger AS $$
			 BEGIN
			   IF TG_OP IN ('DELETE', 'TRUNCATE') THEN
			     RAISE EXCEPTION 'delivery plan step assignments cannot be deleted or truncated';
			   END IF;
			   IF NEW.execution_id IS DISTINCT FROM OLD.execution_id
			      OR NEW.delivery_plan_step_id IS DISTINCT FROM OLD.delivery_plan_step_id
			      OR NEW.child_automation_task_id IS DISTINCT FROM OLD.child_automation_task_id THEN
			     RAISE EXCEPTION 'delivery plan step assignment is immutable';
			   END IF;
			   RETURN NEW;
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_assignments_binding_immutable ON delivery_plan_step_assignments",
			`CREATE TRIGGER delivery_plan_step_assignments_binding_immutable
			 BEFORE UPDATE OR DELETE ON delivery_plan_step_assignments
			 FOR EACH ROW EXECUTE FUNCTION prevent_delivery_plan_step_assignment_rebinding()`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_assignments_no_truncate ON delivery_plan_step_assignments",
			`CREATE TRIGGER delivery_plan_step_assignments_no_truncate
			 BEFORE TRUNCATE ON delivery_plan_step_assignments
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_delivery_plan_step_assignment_rebinding()`,
			`CREATE OR REPLACE FUNCTION capture_delivery_plan_step_assignment_event()
			 RETURNS trigger AS $$
			 DECLARE
			   event_type varchar(40);
			   previous_status varchar(24) := '';
			   previous_target_agent_key varchar(64) := '';
			   previous_target_machine_id varchar(64) := '';
			   assignment_plan_id uuid;
			   step_plan_id uuid;
			   assignment_plan_version integer;
			   parent_task_id uuid;
			 BEGIN
			   IF TG_OP = 'UPDATE' THEN
			     IF NEW.status IS NOT DISTINCT FROM OLD.status
			        AND NEW.target_agent_key IS NOT DISTINCT FROM OLD.target_agent_key
			        AND NEW.target_machine_id IS NOT DISTINCT FROM OLD.target_machine_id THEN
			       RETURN NEW;
			     END IF;
			     previous_status := COALESCE(OLD.status, '');
			     previous_target_agent_key := COALESCE(OLD.target_agent_key, '');
			     previous_target_machine_id := COALESCE(OLD.target_machine_id, '');
			     IF NEW.status IS DISTINCT FROM OLD.status
			        AND (NEW.target_agent_key IS DISTINCT FROM OLD.target_agent_key OR NEW.target_machine_id IS DISTINCT FROM OLD.target_machine_id) THEN
			       event_type := 'status_and_target_changed';
			     ELSIF NEW.status IS DISTINCT FROM OLD.status THEN
			       event_type := 'status_changed';
			     ELSE
			       event_type := 'target_changed';
			     END IF;
			   ELSE
			     event_type := 'assignment_created';
			   END IF;

			   SELECT execution.plan_id, execution.plan_version, execution.automation_task_id
			     INTO assignment_plan_id, assignment_plan_version, parent_task_id
			     FROM delivery_plan_executions AS execution
			     WHERE execution.id = NEW.execution_id;
			   IF NOT FOUND THEN
			     RAISE EXCEPTION 'delivery plan step assignment execution is missing';
			   END IF;
			   SELECT step.plan_id INTO step_plan_id
			     FROM delivery_plan_steps AS step
			     WHERE step.id = NEW.delivery_plan_step_id;
			   IF NOT FOUND OR step_plan_id IS DISTINCT FROM assignment_plan_id THEN
			     RAISE EXCEPTION 'delivery plan step assignment execution and step do not match';
			   END IF;

			   INSERT INTO delivery_plan_step_assignment_events (
			     id, assignment_id, execution_id, plan_id, plan_version, step_id,
			     parent_automation_task_id, child_automation_task_id, event_type,
			     previous_status, status, previous_target_agent_key, target_agent_key,
			     previous_target_machine_id, target_machine_id, occurred_at, created_at
			   ) VALUES (
			     uuid_generate_v4(), NEW.id, NEW.execution_id, assignment_plan_id, assignment_plan_version, NEW.delivery_plan_step_id,
			     parent_task_id, NEW.child_automation_task_id, event_type,
			     previous_status, COALESCE(NEW.status, ''), previous_target_agent_key, COALESCE(NEW.target_agent_key, ''),
			     previous_target_machine_id, COALESCE(NEW.target_machine_id, ''), clock_timestamp(), clock_timestamp()
			   );
			   RETURN NEW;
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_assignments_capture_events ON delivery_plan_step_assignments",
			`CREATE TRIGGER delivery_plan_step_assignments_capture_events
			 AFTER INSERT OR UPDATE OF status, target_agent_key, target_machine_id ON delivery_plan_step_assignments
			 FOR EACH ROW EXECUTE FUNCTION capture_delivery_plan_step_assignment_event()`,
			`CREATE OR REPLACE FUNCTION prevent_delivery_plan_step_assignment_event_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'delivery plan step assignment events are append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_assignment_events_append_only ON delivery_plan_step_assignment_events",
			`CREATE TRIGGER delivery_plan_step_assignment_events_append_only
			 BEFORE UPDATE OR DELETE ON delivery_plan_step_assignment_events
			 FOR EACH ROW EXECUTE FUNCTION prevent_delivery_plan_step_assignment_event_mutation()`,
			"DROP TRIGGER IF EXISTS delivery_plan_step_assignment_events_no_truncate ON delivery_plan_step_assignment_events",
			`CREATE TRIGGER delivery_plan_step_assignment_events_no_truncate
			 BEFORE TRUNCATE ON delivery_plan_step_assignment_events
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_delivery_plan_step_assignment_event_mutation()`,
		}
		for _, statement := range planExecutionStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect delivery plan execution bindings: %w", err)
			}
		}
		// Plan versions preserve the proposal that was reviewed. Only the
		// operational review outcome may change after insertion; content and
		// identity remain immutable, while status and approved_gate_id are
		// intentionally left available to markPlan.
		planVersionStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_delivery_plan_version_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   IF TG_OP IN ('DELETE', 'TRUNCATE') THEN
			     RAISE EXCEPTION 'delivery plan versions are append-only';
			   END IF;
			   IF NEW.id IS DISTINCT FROM OLD.id
			      OR NEW.work_item_id IS DISTINCT FROM OLD.work_item_id
			      OR NEW.version IS DISTINCT FROM OLD.version
			      OR NEW.summary IS DISTINCT FROM OLD.summary
			      OR NEW.structured_json IS DISTINCT FROM OLD.structured_json
			      OR NEW.context_digest IS DISTINCT FROM OLD.context_digest
			      OR NEW.proposed_by IS DISTINCT FROM OLD.proposed_by
			      OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
			     RAISE EXCEPTION 'delivery plan version content is immutable';
			   END IF;
			   RETURN NEW;
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_plans_content_immutable ON delivery_plans",
			`CREATE TRIGGER delivery_plans_content_immutable
			 BEFORE UPDATE OR DELETE ON delivery_plans
			 FOR EACH ROW EXECUTE FUNCTION prevent_delivery_plan_version_mutation()`,
			"DROP TRIGGER IF EXISTS delivery_plans_no_truncate ON delivery_plans",
			`CREATE TRIGGER delivery_plans_no_truncate
			 BEFORE TRUNCATE ON delivery_plans
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_delivery_plan_version_mutation()`,
		}
		for _, statement := range planVersionStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect delivery plan versions: %w", err)
			}
		}
		scheduleEventStatements := []string{
			`CREATE OR REPLACE FUNCTION prevent_delivery_automation_schedule_occurrence_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'delivery automation schedule occurrences are append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_automation_schedule_occurrences_append_only ON delivery_automation_schedule_occurrences",
			`CREATE TRIGGER delivery_automation_schedule_occurrences_append_only
			 BEFORE UPDATE OR DELETE ON delivery_automation_schedule_occurrences
			 FOR EACH ROW EXECUTE FUNCTION prevent_delivery_automation_schedule_occurrence_mutation()`,
			"DROP TRIGGER IF EXISTS delivery_automation_schedule_occurrences_no_truncate ON delivery_automation_schedule_occurrences",
			`CREATE TRIGGER delivery_automation_schedule_occurrences_no_truncate
			 BEFORE TRUNCATE ON delivery_automation_schedule_occurrences
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_delivery_automation_schedule_occurrence_mutation()`,
			`CREATE OR REPLACE FUNCTION prevent_delivery_automation_schedule_event_mutation()
			 RETURNS trigger AS $$
			 BEGIN
			   RAISE EXCEPTION 'delivery automation schedule events are append-only';
			 END;
			 $$ LANGUAGE plpgsql`,
			"DROP TRIGGER IF EXISTS delivery_automation_schedule_events_append_only ON delivery_automation_schedule_events",
			`CREATE TRIGGER delivery_automation_schedule_events_append_only
			 BEFORE UPDATE OR DELETE ON delivery_automation_schedule_events
			 FOR EACH ROW EXECUTE FUNCTION prevent_delivery_automation_schedule_event_mutation()`,
			"DROP TRIGGER IF EXISTS delivery_automation_schedule_events_no_truncate ON delivery_automation_schedule_events",
			`CREATE TRIGGER delivery_automation_schedule_events_no_truncate
			 BEFORE TRUNCATE ON delivery_automation_schedule_events
			 FOR EACH STATEMENT EXECUTE FUNCTION prevent_delivery_automation_schedule_event_mutation()`,
		}
		for _, statement := range scheduleEventStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("protect delivery automation schedule events: %w", err)
			}
		}
		return nil
	})
}

func SeedBaseData(cfg *models.Config) {
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
	profile := models.DefaultGeneralistAgentProfile()
	if err := DB.Where("agent_key = ?", profile.AgentKey).FirstOrCreate(&profile).Error; err != nil {
		slog.Error("default automation agent profile seed failed", "error", err)
	}
	// The deployed budget pair is server-owned routing metadata. When explicitly
	// configured, use it only to create the missing initial GitHub review policy;
	// established operator policy and revision history remain untouched.
	if cfg != nil {
		if err := seeds.SeedCodeReviewAIActionPolicy(DB, cfg.AutomationBudgetProvider, cfg.AutomationBudgetModel); err != nil {
			slog.Error("code review AI action policy bootstrap failed", "error", err)
			os.Exit(1)
		}
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
