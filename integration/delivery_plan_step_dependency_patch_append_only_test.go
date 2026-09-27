//go:build integration

package integration_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDeliveryPlanStepDependencyPatchReceiptsAreAppendOnly(t *testing.T) {
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			rollbackFixture := errors.New("rollback dependency-patch fixture")
			var mutationErr error
			err := configuration.DB.Transaction(func(tx *gorm.DB) error {
				lifecycleEvent := createStepEventFixture(t, tx)
				now := time.Now().UTC()
				runID, sourceRunID, workerID, machineID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
				receipt := models.DeliveryPlanStepDependencyPatch{
					ID: uuid.Must(uuid.NewV4()), PlanID: lifecycleEvent.PlanID,
					AutomationTaskID: uuid.Must(uuid.NewV4()), RunID: runID.String(), StepID: lifecycleEvent.StepID, FencingToken: 1,
					DependencyStepID: uuid.Must(uuid.NewV4()), SourcePatchArtifactID: uuid.Must(uuid.NewV4()),
					SourceAutomationTaskID: uuid.Must(uuid.NewV4()), SourceRunID: sourceRunID.String(),
					WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(),
					ManifestSHA256: strings.Repeat("a", 64), RepositoryRef: "workspace://backend",
					BaseSHA: strings.Repeat("b", 40), SHA256: strings.Repeat("c", 64), SizeBytes: 12, CreatedAt: now,
				}
				require.NoError(t, tx.Create(&receipt).Error)
				switch operation {
				case "update":
					mutationErr = tx.Model(&models.DeliveryPlanStepDependencyPatch{}).Where("id = ?", receipt.ID).Update("sha256", strings.Repeat("d", 64)).Error
				case "delete":
					mutationErr = tx.Delete(&receipt).Error
				}
				return rollbackFixture
			})
			require.ErrorIs(t, err, rollbackFixture, "the fixture transaction must roll back")
			require.Error(t, mutationErr)
			require.Contains(t, strings.ToLower(mutationErr.Error()), "dependency patch receipts are append-only")
		})
	}
}
