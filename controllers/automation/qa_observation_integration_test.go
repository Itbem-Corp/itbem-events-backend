//go:build integration

package automation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/qaevidence"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Exercises the real callback projection and append-only ledger, without
// provider calls or production credentials. HTTP authentication is separate.
func TestQAObservationProjectionPersistsExactSubjectAndRejectsChangedRecovery(t *testing.T) {
	ctx := context.Background()
	container, err := postgrescontainer.Run(ctx, "postgres:16-alpine", postgrescontainer.WithDatabase("testdb"), postgrescontainer.WithUsername("test"), postgrescontainer.WithPassword("test"), postgrescontainer.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableForeignKeyConstraintWhenMigrating: true})
	require.NoError(t, err)
	rawDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rawDB.Close()) })
	require.NoError(t, db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error)
	require.NoError(t, configuration.MigrateModelsForTest(db))
	itemID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	require.NoError(t, db.Create(&models.DeliveryWorkItem{ID: itemID, ProjectID: uuid.Must(uuid.NewV4()), RequestedBy: "synthetic-human", Title: "synthetic QA"}).Error)
	task := models.AutomationTask{ID: taskID, Operation: "delivery.qa", DeliveryWorkItemID: &itemID, EvidenceSubjectDigest: strings.Repeat("a", 64)}
	observation := qaevidence.Observation{SchemaVersion: 2, TaskID: taskID.String(), MatrixDigest: task.EvidenceSubjectDigest, PreviewPassed: true, RepositoryExecutionOrder: []string{"workspace://repo"}, Repositories: []qaevidence.Repository{{Reference: "workspace://repo", Branch: "itbem-agent/" + taskID.String(), Commands: []qaevidence.Command{{Index: 0, Phase: "validation", Kind: "unit", Passed: false}}}}}
	payload, err := json.Marshal(observation)
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, persistQAObservation(db, &task, payload, now))
	require.NoError(t, persistQAObservation(db, &task, payload, now.Add(time.Minute)))
	var events []models.DeliveryEvent
	require.NoError(t, db.Where("work_item_id = ?", itemID).Find(&events).Error)
	require.Len(t, events, 1)
	projected, err := deliveryledger.ProjectQAObservation(events[0])
	require.NoError(t, err)
	require.Equal(t, observation, projected.Observation)
	observation.Repositories[0].Commands[0].Passed = true
	changed, err := json.Marshal(observation)
	require.NoError(t, err)
	require.Error(t, persistQAObservation(db, &task, changed, now))
	observation.MatrixDigest = strings.Repeat("b", 64)
	changed, err = json.Marshal(observation)
	require.NoError(t, err)
	require.Error(t, persistQAObservation(db, &task, changed, now))
	var count int64
	require.NoError(t, db.Model(&models.DeliveryEvent{}).Where("work_item_id = ?", itemID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}
