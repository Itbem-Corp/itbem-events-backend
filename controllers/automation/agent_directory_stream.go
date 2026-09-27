package automation

import (
	"context"
	"crypto/rand"
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
	"events-stocks/internal/authz"
	"events-stocks/utils"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const (
	agentDirectoryStreamActivePollInterval       = time.Second
	agentDirectoryStreamIdlePollInterval         = 3 * time.Second
	agentDirectoryStreamScopedActivePollInterval = 2 * time.Second
	agentDirectoryStreamScopedIdlePollInterval   = 5 * time.Second
	agentDirectoryStreamLifetime                 = 55 * time.Second
)

var agentDirectoryStreamHeartbeat = 12 * time.Second

// agentDirectoryStreamRevisionSQL is deliberately a narrow aggregate
// projection. The directory itself remains the canonical GET response; this
// query only detects changes to records that feed it. In particular, it never
// selects prompts, results, usage JSON, workspace readiness, or credentials.
// The 30-day window matches the directory's heartbeat and cost windows, while
// active task/step/assignment state remains observable regardless of age.
const agentDirectoryStreamRevisionSQL = `
SELECT
  (SELECT COUNT(*) FROM automation_agent_profiles) AS profile_count,
  (SELECT MAX(updated_at) FROM automation_agent_profiles) AS profile_updated_at,
  (SELECT COUNT(*) FROM automation_agent_heartbeats WHERE last_seen_at >= ?) AS heartbeat_count,
  (SELECT MAX(updated_at) FROM automation_agent_heartbeats WHERE last_seen_at >= ?) AS heartbeat_updated_at,
  (SELECT MAX(last_seen_at) FROM automation_agent_heartbeats WHERE last_seen_at >= ?) AS heartbeat_last_seen_at,
  (SELECT COUNT(*) FROM automation_tasks WHERE status = 'queued') AS queued_task_count,
  (SELECT MIN(created_at) FROM automation_tasks WHERE status = 'queued') AS oldest_queued_at,
  (SELECT COUNT(*) FROM automation_tasks WHERE status IN ('running', 'cancel_requested')) AS active_task_count,
  (SELECT MAX(updated_at) FROM automation_tasks WHERE status IN ('running', 'cancel_requested')) AS active_task_updated_at,
  (SELECT MAX(lease_expires_at) FROM automation_tasks WHERE status IN ('running', 'cancel_requested')) AS active_task_lease_at,
  (SELECT COALESCE(SUM(progress_call), 0) FROM automation_tasks WHERE status IN ('running', 'cancel_requested')) AS active_progress_total,
  (SELECT COUNT(*) FROM automation_tasks WHERE status IN ('running', 'cancel_requested') AND worker_id <> '') AS assigned_task_count,
  (SELECT COUNT(*) FROM delivery_plan_steps WHERE status IN ('planned', 'ready', 'running', 'blocked')) AS active_step_count,
  (SELECT MAX(updated_at) FROM delivery_plan_steps WHERE status IN ('planned', 'ready', 'running', 'blocked')) AS active_step_updated_at,
  (SELECT MAX(lease_expires_at) FROM delivery_plan_steps WHERE status IN ('planned', 'ready', 'running', 'blocked')) AS active_step_lease_at,
  (SELECT COUNT(*) FROM delivery_plan_step_assignments WHERE status IN ('pending', 'queued', 'dispatched', 'running', 'blocked')) AS active_assignment_count,
  (SELECT MAX(updated_at) FROM delivery_plan_step_assignments WHERE status IN ('pending', 'queued', 'dispatched', 'running', 'blocked')) AS active_assignment_updated_at,
  (SELECT COUNT(*) FROM (
    SELECT completed_at, total_cost_micros FROM automation_executions WHERE completed_at >= ?
    UNION ALL
    SELECT completed_at, total_cost_micros FROM automation_tool_executions WHERE completed_at >= ?
  ) AS ledger) AS ledger_entry_count,
  (SELECT COALESCE(SUM(total_cost_micros), 0) FROM (
    SELECT total_cost_micros FROM automation_executions WHERE completed_at >= ?
    UNION ALL
    SELECT total_cost_micros FROM automation_tool_executions WHERE completed_at >= ?
  ) AS ledger) AS ledger_spend_micros,
  (SELECT MAX(completed_at) FROM (
    SELECT completed_at FROM automation_executions WHERE completed_at >= ?
    UNION ALL
    SELECT completed_at FROM automation_tool_executions WHERE completed_at >= ?
  ) AS ledger) AS ledger_latest_at,
  (SELECT MAX(w.updated_at)
   FROM delivery_work_items AS w
   JOIN automation_tasks AS t ON t.delivery_work_item_id = w.id
   WHERE t.status IN ('running', 'cancel_requested') AND w.deleted_at IS NULL) AS work_item_updated_at,
  (SELECT MAX(p.updated_at)
   FROM delivery_projects AS p
   JOIN delivery_work_items AS w ON w.project_id = p.id
   JOIN automation_tasks AS t ON t.delivery_work_item_id = w.id
   WHERE t.status IN ('running', 'cancel_requested') AND p.deleted_at IS NULL AND w.deleted_at IS NULL) AS project_updated_at,
  (SELECT MAX(c.updated_at)
   FROM clients AS c
   JOIN delivery_projects AS p ON p.client_id = c.id
   JOIN delivery_work_items AS w ON w.project_id = p.id
   JOIN automation_tasks AS t ON t.delivery_work_item_id = w.id
   WHERE t.status IN ('running', 'cancel_requested') AND c.deleted_at IS NULL AND p.deleted_at IS NULL AND w.deleted_at IS NULL) AS client_updated_at`

type agentDirectoryStreamProjection struct {
	ProfileCount              int64        `gorm:"column:profile_count"`
	ProfileUpdatedAt          sql.NullTime `gorm:"column:profile_updated_at"`
	HeartbeatCount            int64        `gorm:"column:heartbeat_count"`
	HeartbeatUpdatedAt        sql.NullTime `gorm:"column:heartbeat_updated_at"`
	HeartbeatLastSeenAt       sql.NullTime `gorm:"column:heartbeat_last_seen_at"`
	QueuedTaskCount           int64        `gorm:"column:queued_task_count"`
	OldestQueuedAt            sql.NullTime `gorm:"column:oldest_queued_at"`
	ActiveTaskCount           int64        `gorm:"column:active_task_count"`
	ActiveTaskUpdatedAt       sql.NullTime `gorm:"column:active_task_updated_at"`
	ActiveTaskLeaseAt         sql.NullTime `gorm:"column:active_task_lease_at"`
	ActiveProgressTotal       int64        `gorm:"column:active_progress_total"`
	AssignedTaskCount         int64        `gorm:"column:assigned_task_count"`
	ActiveStepCount           int64        `gorm:"column:active_step_count"`
	ActiveStepUpdatedAt       sql.NullTime `gorm:"column:active_step_updated_at"`
	ActiveStepLeaseAt         sql.NullTime `gorm:"column:active_step_lease_at"`
	ActiveAssignmentCount     int64        `gorm:"column:active_assignment_count"`
	ActiveAssignmentUpdatedAt sql.NullTime `gorm:"column:active_assignment_updated_at"`
	LedgerEntryCount          int64        `gorm:"column:ledger_entry_count"`
	LedgerSpendMicros         int64        `gorm:"column:ledger_spend_micros"`
	LedgerLatestAt            sql.NullTime `gorm:"column:ledger_latest_at"`
	WorkItemUpdatedAt         sql.NullTime `gorm:"column:work_item_updated_at"`
	ProjectUpdatedAt          sql.NullTime `gorm:"column:project_updated_at"`
	ClientUpdatedAt           sql.NullTime `gorm:"column:client_updated_at"`
}

type agentDirectoryStreamEvent struct {
	Revision    string `json:"revision"`
	GeneratedAt string `json:"generated_at"`
	fingerprint string `json:"-"`
}

func agentDirectoryStreamSnapshot(db *gorm.DB, now time.Time) (agentDirectoryStreamProjection, agentDirectoryStreamEvent, error) {
	cutoff := now.UTC().Add(-30 * 24 * time.Hour)
	var projection agentDirectoryStreamProjection
	args := []any{cutoff, cutoff, cutoff, cutoff, cutoff, cutoff, cutoff, cutoff, cutoff}
	result := db.Raw(agentDirectoryStreamRevisionSQL, args...).Scan(&projection)
	if result.Error != nil {
		return agentDirectoryStreamProjection{}, agentDirectoryStreamEvent{}, result.Error
	}
	if result.RowsAffected == 0 {
		return agentDirectoryStreamProjection{}, agentDirectoryStreamEvent{}, gorm.ErrRecordNotFound
	}
	event, err := newAgentDirectoryStreamEvent(projection, now)
	if err != nil {
		return agentDirectoryStreamProjection{}, agentDirectoryStreamEvent{}, err
	}
	return projection, event, nil
}

// agentDirectoryStreamScopedSnapshot fingerprints the exact safe directory
// projection returned by buildAgentDirectory, not the platform-wide aggregate.
func agentDirectoryStreamScopedSnapshot(ctx context.Context, db *gorm.DB, now time.Time, scope agentDirectoryScope) (agentDirectoryResponse, agentDirectoryStreamEvent, error) {
	directory, err := buildAgentDirectory(ctx, db, now, scope)
	if err != nil {
		return agentDirectoryResponse{}, agentDirectoryStreamEvent{}, err
	}
	// GeneratedAt necessarily changes on every poll and is not directory state.
	directory.GeneratedAt = time.Time{}
	encoded, err := json.Marshal(directory)
	if err != nil {
		return agentDirectoryResponse{}, agentDirectoryStreamEvent{}, err
	}
	projection := sha256.Sum256(encoded)
	event, err := newAgentDirectoryStreamRevisionEvent(now, hex.EncodeToString(projection[:]))
	if err != nil {
		return agentDirectoryResponse{}, agentDirectoryStreamEvent{}, err
	}
	return directory, event, nil
}

func newAgentDirectoryStreamEvent(projection agentDirectoryStreamProjection, now time.Time) (agentDirectoryStreamEvent, error) {
	// Do not expose a hash of low-entropy operational counters as the SSE
	// revision. The browser only needs a random invalidation token; the
	// deterministic fingerprint stays server-side for change detection.
	revision := make([]byte, 32)
	if _, err := rand.Read(revision); err != nil {
		return agentDirectoryStreamEvent{}, err
	}
	return agentDirectoryStreamEvent{Revision: hex.EncodeToString(revision), GeneratedAt: now.UTC().Format(time.RFC3339Nano), fingerprint: agentDirectoryStreamFingerprint(projection)}, nil
}

func newAgentDirectoryStreamRevisionEvent(now time.Time, fingerprint string) (agentDirectoryStreamEvent, error) {
	revision := make([]byte, 32)
	if _, err := rand.Read(revision); err != nil {
		return agentDirectoryStreamEvent{}, err
	}
	return agentDirectoryStreamEvent{Revision: hex.EncodeToString(revision), GeneratedAt: now.UTC().Format(time.RFC3339Nano), fingerprint: fingerprint}, nil
}

func agentDirectoryStreamFingerprint(projection agentDirectoryStreamProjection) string {
	values := []string{
		strconv.FormatInt(projection.ProfileCount, 10), agentDirectoryStreamTime(projection.ProfileUpdatedAt),
		strconv.FormatInt(projection.HeartbeatCount, 10), agentDirectoryStreamTime(projection.HeartbeatUpdatedAt), agentDirectoryStreamTime(projection.HeartbeatLastSeenAt),
		strconv.FormatInt(projection.QueuedTaskCount, 10), agentDirectoryStreamTime(projection.OldestQueuedAt),
		strconv.FormatInt(projection.ActiveTaskCount, 10), agentDirectoryStreamTime(projection.ActiveTaskUpdatedAt), agentDirectoryStreamTime(projection.ActiveTaskLeaseAt),
		strconv.FormatInt(projection.ActiveProgressTotal, 10), strconv.FormatInt(projection.AssignedTaskCount, 10),
		strconv.FormatInt(projection.ActiveStepCount, 10), agentDirectoryStreamTime(projection.ActiveStepUpdatedAt), agentDirectoryStreamTime(projection.ActiveStepLeaseAt),
		strconv.FormatInt(projection.ActiveAssignmentCount, 10), agentDirectoryStreamTime(projection.ActiveAssignmentUpdatedAt),
		strconv.FormatInt(projection.LedgerEntryCount, 10), strconv.FormatInt(projection.LedgerSpendMicros, 10), agentDirectoryStreamTime(projection.LedgerLatestAt),
		agentDirectoryStreamTime(projection.WorkItemUpdatedAt), agentDirectoryStreamTime(projection.ProjectUpdatedAt), agentDirectoryStreamTime(projection.ClientUpdatedAt),
	}
	digest := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return hex.EncodeToString(digest[:])
}

func agentDirectoryStreamTime(value sql.NullTime) string {
	if !value.Valid {
		return ""
	}
	return value.Time.UTC().Format(time.RFC3339Nano)
}

func writeAgentDirectoryStreamEvent(writer http.ResponseWriter, eventName string, event agentDirectoryStreamEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", eventName, payload); err != nil {
		return err
	}
	writer.(http.Flusher).Flush()
	return nil
}

func writeAgentDirectoryStreamKeepalive(writer http.ResponseWriter) error {
	if _, err := fmt.Fprint(writer, ": keepalive\n\n"); err != nil {
		return err
	}
	writer.(http.Flusher).Flush()
	return nil
}

func resolveAgentDirectoryStreamScope(c echo.Context, db *gorm.DB) (agentDirectoryScope, string, error) {
	if db == nil {
		return agentDirectoryScope{}, "", gorm.ErrInvalidDB
	}
	workspaceMode, _ := c.Get("workspace_mode").(string)
	workspaceMode = strings.ToLower(strings.TrimSpace(workspaceMode))
	if workspaceMode == "platform" {
		if err := requireAgentPlatformWorkspace(c, false); err != nil {
			return agentDirectoryScope{}, workspaceMode, err
		}
	} else if workspaceMode != "organization" {
		return agentDirectoryScope{}, workspaceMode, &authz.Failure{Status: http.StatusNotFound, Message: "Agent directory unavailable"}
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return agentDirectoryScope{}, workspaceMode, err
	}
	scope, status, err := parseAgentDirectoryScope(c, db)
	if err != nil {
		return agentDirectoryScope{}, workspaceMode, &authz.Failure{Status: status, Message: "Agent directory unavailable", Detail: "The selected client or project is unavailable"}
	}
	if err := authorizeAgentDirectoryScope(c, db, user, workspaceMode, &scope); err != nil {
		return agentDirectoryScope{}, workspaceMode, err
	}
	return scope, workspaceMode, nil
}

// StreamAgentDirectory sends only opaque revision invalidations. Subscribers
// must re-fetch GET /automation/agents through the normal authenticated path;
// this connection never carries directory data or stays open beyond one minute.
func StreamAgentDirectory(c echo.Context) error {
	workspaceMode, _ := c.Get("workspace_mode").(string)
	workspaceMode = strings.ToLower(strings.TrimSpace(workspaceMode))
	if workspaceMode == "platform" {
		if err := requireAgentPlatformWorkspace(c, false); err != nil {
			return authz.Respond(c, err)
		}
	} else if workspaceMode != "organization" {
		return utils.Error(c, http.StatusNotFound, "Agent directory unavailable", "")
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Agent directory unavailable", "Database is unavailable")
	}
	scope, status, err := parseAgentDirectoryScope(c, configuration.DB)
	if err != nil {
		return utils.Error(c, status, "Agent directory unavailable", "The selected client or project is unavailable")
	}
	if err := authorizeAgentDirectoryScope(c, configuration.DB, user, workspaceMode, &scope); err != nil {
		return err
	}
	response := c.Response()
	if _, ok := response.Writer.(http.Flusher); !ok {
		return utils.Error(c, http.StatusInternalServerError, "Agent directory stream unavailable", "Response streaming is not supported")
	}
	initialAt := time.Now().UTC()
	var initial agentDirectoryStreamProjection
	var initialEvent agentDirectoryStreamEvent
	if !scope.isScoped() {
		initial, initialEvent, err = agentDirectoryStreamSnapshot(configuration.DB, initialAt)
	} else {
		var scoped agentDirectoryResponse
		scoped, initialEvent, err = agentDirectoryStreamScopedSnapshot(c.Request().Context(), configuration.DB, initialAt, scope)
		initial.ActiveTaskCount = int64(scoped.Summary.ActiveRuns)
		initial.ActiveStepCount = 0
		initial.ActiveAssignmentCount = 0
		initial.QueuedTaskCount = int64(scoped.Summary.QueuedTasks)
	}
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Agent directory stream unavailable", "")
	}

	headers := response.Header()
	headers.Set(echo.HeaderContentType, "text/event-stream; charset=utf-8")
	headers.Set(echo.HeaderCacheControl, "private, no-store, no-cache, no-transform")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")
	headers.Set(echo.HeaderContentEncoding, "identity")
	headers.Set("Retry-After", "2")
	response.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(response.Writer, "retry: 2000\n\n"); err != nil {
		return nil
	}
	response.Writer.(http.Flusher).Flush()
	if err := writeAgentDirectoryStreamEvent(response.Writer, "snapshot", initialEvent); err != nil {
		return nil
	}

	streamContext, cancel := context.WithTimeout(c.Request().Context(), agentDirectoryStreamLifetime)
	defer cancel()
	pollInterval := agentDirectoryStreamIdlePollInterval
	if initial.ActiveTaskCount > 0 || initial.ActiveStepCount > 0 || initial.ActiveAssignmentCount > 0 {
		pollInterval = agentDirectoryStreamActivePollInterval
	}
	if scope.isScoped() {
		pollInterval = agentDirectoryStreamScopedIdlePollInterval
		if initial.ActiveTaskCount > 0 || initial.QueuedTaskCount > 0 {
			pollInterval = agentDirectoryStreamScopedActivePollInterval
		}
	}
	pollTimer := time.NewTimer(pollInterval)
	heartbeatTicker := time.NewTicker(agentDirectoryStreamHeartbeat)
	defer pollTimer.Stop()
	defer heartbeatTicker.Stop()
	currentFingerprint := initialEvent.fingerprint
	currentScope := scope
	for {
		select {
		case <-streamContext.Done():
			return nil
		case <-heartbeatTicker.C:
			// Re-run the same workspace, filters and project/client authorization
			// as GET /automation/agents so revoked membership closes the stream.
			authorizedScope, authorizedWorkspace, authorizationErr := resolveAgentDirectoryStreamScope(c, configuration.DB)
			if authorizationErr != nil || authorizedWorkspace != workspaceMode {
				return nil
			}
			currentScope = authorizedScope
			if err := writeAgentDirectoryStreamKeepalive(response.Writer); err != nil {
				return nil
			}
		case <-pollTimer.C:
			now := time.Now().UTC()
			var next agentDirectoryStreamProjection
			var nextEvent agentDirectoryStreamEvent
			var snapshotErr error
			if !currentScope.isScoped() {
				next, nextEvent, snapshotErr = agentDirectoryStreamSnapshot(configuration.DB, now)
			} else {
				var scoped agentDirectoryResponse
				scoped, nextEvent, snapshotErr = agentDirectoryStreamScopedSnapshot(streamContext, configuration.DB, now, currentScope)
				next.ActiveTaskCount = int64(scoped.Summary.ActiveRuns)
				next.ActiveStepCount = 0
				next.ActiveAssignmentCount = 0
				next.QueuedTaskCount = int64(scoped.Summary.QueuedTasks)
			}
			if snapshotErr != nil {
				// A reconnect repeats authentication and re-fetches the directory.
				return nil
			}
			if nextEvent.fingerprint != currentFingerprint {
				currentFingerprint = nextEvent.fingerprint
				if err := writeAgentDirectoryStreamEvent(response.Writer, "update", nextEvent); err != nil {
					return nil
				}
			}
			pollInterval = agentDirectoryStreamIdlePollInterval
			if next.ActiveTaskCount > 0 || next.ActiveStepCount > 0 || next.ActiveAssignmentCount > 0 {
				pollInterval = agentDirectoryStreamActivePollInterval
			}
			if currentScope.isScoped() {
				pollInterval = agentDirectoryStreamScopedIdlePollInterval
				if next.ActiveTaskCount > 0 || next.QueuedTaskCount > 0 {
					pollInterval = agentDirectoryStreamScopedActivePollInterval
				}
			}
			pollTimer.Reset(pollInterval)
		}
	}
}
