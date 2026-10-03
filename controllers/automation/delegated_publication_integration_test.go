//go:build integration

package automation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDelegatedPublicationPostgresFencesAndReceipts(t *testing.T) {
	container, err := postgrescontainer.Run(context.Background(), "postgres:16-alpine", postgrescontainer.WithDatabase("testdb"), postgrescontainer.WithUsername("test"), postgrescontainer.WithPassword("test"), postgrescontainer.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(context.Background(), "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableForeignKeyConstraintWhenMigrating: true})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error)
	require.NoError(t, configuration.MigrateModelsForTest(db))
	for _, scenario := range []string{"valid", "revoked", "expired", "superseded", "changed diff", "missing scanner", "wrong target"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Microsecond)
			item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: uuid.Must(uuid.NewV4()), Title: "Synthetic publication", State: "code_review", AutomationEpoch: 4, RequestedBy: "synthetic-owner"}
			require.NoError(t, db.Create(&item).Error)
			grant := models.DeliveryPublicationGrant{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, RepositoryRef: "workspace://api", Branch: "itbem-agent/" + uuid.Must(uuid.NewV4()).String(), BaseSHA: strings.Repeat("a", 40), GitHubRepository: "example/service", ReviewDiffSHA256: strings.Repeat("b", 64), CapabilitiesJSON: `["commit:stage","branch:publish","pull_request:create"]`, Reason: `{"allowed_target_branches":["main"]}`, GrantedBy: "delivery-gatekeeper", GrantedAt: now.Add(-time.Minute), ExpiresAt: now.Add(30 * time.Minute)}
			require.NoError(t, db.Create(&grant).Error)
			intent := models.DeliveryContinuation{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Epoch: item.AutomationEpoch, Phase: "publish", PublicationGrantID: grant.ID.String(), Status: "dispatched", RequestedBy: "synthetic-owner", CreatedAt: grant.GrantedAt, AvailableAt: now}
			require.NoError(t, db.Create(&intent).Error)
			task := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), Operation: "delivery.publish", Status: "running", DeliveryWorkItemID: &item.ID, ContinuationID: &intent.ID, EvidenceSubjectDigest: grant.ReviewDiffSHA256, CreatedAt: grant.GrantedAt}
			require.NoError(t, db.Create(&task).Error)
			metadata, _ := json.Marshal(map[string]any{"verification_source": "itbem-local-agent", "automation_task_id": uuid.Must(uuid.NewV4()).String(), "worktree": grant.RepositoryRef + "#" + grant.Branch, "base_sha": grant.BaseSHA, "github_repository": grant.GitHubRepository, "review_diff_sha256": grant.ReviewDiffSHA256})
			local := models.DeliveryChangeSet{WorkItemID: item.ID, RepositoryRef: grant.RepositoryRef, Branch: grant.Branch, ReviewType: "local_worktree", CIStatus: "passed", MetadataJSON: string(metadata), CreatedBy: "itbem-local-agent", CreatedAt: now.Add(-2 * time.Minute)}
			require.NoError(t, db.Create(&local).Error)
			handoff := publicationExecutionHandoff{GrantID: grant.ID.String(), Workspace: grant.RepositoryRef, RepositoryRef: grant.RepositoryRef, Worktree: grant.RepositoryRef + "#" + grant.Branch, Branch: grant.Branch, TargetBranch: "main", BaseSHA: grant.BaseSHA, CommitSHA: strings.Repeat("c", 40), RemoteRepository: grant.GitHubRepository, BranchPublished: true, PullRequestURL: "https://github.com/example/service/pull/42", PullRequestCreated: true, SecurityChecks: []string{"security:secrets", "security:high-critical"}}
			switch scenario {
			case "revoked":
				require.NoError(t, db.Model(&grant).Update("revoked_at", now).Error)
			case "expired":
				require.NoError(t, db.Model(&grant).Update("expires_at", now.Add(-time.Second)).Error)
			case "superseded":
				require.NoError(t, db.Model(&item).Update("automation_epoch", 5).Error)
			case "changed diff":
				local.ID = uuid.Must(uuid.NewV4())
				local.CreatedAt = now
				local.MetadataJSON = strings.ReplaceAll(local.MetadataJSON, grant.ReviewDiffSHA256, strings.Repeat("d", 64))
				require.NoError(t, db.Create(&local).Error)
			case "missing scanner":
				handoff.SecurityChecks = nil
			case "wrong target":
				handoff.TargetBranch = "production"
			}
			authorityErr := validateDelegatedPublicationRuntimeAuthority(db, task, now)
			if scenario == "revoked" || scenario == "expired" || scenario == "superseded" || scenario == "changed diff" {
				require.Error(t, authorityErr)
			} else {
				require.NoError(t, authorityErr)
			}
			raw, err := json.Marshal(handoff)
			require.NoError(t, err)
			err = db.Transaction(func(tx *gorm.DB) error { return persistPublicationChangeSet(tx, &task, raw, now) })
			var published int64
			require.NoError(t, db.Model(&models.DeliveryChangeSet{}).Where("work_item_id = ? AND review_type = ?", item.ID, "pull_request").Count(&published).Error)
			if scenario == "valid" {
				require.NoError(t, err)
				require.EqualValues(t, 1, published)
				var stored models.DeliveryChangeSet
				require.NoError(t, db.Where("work_item_id = ? AND review_type = ?", item.ID, "pull_request").First(&stored).Error)
				require.Contains(t, stored.MetadataJSON, grant.ReviewDiffSHA256)
				require.Contains(t, stored.MetadataJSON, "security:secrets")
				require.NoError(t, db.First(&grant, grant.ID).Error)
				require.NotNil(t, grant.RevokedAt)
				require.Error(t, validateDelegatedPublicationRuntimeAuthority(db, task, now))
			} else {
				require.Error(t, err)
				require.Zero(t, published)
			}
		})
	}
}
