package delivery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// The stream is intentionally short lived. Reconnecting periodically makes
// authorization changes effective without trusting a subscription forever,
// while the browser gets a continuous feed during an active agent run.
const (
	deliveryWorkItemStreamActivePollInterval = time.Second
	deliveryWorkItemStreamIdlePollInterval   = 3 * time.Second
	deliveryWorkItemStreamHeartbeat          = 12 * time.Second
	deliveryWorkItemStreamLifetime           = 55 * time.Second
)

// deliveryWorkItemStreamRevisionSQL is a small, read-only projection used to
// decide whether the rich work-item document needs revalidation. It never
// loads agent prompts, outputs, provider usage JSON, or private references.
//
// The agent can run in a separate process, so an in-memory pub/sub fanout
// would lose updates. This projection stays faithful to the database source
// of truth and can later be backed by a durable outbox or Postgres NOTIFY
// without changing the browser event contract.
const deliveryWorkItemStreamRevisionSQL = `
SELECT
  work_item.id AS work_item_id,
  work_item.state AS state,
  work_item.updated_at AS work_item_updated_at,
  (SELECT COUNT(*) FROM automation_tasks AS task WHERE task.delivery_work_item_id = work_item.id) AS automation_task_count,
  -- A stop request is the operator's newest intent. Reporting the queued or
  -- running sibling attempts as active would make a closing run look live to
  -- the UI and keep the high-frequency polling path unnecessarily awake.
  (SELECT CASE
    WHEN EXISTS (
      SELECT 1 FROM automation_tasks AS stopping_task
      WHERE stopping_task.delivery_work_item_id = work_item.id
        AND stopping_task.status = 'cancel_requested'
    ) THEN 0
    ELSE COUNT(*)
  END FROM automation_tasks AS task
  WHERE task.delivery_work_item_id = work_item.id AND task.status IN ('queued', 'running')) AS active_task_count,
  (SELECT MAX(task.updated_at) FROM automation_tasks AS task WHERE task.delivery_work_item_id = work_item.id) AS automation_task_updated_at,
  (SELECT COUNT(*) FROM delivery_context_snapshots AS snapshot WHERE snapshot.work_item_id = work_item.id) AS context_snapshot_count,
  (SELECT MAX(snapshot.created_at) FROM delivery_context_snapshots AS snapshot WHERE snapshot.work_item_id = work_item.id) AS context_snapshot_created_at,
  (SELECT COUNT(*) FROM delivery_work_item_dependencies AS dependency WHERE dependency.work_item_id = work_item.id) AS dependency_count,
  (SELECT MAX(dependency.created_at) FROM delivery_work_item_dependencies AS dependency WHERE dependency.work_item_id = work_item.id) AS dependency_created_at,
  (SELECT COUNT(*) FROM delivery_plans AS plan WHERE plan.work_item_id = work_item.id) AS plan_count,
  (SELECT MAX(plan.created_at) FROM delivery_plans AS plan WHERE plan.work_item_id = work_item.id) AS plan_created_at,
  (SELECT COUNT(*) FROM delivery_change_sets AS change_set WHERE change_set.work_item_id = work_item.id) AS change_set_count,
  (SELECT MAX(change_set.updated_at) FROM delivery_change_sets AS change_set WHERE change_set.work_item_id = work_item.id) AS change_set_updated_at,
  (SELECT COUNT(*) FROM delivery_publication_grants AS publication_grant WHERE publication_grant.work_item_id = work_item.id) AS publication_grant_count,
  (SELECT MAX(publication_grant.updated_at) FROM delivery_publication_grants AS publication_grant WHERE publication_grant.work_item_id = work_item.id) AS publication_grant_updated_at,
  (SELECT COUNT(*) FROM delivery_gates AS gate WHERE gate.work_item_id = work_item.id) AS gate_count,
  (SELECT MAX(gate.created_at) FROM delivery_gates AS gate WHERE gate.work_item_id = work_item.id) AS gate_created_at,
  (SELECT COUNT(*) FROM delivery_evidences AS evidence WHERE evidence.work_item_id = work_item.id) AS evidence_count,
  (SELECT MAX(evidence.created_at) FROM delivery_evidences AS evidence WHERE evidence.work_item_id = work_item.id) AS evidence_created_at,
  (SELECT COUNT(*) FROM delivery_messages AS message WHERE message.work_item_id = work_item.id) AS message_count,
  (SELECT MAX(message.created_at) FROM delivery_messages AS message WHERE message.work_item_id = work_item.id) AS message_created_at,
  (SELECT COUNT(*) FROM automation_executions AS execution WHERE execution.delivery_work_item_id = work_item.id) AS execution_count,
  (SELECT MAX(execution.completed_at) FROM automation_executions AS execution WHERE execution.delivery_work_item_id = work_item.id) AS execution_completed_at,
  (SELECT COUNT(*) FROM automation_tool_executions AS tool_execution WHERE tool_execution.delivery_work_item_id = work_item.id) AS tool_execution_count,
  (SELECT MAX(tool_execution.completed_at) FROM automation_tool_executions AS tool_execution WHERE tool_execution.delivery_work_item_id = work_item.id) AS tool_execution_completed_at
FROM delivery_work_items AS work_item
WHERE work_item.id = ? AND work_item.deleted_at IS NULL`

type deliveryWorkItemStreamRevisionSource struct {
	WorkItemID                uuid.UUID    `gorm:"column:work_item_id"`
	State                     string       `gorm:"column:state"`
	WorkItemUpdatedAt         time.Time    `gorm:"column:work_item_updated_at"`
	AutomationTaskCount       int64        `gorm:"column:automation_task_count"`
	ActiveTaskCount           int64        `gorm:"column:active_task_count"`
	AutomationTaskUpdatedAt   sql.NullTime `gorm:"column:automation_task_updated_at"`
	ContextSnapshotCount      int64        `gorm:"column:context_snapshot_count"`
	ContextSnapshotCreatedAt  sql.NullTime `gorm:"column:context_snapshot_created_at"`
	DependencyCount           int64        `gorm:"column:dependency_count"`
	DependencyCreatedAt       sql.NullTime `gorm:"column:dependency_created_at"`
	PlanCount                 int64        `gorm:"column:plan_count"`
	PlanCreatedAt             sql.NullTime `gorm:"column:plan_created_at"`
	ChangeSetCount            int64        `gorm:"column:change_set_count"`
	ChangeSetUpdatedAt        sql.NullTime `gorm:"column:change_set_updated_at"`
	PublicationGrantCount     int64        `gorm:"column:publication_grant_count"`
	PublicationGrantUpdatedAt sql.NullTime `gorm:"column:publication_grant_updated_at"`
	GateCount                 int64        `gorm:"column:gate_count"`
	GateCreatedAt             sql.NullTime `gorm:"column:gate_created_at"`
	EvidenceCount             int64        `gorm:"column:evidence_count"`
	EvidenceCreatedAt         sql.NullTime `gorm:"column:evidence_created_at"`
	MessageCount              int64        `gorm:"column:message_count"`
	MessageCreatedAt          sql.NullTime `gorm:"column:message_created_at"`
	ExecutionCount            int64        `gorm:"column:execution_count"`
	ExecutionCompletedAt      sql.NullTime `gorm:"column:execution_completed_at"`
	ToolExecutionCount        int64        `gorm:"column:tool_execution_count"`
	ToolExecutionCompletedAt  sql.NullTime `gorm:"column:tool_execution_completed_at"`
}

// deliveryWorkItemStreamEvent is intentionally an invalidation signal, not a
// second representation of the work item. Clients revalidate the existing
// authorized resource after receiving it, so every graph and panel keeps one
// canonical data contract.
type deliveryWorkItemStreamEvent struct {
	WorkItemID     string `json:"work_item_id"`
	Revision       string `json:"revision"`
	State          string `json:"state"`
	ActiveTasks    int64  `json:"active_tasks"`
	LastActivityAt string `json:"last_activity_at,omitempty"`
	GeneratedAt    string `json:"generated_at"`
}

// deliveryWorkItemStreamPollIntervalFor keeps the live execution feeling
// immediate while the agent is working, without asking the database to prove
// an unchanged idle gate every second. A decision made in the UI already
// refreshes its own resource; this bounds the passive SSE read path.
func deliveryWorkItemStreamPollIntervalFor(event deliveryWorkItemStreamEvent) time.Duration {
	if event.ActiveTasks > 0 {
		return deliveryWorkItemStreamActivePollInterval
	}
	return deliveryWorkItemStreamIdlePollInterval
}

func deliveryWorkItemStreamSnapshot(db *gorm.DB, workItemID uuid.UUID, now time.Time) (deliveryWorkItemStreamEvent, error) {
	var source deliveryWorkItemStreamRevisionSource
	result := db.Raw(deliveryWorkItemStreamRevisionSQL, workItemID).Scan(&source)
	if result.Error != nil {
		return deliveryWorkItemStreamEvent{}, result.Error
	}
	if result.RowsAffected == 0 || source.WorkItemID == uuid.Nil {
		return deliveryWorkItemStreamEvent{}, gorm.ErrRecordNotFound
	}

	return deliveryWorkItemStreamEvent{
		WorkItemID:     source.WorkItemID.String(),
		Revision:       deliveryWorkItemStreamRevision(source),
		State:          source.State,
		ActiveTasks:    source.ActiveTaskCount,
		LastActivityAt: deliveryWorkItemStreamLastActivity(source),
		GeneratedAt:    now.UTC().Format(time.RFC3339Nano),
	}, nil
}

func deliveryWorkItemStreamRevision(source deliveryWorkItemStreamRevisionSource) string {
	values := []string{
		source.WorkItemID.String(), source.State, deliveryWorkItemStreamTime(source.WorkItemUpdatedAt),
		strconv.FormatInt(source.AutomationTaskCount, 10), strconv.FormatInt(source.ActiveTaskCount, 10), deliveryWorkItemStreamNullTime(source.AutomationTaskUpdatedAt),
		strconv.FormatInt(source.ContextSnapshotCount, 10), deliveryWorkItemStreamNullTime(source.ContextSnapshotCreatedAt),
		strconv.FormatInt(source.DependencyCount, 10), deliveryWorkItemStreamNullTime(source.DependencyCreatedAt),
		strconv.FormatInt(source.PlanCount, 10), deliveryWorkItemStreamNullTime(source.PlanCreatedAt),
		strconv.FormatInt(source.ChangeSetCount, 10), deliveryWorkItemStreamNullTime(source.ChangeSetUpdatedAt),
		strconv.FormatInt(source.PublicationGrantCount, 10), deliveryWorkItemStreamNullTime(source.PublicationGrantUpdatedAt),
		strconv.FormatInt(source.GateCount, 10), deliveryWorkItemStreamNullTime(source.GateCreatedAt),
		strconv.FormatInt(source.EvidenceCount, 10), deliveryWorkItemStreamNullTime(source.EvidenceCreatedAt),
		strconv.FormatInt(source.MessageCount, 10), deliveryWorkItemStreamNullTime(source.MessageCreatedAt),
		strconv.FormatInt(source.ExecutionCount, 10), deliveryWorkItemStreamNullTime(source.ExecutionCompletedAt),
		strconv.FormatInt(source.ToolExecutionCount, 10), deliveryWorkItemStreamNullTime(source.ToolExecutionCompletedAt),
	}
	digest := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return hex.EncodeToString(digest[:])
}

func deliveryWorkItemStreamLastActivity(source deliveryWorkItemStreamRevisionSource) string {
	latest := source.WorkItemUpdatedAt
	for _, candidate := range []sql.NullTime{
		source.AutomationTaskUpdatedAt,
		source.ContextSnapshotCreatedAt,
		source.DependencyCreatedAt,
		source.PlanCreatedAt,
		source.ChangeSetUpdatedAt,
		source.PublicationGrantUpdatedAt,
		source.GateCreatedAt,
		source.EvidenceCreatedAt,
		source.MessageCreatedAt,
		source.ExecutionCompletedAt,
		source.ToolExecutionCompletedAt,
	} {
		if candidate.Valid && candidate.Time.After(latest) {
			latest = candidate.Time
		}
	}
	return deliveryWorkItemStreamTime(latest)
}

func deliveryWorkItemStreamTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func deliveryWorkItemStreamNullTime(value sql.NullTime) string {
	if !value.Valid {
		return ""
	}
	return deliveryWorkItemStreamTime(value.Time)
}

func writeDeliveryWorkItemStreamEvent(writer http.ResponseWriter, eventName string, event deliveryWorkItemStreamEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "event: %s\nid: %s\ndata: %s\n\n", eventName, event.Revision, payload); err != nil {
		return err
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeDeliveryWorkItemStreamKeepalive(writer http.ResponseWriter) error {
	if _, err := fmt.Fprint(writer, ": keepalive\n\n"); err != nil {
		return err
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// StreamWorkItem exposes an authorized, bounded SSE invalidation stream. It
// sends a snapshot immediately, then an update only when a database-backed
// revision changes. The endpoint deliberately does not stream task output or
// agent payloads; clients keep using their existing scoped GET endpoint.
func StreamWorkItem(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	if _, _, err := workItemActor(c, workItemID, deliveryView); err != nil {
		return err
	}

	response := c.Response()
	flusher, ok := response.Writer.(http.Flusher)
	if !ok {
		return utils.Error(c, http.StatusInternalServerError, "Delivery stream unavailable", "Response streaming is not supported")
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Delivery unavailable", "Database is unavailable")
	}

	initial, err := deliveryWorkItemStreamSnapshot(configuration.DB, workItemID, time.Now().UTC())
	if err != nil {
		return lookup(c, "Delivery work item", err)
	}

	headers := response.Header()
	headers.Set(echo.HeaderContentType, "text/event-stream; charset=utf-8")
	headers.Set(echo.HeaderCacheControl, "no-cache, no-transform")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")
	// The global gzip middleware can buffer streaming data in some proxy
	// setups. SSE events are intentionally tiny, so compression has no value.
	headers.Set(echo.HeaderContentEncoding, "identity")
	headers.Set("Retry-After", "2")
	response.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(response.Writer, "retry: 2000\n\n"); err != nil {
		return nil
	}
	flusher.Flush()
	if err := writeDeliveryWorkItemStreamEvent(response.Writer, "snapshot", initial); err != nil {
		return nil
	}

	streamContext, cancel := context.WithTimeout(c.Request().Context(), deliveryWorkItemStreamLifetime)
	defer cancel()
	pollTimer := time.NewTimer(deliveryWorkItemStreamPollIntervalFor(initial))
	heartbeatTicker := time.NewTicker(deliveryWorkItemStreamHeartbeat)
	defer pollTimer.Stop()
	defer heartbeatTicker.Stop()

	currentRevision := initial.Revision
	for {
		select {
		case <-streamContext.Done():
			return nil
		case <-heartbeatTicker.C:
			if err := writeDeliveryWorkItemStreamKeepalive(response.Writer); err != nil {
				return nil
			}
		case <-pollTimer.C:
			next, snapshotErr := deliveryWorkItemStreamSnapshot(configuration.DB, workItemID, time.Now().UTC())
			if snapshotErr != nil {
				// The next short-lived connection will re-run authorization. A
				// missing item should quietly terminate rather than disclose any
				// state to a subscriber that lost access.
				return nil
			}
			if next.Revision != currentRevision {
				currentRevision = next.Revision
				if err := writeDeliveryWorkItemStreamEvent(response.Writer, "update", next); err != nil {
					return nil
				}
			}
			pollTimer.Reset(deliveryWorkItemStreamPollIntervalFor(next))
		}
	}
}
