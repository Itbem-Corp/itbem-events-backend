//go:build integration

package deliveryledger

import (
	"context"
	"testing"
	"time"

	"events-stocks/internal/deliverypolicy"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLedgerProjectionsSurvivePostgreSQLJSONBAndRejectTampering(t *testing.T) {
	ctx := context.Background()
	container, err := postgrescontainer.Run(ctx, "postgres:16-alpine", postgrescontainer.WithDatabase("testdb"), postgrescontainer.WithUsername("test"), postgrescontainer.WithPassword("test"), postgrescontainer.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error)
	require.NoError(t, db.AutoMigrate(&models.DeliveryEvent{}))
	item, now := uuid.Must(uuid.NewV4()), time.Now().UTC()
	tests := []struct {
		name    string
		event   models.DeliveryEvent
		project func(models.DeliveryEvent) error
	}{}
	qa, err := newQAObservationEvent(item, ledgerQAObservation(), now)
	require.NoError(t, err)
	tests = append(tests, struct {
		name    string
		event   models.DeliveryEvent
		project func(models.DeliveryEvent) error
	}{"qa", qa, func(e models.DeliveryEvent) error { _, err := ProjectQAObservation(e); return err }})
	security, err := newSecurityObservationEvent(item, ledgerSecurityObservation(), now)
	require.NoError(t, err)
	tests = append(tests, struct {
		name    string
		event   models.DeliveryEvent
		project func(models.DeliveryEvent) error
	}{"security", security, func(e models.DeliveryEvent) error { _, err := ProjectSecurityObservation(e); return err }})
	environment, err := newEnvironmentObservationEvent(item, ledgerEnvironmentObservation(), now)
	require.NoError(t, err)
	tests = append(tests, struct {
		name    string
		event   models.DeliveryEvent
		project func(models.DeliveryEvent) error
	}{"environment", environment, func(e models.DeliveryEvent) error { _, err := ProjectEnvironmentObservation(e); return err }})
	autonomy, err := newAutonomySnapshotEvent(item, autonomySnapshotFixture(t, deliverypolicy.GateApprovalDelegated), now)
	require.NoError(t, err)
	tests = append(tests, struct {
		name    string
		event   models.DeliveryEvent
		project func(models.DeliveryEvent) error
	}{"autonomy", autonomy, func(e models.DeliveryEvent) error { _, err := ProjectAutonomySnapshot(e); return err }})
	gate, err := newGateEvaluationEvent(item, validGateInput(t), now)
	require.NoError(t, err)
	tests = append(tests, struct {
		name    string
		event   models.DeliveryEvent
		project func(models.DeliveryEvent) error
	}{"gate", gate, func(e models.DeliveryEvent) error { _, err := ProjectGateEvaluation(e); return err }})
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.event.Sequence = int64(index + 1)
			require.NoError(t, db.Create(&test.event).Error)
			var stored models.DeliveryEvent
			require.NoError(t, db.First(&stored, test.event.ID).Error)
			require.NotEqual(t, test.event.PayloadJSON, stored.PayloadJSON, "fixture must exercise actual JSONB reformatting")
			require.NoError(t, test.project(stored))
			stored.PayloadDigest = "invalid"
			require.Error(t, test.project(stored))
		})
	}
}
