package configuration

import (
	"fmt"
	"gorm.io/gorm"
)

func protectModelEvaluationSnapshots(tx *gorm.DB) error {
	statements := []string{
		`CREATE OR REPLACE FUNCTION protect_model_evaluation_batch() RETURNS trigger AS $$
		 BEGIN
		   IF TG_OP IN ('DELETE', 'TRUNCATE') THEN
		     RAISE EXCEPTION 'model evaluation admission cannot be deleted or truncated';
		   END IF;
		   IF NEW.budget_micros <= 0 OR NEW.budget_micros > 1000000
		      OR NEW.reservation_micros <= 0 OR NEW.reservation_micros > NEW.budget_micros
		      OR NEW.status NOT IN ('active', 'halted', 'completed') THEN
		     RAISE EXCEPTION 'model evaluation admission bounds are invalid';
		   END IF;
		   IF TG_OP = 'UPDATE' THEN
		     IF (to_jsonb(NEW) - 'status') IS DISTINCT FROM (to_jsonb(OLD) - 'status')
		        OR (OLD.status <> 'active' AND NEW.status IS DISTINCT FROM OLD.status) THEN
		       RAISE EXCEPTION 'model evaluation admission is immutable';
		     END IF;
		   END IF;
		   RETURN NEW;
		 END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS model_evaluation_batch_immutable ON automation_model_evaluations`,
		`CREATE TRIGGER model_evaluation_batch_immutable BEFORE INSERT OR UPDATE OR DELETE ON automation_model_evaluations FOR EACH ROW EXECUTE FUNCTION protect_model_evaluation_batch()`,
		`DROP TRIGGER IF EXISTS model_evaluation_batch_no_truncate ON automation_model_evaluations`,
		`CREATE TRIGGER model_evaluation_batch_no_truncate BEFORE TRUNCATE ON automation_model_evaluations FOR EACH STATEMENT EXECUTE FUNCTION protect_model_evaluation_batch()`,
		`DROP TRIGGER IF EXISTS model_evaluation_calls_append_only ON automation_model_evaluation_calls`,
		`CREATE TRIGGER model_evaluation_calls_append_only BEFORE UPDATE OR DELETE ON automation_model_evaluation_calls FOR EACH ROW EXECUTE FUNCTION prevent_automation_inference_attempt_policy_mutation()`,
		`DROP TRIGGER IF EXISTS model_evaluation_calls_no_truncate ON automation_model_evaluation_calls`,
		`CREATE TRIGGER model_evaluation_calls_no_truncate BEFORE TRUNCATE ON automation_model_evaluation_calls FOR EACH STATEMENT EXECUTE FUNCTION prevent_automation_inference_attempt_policy_mutation()`,
	}
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("protect model evaluation snapshots: %w", err)
		}
	}
	return nil
}
