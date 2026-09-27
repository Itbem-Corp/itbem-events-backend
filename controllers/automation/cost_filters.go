package automation

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"events-stocks/internal/organizationscope"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

var automationCostModelFilterPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
var automationCostPositiveIntegerPattern = regexp.MustCompile(`^[0-9]{1,9}$`)
var automationCostStepKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

const (
	defaultAutomationCostWorkItemLimit = 20
	maxAutomationCostWorkItemLimit     = 100
	maxAutomationCostWorkItemCursor    = 1024
)

func setAutomationCostNoStoreHeaders(c echo.Context) {
	c.Response().Header().Set(echo.HeaderCacheControl, "private, no-store")
}

type automationCostQuery struct {
	Days                int
	FromAt              time.Time
	ToAt                time.Time
	Page                int
	PageSize            int
	WorkItemLimit       int
	WorkItemCursor      *automationCostWorkItemCursor
	WorkItemCursorToken string
	ClientID            *uuid.UUID
	ProjectID           *uuid.UUID
	EpicID              *uuid.UUID
	WorkItemID          *uuid.UUID
	AgentInstanceID     *uuid.UUID
	AgentKey            string
	StepKey             string
	Provider            string
	Model               string
	Cursor              *automationCostCursor
	WorkspaceClientIDs  []uuid.UUID
	SnapshotAt          time.Time
}

type automationCostCursor struct {
	SnapshotAt  time.Time `json:"snapshot_at"`
	CompletedAt time.Time `json:"completed_at"`
	ID          uuid.UUID `json:"id"`
	ScopeHash   string    `json:"scope_hash"`
}

type automationCostWorkItemCursor struct {
	Version         int       `json:"v"`
	SnapshotAt      time.Time `json:"snapshot_at"`
	TotalCostMicros int64     `json:"total_cost_microusd"`
	WorkItemID      uuid.UUID `json:"work_item_id"`
	ScopeHash       string    `json:"scope_hash"`
}

func automationCostCursorScope(filters automationCostQuery, workspaceMode string, organizationID uuid.UUID, hasOrganizationID bool, actorSub string) string {
	clientID, projectID, epicID, workItemID, agentInstanceID, organization := "", "", "", "", "", ""
	if filters.ClientID != nil {
		clientID = filters.ClientID.String()
	}
	if filters.ProjectID != nil {
		projectID = filters.ProjectID.String()
	}
	if filters.EpicID != nil {
		epicID = filters.EpicID.String()
	}
	if filters.WorkItemID != nil {
		workItemID = filters.WorkItemID.String()
	}
	if filters.AgentInstanceID != nil {
		agentInstanceID = filters.AgentInstanceID.String()
	}
	if hasOrganizationID {
		organization = organizationID.String()
	}
	clientIDs := make([]string, 0, len(filters.WorkspaceClientIDs))
	for _, id := range filters.WorkspaceClientIDs {
		clientIDs = append(clientIDs, id.String())
	}
	sort.Strings(clientIDs)
	fields := []string{strconv.Itoa(filters.Days), automationCostTimeScopeValue(filters.FromAt), automationCostTimeScopeValue(filters.ToAt), strconv.Itoa(filters.PageSize), clientID, strings.Join(clientIDs, ","), projectID, epicID, workItemID}
	if agentInstanceID != "" {
		fields = append(fields, agentInstanceID)
	}
	snapshotAt := ""
	if !filters.SnapshotAt.IsZero() {
		snapshotAt = filters.SnapshotAt.UTC().Format(time.RFC3339Nano)
	}
	fields = append(fields, filters.AgentKey, filters.StepKey, strings.ToLower(filters.Provider), strings.ToLower(filters.Model), strings.ToLower(strings.TrimSpace(workspaceMode)), organization, actorSub, snapshotAt)
	canonical, _ := json.Marshal(fields)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func automationCostWorkItemCursorScope(filters automationCostQuery, workspaceMode string, organizationID uuid.UUID, hasOrganizationID bool, actorSub string) string {
	clientID, projectID, epicID, workItemID, agentInstanceID, organization := "", "", "", "", "", ""
	if filters.ClientID != nil {
		clientID = filters.ClientID.String()
	}
	if filters.ProjectID != nil {
		projectID = filters.ProjectID.String()
	}
	if filters.EpicID != nil {
		epicID = filters.EpicID.String()
	}
	if filters.WorkItemID != nil {
		workItemID = filters.WorkItemID.String()
	}
	if filters.AgentInstanceID != nil {
		agentInstanceID = filters.AgentInstanceID.String()
	}
	if hasOrganizationID {
		organization = organizationID.String()
	}
	clientIDs := make([]string, 0, len(filters.WorkspaceClientIDs))
	for _, id := range filters.WorkspaceClientIDs {
		clientIDs = append(clientIDs, id.String())
	}
	sort.Strings(clientIDs)
	fields := []string{strconv.Itoa(filters.Days), automationCostTimeScopeValue(filters.FromAt), automationCostTimeScopeValue(filters.ToAt), strconv.Itoa(filters.WorkItemLimit), clientID, strings.Join(clientIDs, ","), projectID, epicID, workItemID}
	if agentInstanceID != "" {
		fields = append(fields, agentInstanceID)
	}
	snapshotAt := ""
	if !filters.SnapshotAt.IsZero() {
		snapshotAt = filters.SnapshotAt.UTC().Format(time.RFC3339Nano)
	}
	fields = append(fields, filters.AgentKey, filters.StepKey, strings.ToLower(filters.Provider), strings.ToLower(filters.Model), strings.ToLower(strings.TrimSpace(workspaceMode)), organization, actorSub, snapshotAt)
	canonical, _ := json.Marshal(fields)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// applyAutomationCostWorkspaceScope intersects every cost query with the
// authenticated workspace. Organization mode must never fall back to the
// actor's global project memberships; platform mode is reserved for platform
// administrators. The caller should return a generic not-found response when
// this fails so workspace/resource existence is not disclosed.
func automationCostWorkspaceClientIDs(db *gorm.DB, workspaceMode string, organizationID uuid.UUID, hasOrganizationID bool) ([]uuid.UUID, error) {
	if !strings.EqualFold(strings.TrimSpace(workspaceMode), "organization") {
		return nil, nil
	}
	if !hasOrganizationID || organizationID == uuid.Nil {
		return nil, organizationscope.ErrOrganizationNotFound
	}
	return organizationscope.ClientIDs(db, organizationID)
}

func applyAutomationCostWorkspaceScope(query *gorm.DB, workspaceMode string, organizationID uuid.UUID, hasOrganizationID, isPlatformAdmin bool, clientIDs []uuid.UUID) (*gorm.DB, error) {
	switch strings.ToLower(strings.TrimSpace(workspaceMode)) {
	case "organization":
		if !hasOrganizationID || organizationID == uuid.Nil || len(clientIDs) == 0 {
			return query, fmt.Errorf("organization workspace is missing its organization")
		}
		return query.Where("project.client_id IN ?", clientIDs), nil
	case "platform":
		if !isPlatformAdmin {
			return query, fmt.Errorf("platform workspace requires a platform administrator")
		}
		return query, nil
	default:
		return query, fmt.Errorf("automation cost workspace is invalid")
	}
}

func applyAutomationCostActorScope(query *gorm.DB, requestedBy string, projectIDs []uuid.UUID, isPlatformAdmin bool) *gorm.DB {
	if isPlatformAdmin {
		return query
	}
	if len(projectIDs) > 0 {
		// Parentheses are security-significant: the owner fallback must still be
		// intersected with the active organization below.
		return query.Where("(task.requested_by = ? OR work_item.project_id IN ?)", requestedBy, projectIDs)
	}
	return query.Where("task.requested_by = ?", requestedBy)
}

func parseAutomationCostQuery(c echo.Context) (automationCostQuery, error) {
	query := automationCostQuery{Days: 30, Page: 1, PageSize: 40, WorkItemLimit: defaultAutomationCostWorkItemLimit}
	if raw := strings.TrimSpace(c.QueryParam("days")); raw != "" {
		days, err := strconv.Atoi(raw)
		if err != nil || days < 1 || days > 365 {
			return query, fmt.Errorf("days must be from 1 to 365")
		}
		query.Days = days
	}
	fromValues, toValues := c.QueryParams()["from_at"], c.QueryParams()["to_at"]
	if len(fromValues) > 1 || len(toValues) > 1 {
		return query, fmt.Errorf("from_at and to_at must each appear once")
	}
	if (len(fromValues) == 1) != (len(toValues) == 1) {
		return query, fmt.Errorf("from_at and to_at must be provided together")
	}
	if len(fromValues) == 1 {
		fromAt, fromErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(fromValues[0]))
		toAt, toErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(toValues[0]))
		if fromErr != nil || toErr != nil || !toAt.After(fromAt) {
			return query, fmt.Errorf("from_at and to_at must be valid timestamps with to_at after from_at")
		}
		if toAt.Sub(fromAt) > 368*24*time.Hour {
			return query, fmt.Errorf("custom cost range cannot exceed 368 days")
		}
		query.FromAt = fromAt.UTC().Truncate(time.Microsecond)
		query.ToAt = toAt.UTC().Truncate(time.Microsecond)
	}
	snapshotValues := c.QueryParams()["snapshot_at"]
	if len(snapshotValues) > 1 {
		return query, fmt.Errorf("snapshot_at must appear once")
	}
	if raw := strings.TrimSpace(c.QueryParam("snapshot_at")); raw != "" {
		snapshotAt, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil || snapshotAt.After(time.Now().UTC().Add(time.Minute)) {
			return query, fmt.Errorf("snapshot_at must be a valid, non-future timestamp")
		}
		query.SnapshotAt = snapshotAt.UTC().Truncate(time.Microsecond)
	}
	if raw := strings.TrimSpace(c.QueryParam("page")); raw != "" {
		page, err := strconv.Atoi(raw)
		if err != nil || page < 1 || page > 10000 {
			return query, fmt.Errorf("page must be from 1 to 10000; use cursor pagination for deeper history")
		}
		query.Page = page
	}
	if raw := strings.TrimSpace(c.QueryParam("page_size")); raw != "" {
		pageSize, err := strconv.Atoi(raw)
		if err != nil || pageSize < 1 || pageSize > 100 {
			return query, fmt.Errorf("page_size must be from 1 to 100")
		}
		query.PageSize = pageSize
	}
	workItemLimitValues := c.QueryParams()["work_item_limit"]
	if len(workItemLimitValues) > 1 {
		return query, fmt.Errorf("work_item_limit must appear once")
	}
	if len(workItemLimitValues) == 1 {
		limit, err := parseAutomationCostPageLimit(workItemLimitValues[0], defaultAutomationCostWorkItemLimit, maxAutomationCostWorkItemLimit)
		if err != nil {
			return query, fmt.Errorf("work_item_limit must be from 1 to %d", maxAutomationCostWorkItemLimit)
		}
		query.WorkItemLimit = limit
	}
	for name, destination := range map[string]**uuid.UUID{"client_id": &query.ClientID, "project_id": &query.ProjectID, "epic_id": &query.EpicID, "work_item_id": &query.WorkItemID} {
		values := c.QueryParams()[name]
		if len(values) > 1 {
			return query, fmt.Errorf("%s must appear once", name)
		}
		raw := ""
		if len(values) == 1 {
			raw = strings.TrimSpace(values[0])
		}
		if raw == "" {
			continue
		}
		parsed, err := uuid.FromString(raw)
		if err != nil || parsed == uuid.Nil {
			return query, fmt.Errorf("%s must be a valid identifier", name)
		}
		*destination = &parsed
	}
	if raw := strings.TrimSpace(c.QueryParam("agent_instance_id")); raw != "" {
		parsed, err := uuid.FromString(raw)
		if err != nil || parsed == uuid.Nil {
			return query, fmt.Errorf("agent_instance_id must be a valid identifier")
		}
		query.AgentInstanceID = &parsed
	}
	query.AgentKey = strings.TrimSpace(c.QueryParam("agent_key"))
	if query.AgentKey != "" && !agentProfileKeyPattern.MatchString(query.AgentKey) {
		return query, fmt.Errorf("agent_key is invalid")
	}
	query.StepKey = strings.TrimSpace(c.QueryParam("step_key"))
	if query.StepKey != "" && !automationCostStepKeyPattern.MatchString(query.StepKey) {
		return query, fmt.Errorf("step_key is invalid")
	}
	query.Provider = strings.ToLower(strings.TrimSpace(c.QueryParam("provider")))
	if query.Provider != "" && !providerAllowed(query.Provider) {
		return query, fmt.Errorf("provider is invalid")
	}
	query.Model = strings.TrimSpace(c.QueryParam("model"))
	if query.Model != "" && !automationCostModelFilterPattern.MatchString(query.Model) {
		return query, fmt.Errorf("model is invalid")
	}
	if raw := strings.TrimSpace(c.QueryParam("cursor")); raw != "" {
		cursor, err := decodeAutomationCostCursor(raw)
		if err != nil {
			return query, fmt.Errorf("cursor is invalid")
		}
		query.Cursor = &cursor
	}
	workItemCursorValues := c.QueryParams()["work_item_cursor"]
	if len(workItemCursorValues) > 1 {
		return query, fmt.Errorf("work_item_cursor must appear once")
	}
	if len(workItemCursorValues) == 1 {
		query.WorkItemCursorToken = strings.TrimSpace(workItemCursorValues[0])
		if query.WorkItemCursorToken != "" {
			cursor, err := decodeAutomationCostWorkItemCursor(query.WorkItemCursorToken)
			if err != nil {
				return query, fmt.Errorf("work_item_cursor is invalid")
			}
			query.WorkItemCursor = &cursor
		}
	}
	return query, nil
}

// applyAutomationCostTimeWindow gives explicit RFC3339 date windows priority
// over the relative-days preset. `to_at` is an exclusive boundary; the
// snapshot cap is inclusive by adding one database-precision microsecond.
func applyAutomationCostTimeWindow(query *gorm.DB, snapshotAt time.Time, filters automationCostQuery) *gorm.DB {
	snapshotAt = snapshotAt.UTC().Truncate(time.Microsecond)
	startAt := snapshotAt.AddDate(0, 0, -filters.Days)
	endExclusive := snapshotAt.Add(time.Microsecond)
	if !filters.FromAt.IsZero() && !filters.ToAt.IsZero() {
		startAt = filters.FromAt.UTC().Truncate(time.Microsecond)
		endExclusive = filters.ToAt.UTC().Truncate(time.Microsecond)
		if snapshotCap := snapshotAt.Add(time.Microsecond); endExclusive.After(snapshotCap) {
			endExclusive = snapshotCap
		}
	}
	return query.
		Where("execution.completed_at >= ?", startAt).
		Where("execution.completed_at < ?", endExclusive).
		Where("execution.created_at <= ?", snapshotAt)
}

func automationCostAppliedTimestamp(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC().Truncate(time.Microsecond)
	return &value
}

func automationCostTimeScopeValue(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseAutomationCostPageLimit(raw string, defaultLimit, maxLimit int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultLimit, nil
	}
	if !automationCostPositiveIntegerPattern.MatchString(raw) {
		return 0, fmt.Errorf("page limit is invalid")
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxLimit {
		return 0, fmt.Errorf("page limit is out of bounds")
	}
	return limit, nil
}

func applyAutomationCostFilters(query *gorm.DB, filters automationCostQuery) *gorm.DB {
	if filters.ClientID != nil {
		query = query.Where("project.client_id = ?", *filters.ClientID)
	}
	if filters.ProjectID != nil {
		query = query.Where("work_item.project_id = ?", *filters.ProjectID)
	}
	if filters.EpicID != nil {
		// Epic memberships are soft-deleted history. Attribute each call to the
		// project-scoped membership interval active at completion, not today's epic.
		query = query.Where("EXISTS (SELECT 1 FROM delivery_epic_work_items AS epic_membership WHERE epic_membership.work_item_id = execution.delivery_work_item_id AND epic_membership.project_id = work_item.project_id AND epic_membership.epic_id = ? AND epic_membership.created_at <= execution.completed_at AND (epic_membership.deleted_at IS NULL OR epic_membership.deleted_at > execution.completed_at))", *filters.EpicID)
	}
	if filters.WorkItemID != nil {
		query = query.Where("execution.delivery_work_item_id = ?", *filters.WorkItemID)
	}
	if filters.AgentKey != "" {
		query = query.Where("execution.agent_key = ?", filters.AgentKey)
	}
	if filters.AgentInstanceID != nil {
		query = query.Where("execution.agent_instance_id = ?", *filters.AgentInstanceID)
	}
	if filters.Provider != "" {
		query = query.Where("LOWER(execution.provider) = ?", filters.Provider)
	}
	if filters.Model != "" {
		query = query.Where("LOWER(execution.model) = LOWER(?)", filters.Model)
	}
	if filters.StepKey != "" {
		query = query.Where("execution.step_key = ?", filters.StepKey)
	}
	return query
}

func applyAutomationCostCursor(query *gorm.DB, cursor *automationCostCursor) *gorm.DB {
	if cursor == nil {
		return query
	}
	return query.Where("(execution.completed_at < ?) OR (execution.completed_at = ? AND execution.id < ?)", cursor.CompletedAt, cursor.CompletedAt, cursor.ID)
}

func applyAutomationCostWorkItemCursor(query *gorm.DB, cursor *automationCostWorkItemCursor) *gorm.DB {
	if cursor == nil {
		return query
	}
	return query.Having(
		"SUM(execution.total_cost_micros) < ? OR (SUM(execution.total_cost_micros) = ? AND work_item.id > ?)",
		cursor.TotalCostMicros, cursor.TotalCostMicros, cursor.WorkItemID,
	)
}

func encodeAutomationCostWorkItemCursor(cursor automationCostWorkItemCursor) (string, error) {
	if cursor.SnapshotAt.IsZero() || cursor.SnapshotAt.After(time.Now().UTC().Add(time.Minute)) || cursor.WorkItemID == uuid.Nil || cursor.TotalCostMicros < 0 {
		return "", fmt.Errorf("work item cost cursor source is incomplete")
	}
	if len(cursor.ScopeHash) != sha256.Size*2 {
		return "", fmt.Errorf("work item cost cursor scope is incomplete")
	}
	if _, err := hex.DecodeString(cursor.ScopeHash); err != nil {
		return "", fmt.Errorf("work item cost cursor scope is invalid")
	}
	cursor.Version = 1
	cursor.SnapshotAt = cursor.SnapshotAt.UTC().Truncate(time.Microsecond)
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeAutomationCostWorkItemCursor(raw string) (automationCostWorkItemCursor, error) {
	if len(raw) > maxAutomationCostWorkItemCursor {
		return automationCostWorkItemCursor{}, fmt.Errorf("work item cost cursor is too large")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 {
		return automationCostWorkItemCursor{}, fmt.Errorf("work item cost cursor encoding is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor automationCostWorkItemCursor
	if err := decoder.Decode(&cursor); err != nil {
		return automationCostWorkItemCursor{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return automationCostWorkItemCursor{}, fmt.Errorf("work item cost cursor contains trailing data")
	}
	if cursor.Version != 1 || cursor.SnapshotAt.IsZero() || cursor.SnapshotAt.After(time.Now().UTC().Add(time.Minute)) || cursor.WorkItemID == uuid.Nil || cursor.TotalCostMicros < 0 || len(cursor.ScopeHash) != sha256.Size*2 {
		return automationCostWorkItemCursor{}, fmt.Errorf("work item cost cursor is incomplete")
	}
	cursor.SnapshotAt = cursor.SnapshotAt.UTC().Truncate(time.Microsecond)
	if _, err := hex.DecodeString(cursor.ScopeHash); err != nil {
		return automationCostWorkItemCursor{}, fmt.Errorf("work item cost cursor scope is invalid")
	}
	return cursor, nil
}

func encodeAutomationCostCursor(execution automationCostExecution, scopeHash string, snapshotAt time.Time) (string, error) {
	if execution.ID == uuid.Nil || execution.CompletedAt.IsZero() || snapshotAt.IsZero() || snapshotAt.After(time.Now().UTC().Add(time.Minute)) {
		return "", fmt.Errorf("cost execution cursor source is incomplete")
	}
	if len(scopeHash) != sha256.Size*2 {
		return "", fmt.Errorf("cost execution cursor scope is incomplete")
	}
	if _, err := hex.DecodeString(scopeHash); err != nil {
		return "", fmt.Errorf("cost execution cursor scope is invalid")
	}
	raw, err := json.Marshal(automationCostCursor{SnapshotAt: snapshotAt.UTC().Truncate(time.Microsecond), CompletedAt: execution.CompletedAt.UTC(), ID: execution.ID, ScopeHash: scopeHash})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeAutomationCostCursor(raw string) (automationCostCursor, error) {
	if len(raw) > 1024 {
		return automationCostCursor{}, fmt.Errorf("cursor is too large")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 {
		return automationCostCursor{}, fmt.Errorf("cursor encoding is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor automationCostCursor
	if err := decoder.Decode(&cursor); err != nil {
		return automationCostCursor{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return automationCostCursor{}, fmt.Errorf("cursor contains trailing data")
	}
	if cursor.ID == uuid.Nil || cursor.SnapshotAt.IsZero() || cursor.SnapshotAt.After(time.Now().UTC().Add(time.Minute)) || cursor.CompletedAt.IsZero() || cursor.CompletedAt.After(cursor.SnapshotAt) || len(cursor.ScopeHash) != sha256.Size*2 {
		return automationCostCursor{}, fmt.Errorf("cursor is incomplete")
	}
	if _, err := hex.DecodeString(cursor.ScopeHash); err != nil {
		return automationCostCursor{}, fmt.Errorf("cursor scope is invalid")
	}
	cursor.CompletedAt = cursor.CompletedAt.UTC()
	cursor.SnapshotAt = cursor.SnapshotAt.UTC().Truncate(time.Microsecond)
	return cursor, nil
}
