package delivery

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultEpicPageSize   = 25
	maxEpicPageSize       = 100
	maxEpicCursorBytes    = 2048
	maxEpicTaskStateBytes = 32
	maxEpicUpdateBodySize = 16 * 1024
)

var (
	errEpicNotFound        = errors.New("delivery epic not found")
	errEpicWorkItemMissing = errors.New("delivery work item not found in this project")
	errEpicMembershipGone  = errors.New("delivery epic membership not found")
	errEpicAlreadyLinked   = errors.New("delivery work item is already assigned to another epic")
)

// deliveryEpicSummary is an API allow-list. In particular it never embeds a
// task, request, agent result, object reference, or prompt.
type deliveryEpicSummary struct {
	ID        uuid.UUID `json:"id"`
	ProjectID uuid.UUID `json:"project_id"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary,omitempty"`
	Status    string    `json:"status"`
	TaskCount int64     `json:"task_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type deliveryEpicPage struct {
	Items      []deliveryEpicSummary `json:"items"`
	Limit      int                   `json:"limit"`
	NextCursor string                `json:"next_cursor,omitempty"`
	CanManage  bool                  `json:"can_manage"`
}

type deliveryEpicTaskSummary struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	AddedAt   time.Time `json:"added_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type deliveryEpicTaskPage struct {
	Items      []deliveryEpicTaskSummary `json:"items"`
	Total      int64                     `json:"total"`
	Limit      int                       `json:"limit"`
	NextCursor string                    `json:"next_cursor,omitempty"`
}

type deliveryEpicDetail struct {
	Epic      deliveryEpicSummary  `json:"epic"`
	WorkItems deliveryEpicTaskPage `json:"work_items"`
	CanManage bool                 `json:"can_manage"`
}

type deliveryEpicAssociationDTO struct {
	EpicID     uuid.UUID `json:"epic_id"`
	ProjectID  uuid.UUID `json:"project_id"`
	WorkItemID uuid.UUID `json:"work_item_id"`
	AddedAt    time.Time `json:"added_at"`
}

type deliveryEpicListRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	ProjectID uuid.UUID `gorm:"column:project_id"`
	Title     string    `gorm:"column:title"`
	Summary   string    `gorm:"column:summary"`
	Status    string    `gorm:"column:status"`
	TaskCount int64     `gorm:"column:task_count"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type deliveryEpicTaskRow struct {
	MembershipID uuid.UUID `gorm:"column:membership_id"`
	AddedAt      time.Time `gorm:"column:added_at"`
	ID           uuid.UUID `gorm:"column:id"`
	Title        string    `gorm:"column:title"`
	State        string    `gorm:"column:state"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

type deliveryEpicCursor struct {
	Version   int       `json:"v"`
	Scope     string    `json:"s"`
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"i"`
}

type createDeliveryEpicInput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

type updateDeliveryEpicInput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
}

type associateDeliveryEpicWorkItemInput struct {
	WorkItemID string `json:"work_item_id"`
}

// ListEpics returns project-visible epics in a deterministic keyset order.
// The cursor is scoped to both project and status filter so it cannot be
// accidentally reused against a different project or filtered result set.
func ListEpics(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryView)
	if err != nil {
		return err
	}
	if err := requireEpicProject(c, projectID); err != nil {
		return err
	}
	canManage, err := projectCanManage(projectID, actor)
	if err != nil {
		return utilsError(c, err)
	}
	status := strings.ToLower(strings.TrimSpace(c.QueryParam("status")))
	if status != "" && !validDeliveryEpicStatus(status) {
		return badRequest(c, "Invalid epic status", "status is not a supported delivery epic state")
	}
	limit, err := epicPageSize(c.QueryParam("limit"))
	if err != nil {
		return badRequest(c, "Invalid epic page size", err.Error())
	}
	scope := epicListCursorScope(projectID, status)
	cursor, err := decodeDeliveryEpicCursor(c.QueryParam("cursor"), scope)
	if err != nil {
		return badRequest(c, "Invalid epic cursor", err.Error())
	}

	query := configuration.DB.Model(&models.DeliveryEpic{}).
		Select(deliveryEpicSummarySelect).
		Where("delivery_epics.project_id = ?", projectID)
	if status != "" {
		query = query.Where("delivery_epics.status = ?", status)
	}
	if cursor != nil {
		query = query.Where("(delivery_epics.created_at < ? OR (delivery_epics.created_at = ? AND delivery_epics.id < ?))", cursor.CreatedAt, cursor.CreatedAt, uuid.Must(uuid.FromString(cursor.ID)))
	}
	var rows []deliveryEpicListRow
	if err := query.Order("delivery_epics.created_at DESC, delivery_epics.id DESC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return utilsError(c, err)
	}
	page := deliveryEpicPage{Items: make([]deliveryEpicSummary, 0, limit), Limit: limit, CanManage: canManage}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodeDeliveryEpicCursor(deliveryEpicCursor{Version: 1, Scope: scope, CreatedAt: last.CreatedAt, ID: last.ID.String()})
	}
	for _, row := range rows {
		page.Items = append(page.Items, epicSummaryFromRow(row))
	}
	return success(c, "Delivery epics", page)
}

// CreateEpic creates an epic under an existing project. Requests are not
// rewritten or implicitly promoted to epics, preserving the legacy intake
// hierarchy and standalone-work-item behavior.
func CreateEpic(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryManage)
	if err != nil {
		return err
	}
	var input createDeliveryEpicInput
	if err := decodeDeliveryEpicJSON(c, &input); err != nil {
		return badRequest(c, "Invalid delivery epic", "body must contain only title and summary as valid JSON")
	}
	title := compactEpicText(input.Title)
	summary := strings.TrimSpace(input.Summary)
	if title == "" || len(title) > 180 {
		return badRequest(c, "Invalid delivery epic", "title is required and must be at most 180 bytes")
	}
	if len(summary) > 4000 {
		return badRequest(c, "Invalid delivery epic", "summary must be at most 4,000 bytes")
	}
	if containsEpicSensitiveMaterial(title) || containsEpicSensitiveMaterial(summary) {
		return badRequest(c, "Invalid delivery epic", "title and summary cannot contain credentials, tokens, or private keys")
	}

	epic := models.DeliveryEpic{ProjectID: projectID, Title: title, Summary: summary, Status: "planned", CreatedBy: actor.CognitoSub}
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		var project models.DeliveryProject
		if err := tx.Select("id").First(&project, "id = ?", projectID).Error; err != nil {
			return err
		}
		return tx.Create(&epic).Error
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return lookup(c, "Delivery project", err)
		}
		return utilsError(c, err)
	}
	return created(c, "Delivery epic created", epicSummaryFromModel(epic, 0))
}

// UpdateEpic changes only the user-facing epic context fields. The request is
// project-authorized, strictly decoded, bounded, and rejected when it appears
// to contain credentials so secret material is never persisted as context.
func UpdateEpic(c echo.Context) error {
	epicID, err := id(c, "epic")
	if err != nil {
		return err
	}
	epic, _, err := loadEpicForPermissionWithActor(c, epicID, deliveryManage)
	if err != nil {
		return err
	}
	var input updateDeliveryEpicInput
	if err := decodeDeliveryEpicUpdate(c, &input); err != nil {
		return badRequest(c, "Invalid delivery epic", "body must contain only title, summary, and status as valid JSON")
	}
	title := compactEpicText(input.Title)
	status := strings.ToLower(strings.TrimSpace(input.Status))
	summary := strings.TrimSpace(input.Summary)
	if title == "" || len(title) > 180 {
		return badRequest(c, "Invalid delivery epic", "title is required and must be at most 180 bytes")
	}
	if len(summary) > 4000 {
		return badRequest(c, "Invalid delivery epic", "summary must be at most 4,000 bytes")
	}
	if !validDeliveryEpicStatus(status) {
		return badRequest(c, "Invalid delivery epic", "status is not a supported delivery epic state")
	}
	if containsEpicSensitiveMaterial(title) || containsEpicSensitiveMaterial(summary) {
		return badRequest(c, "Invalid delivery epic", "title and summary cannot contain credentials, tokens, or private keys")
	}

	var updated models.DeliveryEpic
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var locked models.DeliveryEpic
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ?", epicID, epic.ProjectID).First(&locked).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.DeliveryEpic{}).
			Where("id = ? AND project_id = ?", epicID, epic.ProjectID).
			Updates(map[string]any{"title": title, "summary": summary, "status": status}).Error; err != nil {
			return err
		}
		locked.Title, locked.Summary, locked.Status = title, summary, status
		updated = locked
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return lookup(c, "Delivery epic", err)
		}
		return utilsError(c, err)
	}
	var taskCount int64
	if err := configuration.DB.Table("delivery_epic_work_items AS membership").
		Joins("JOIN delivery_work_items AS work_item ON work_item.id = membership.work_item_id AND work_item.project_id = membership.project_id AND work_item.deleted_at IS NULL").
		Where("membership.epic_id = ? AND membership.project_id = ? AND membership.deleted_at IS NULL", epicID, epic.ProjectID).
		Count(&taskCount).Error; err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery epic updated", epicSummaryFromModel(updated, taskCount))
}

// GetEpic returns safe epic metadata plus a separately paged task projection.
// Descriptions, mandates, agent prompts/results, object references, and raw
// JSON blobs are deliberately absent from both DTOs.
func GetEpic(c echo.Context) error {
	epicID, err := id(c, "epic")
	if err != nil {
		return err
	}
	epic, actor, err := loadEpicForPermissionWithActor(c, epicID, deliveryView)
	if err != nil {
		return err
	}
	canManage, err := projectCanManage(epic.ProjectID, actor)
	if err != nil {
		return utilsError(c, err)
	}
	limit, err := epicPageSize(c.QueryParam("tasks_limit"))
	if err != nil {
		return badRequest(c, "Invalid epic task page size", err.Error())
	}
	state, err := epicTaskStateFilter(c.QueryParams()["tasks_state"])
	if err != nil {
		return badRequest(c, "Invalid epic task state", err.Error())
	}
	scope := epicTaskCursorScope(epicID, state)
	cursor, err := decodeDeliveryEpicCursor(c.QueryParam("tasks_cursor"), scope)
	if err != nil {
		return badRequest(c, "Invalid epic task cursor", err.Error())
	}
	page, err := loadEpicWorkItemPage(epicID, epic.ProjectID, limit, state, cursor)
	if err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery epic", deliveryEpicDetail{Epic: epicSummaryFromModel(*epic, page.Total), WorkItems: page, CanManage: canManage})
}

// AddWorkItemToEpic associates exactly one existing task. The task row is
// locked before checking current membership, so two concurrent callers cannot
// put the same task in different epics. Project equality is checked inside
// that transaction rather than trusted from client input.
func AddWorkItemToEpic(c echo.Context) error {
	epicID, err := id(c, "epic")
	if err != nil {
		return err
	}
	epic, actor, err := loadEpicForPermissionWithActor(c, epicID, deliveryManage)
	if err != nil {
		return err
	}
	var input associateDeliveryEpicWorkItemInput
	if err := c.Bind(&input); err != nil {
		return badRequest(c, "Invalid epic task association", "work_item_id must be a UUID")
	}
	workItemID, err := uuid.FromString(strings.TrimSpace(input.WorkItemID))
	if err != nil || workItemID == uuid.Nil {
		return badRequest(c, "Invalid epic task association", "work_item_id must be a UUID")
	}
	var association models.DeliveryEpicWorkItem
	alreadyAssociated := false
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		var lockedEpic models.DeliveryEpic
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedEpic, "id = ?", epicID).Error; err != nil {
			return errEpicNotFound
		}
		if lockedEpic.ProjectID != epic.ProjectID {
			return errEpicNotFound
		}
		var item models.DeliveryWorkItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "project_id").
			Where("id = ? AND project_id = ?", workItemID, lockedEpic.ProjectID).First(&item).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errEpicWorkItemMissing
			}
			return err
		}
		if item.ProjectID != lockedEpic.ProjectID {
			return errEpicWorkItemMissing
		}
		var current models.DeliveryEpicWorkItem
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("work_item_id = ? AND deleted_at IS NULL", item.ID).
			Order("created_at DESC").First(&current).Error
		if err == nil {
			if current.EpicID != lockedEpic.ID || current.ProjectID != lockedEpic.ProjectID {
				return errEpicAlreadyLinked
			}
			association, alreadyAssociated = current, true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		association = models.DeliveryEpicWorkItem{ProjectID: lockedEpic.ProjectID, EpicID: lockedEpic.ID, WorkItemID: item.ID, CreatedBy: actor.CognitoSub}
		return tx.Create(&association).Error
	}); err != nil {
		switch {
		case errors.Is(err, errEpicNotFound):
			return lookup(c, "Delivery epic", gorm.ErrRecordNotFound)
		case errors.Is(err, errEpicWorkItemMissing):
			return lookup(c, "Delivery work item", gorm.ErrRecordNotFound)
		case errors.Is(err, errEpicAlreadyLinked):
			return conflict(c, "Delivery epic association rejected", "the work item already belongs to a different active epic")
		case errors.Is(err, gorm.ErrRecordNotFound):
			return lookup(c, "Delivery epic", err)
		default:
			return utilsError(c, err)
		}
	}
	dto := deliveryEpicAssociationDTO{EpicID: association.EpicID, ProjectID: association.ProjectID, WorkItemID: association.WorkItemID, AddedAt: association.CreatedAt}
	if alreadyAssociated {
		return success(c, "Delivery epic association already exists", dto)
	}
	return created(c, "Delivery work item added to epic", dto)
}

// RemoveWorkItemFromEpic retires an active association without deleting its
// row. Re-adding a task creates a fresh association record; work items remain
// standalone when no active association exists.
func RemoveWorkItemFromEpic(c echo.Context) error {
	epicID, err := id(c, "epic")
	if err != nil {
		return err
	}
	workItemID, err := uuid.FromString(strings.TrimSpace(c.Param("workItemID")))
	if err != nil || workItemID == uuid.Nil {
		return badRequest(c, "Invalid delivery work item", "work item id must be a UUID")
	}
	epic, _, err := loadEpicForPermissionWithActor(c, epicID, deliveryManage)
	if err != nil {
		return err
	}
	var association models.DeliveryEpicWorkItem
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		var lockedEpic models.DeliveryEpic
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedEpic, "id = ?", epicID).Error; err != nil {
			return errEpicNotFound
		}
		var item models.DeliveryWorkItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "project_id").
			Where("id = ? AND project_id = ?", workItemID, lockedEpic.ProjectID).First(&item).Error; err != nil {
			return errEpicWorkItemMissing
		}
		if lockedEpic.ProjectID != epic.ProjectID {
			return errEpicNotFound
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("epic_id = ? AND project_id = ? AND work_item_id = ? AND deleted_at IS NULL", epicID, lockedEpic.ProjectID, workItemID).
			First(&association).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errEpicMembershipGone
			}
			return err
		}
		return tx.Delete(&association).Error
	}); err != nil {
		switch {
		case errors.Is(err, errEpicNotFound):
			return lookup(c, "Delivery epic", gorm.ErrRecordNotFound)
		case errors.Is(err, errEpicWorkItemMissing), errors.Is(err, errEpicMembershipGone):
			return lookup(c, "Delivery epic task association", gorm.ErrRecordNotFound)
		default:
			return utilsError(c, err)
		}
	}
	return success(c, "Delivery work item removed from epic", deliveryEpicAssociationDTO{EpicID: association.EpicID, ProjectID: association.ProjectID, WorkItemID: association.WorkItemID, AddedAt: association.CreatedAt})
}

func loadEpicForPermission(c echo.Context, epicID uuid.UUID, permission deliveryPermission) (*models.DeliveryEpic, error) {
	epic, _, err := loadEpicForPermissionWithActor(c, epicID, permission)
	return epic, err
}

// projectCanManage exposes only the current actor's effective permission so
// clients can render controls without trying to identify themselves from the
// project member roster. Mutations still re-check projectActor at execution.
func projectCanManage(projectID uuid.UUID, actor *models.User) (bool, error) {
	if actor == nil {
		return false, errors.New("delivery actor is unavailable")
	}
	if actor.IsPlatformAdmin() {
		return true, nil
	}
	var member models.DeliveryProjectMember
	err := configuration.DB.Select("role", "permissions").
		Where("project_id = ? AND cognito_sub = ?", projectID, actor.CognitoSub).
		First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return memberAllows(member, deliveryManage), nil
}

func loadEpicForPermissionWithActor(c echo.Context, epicID uuid.UUID, permission deliveryPermission) (*models.DeliveryEpic, *models.User, error) {
	if configuration.DB == nil {
		return nil, nil, utils.Error(c, http.StatusServiceUnavailable, "Delivery unavailable", "Database is unavailable")
	}
	var epic models.DeliveryEpic
	if err := configuration.DB.First(&epic, "id = ?", epicID).Error; err != nil {
		return nil, nil, lookup(c, "Delivery epic", err)
	}
	actor, err := projectActor(c, epic.ProjectID, permission)
	if err != nil {
		return nil, nil, err
	}
	return &epic, actor, nil
}

func requireEpicProject(c echo.Context, projectID uuid.UUID) error {
	var project models.DeliveryProject
	if err := configuration.DB.Select("id").First(&project, "id = ?", projectID).Error; err != nil {
		return lookup(c, "Delivery project", err)
	}
	return nil
}

const deliveryEpicSummarySelect = `delivery_epics.id, delivery_epics.project_id, delivery_epics.title, delivery_epics.summary, delivery_epics.status, delivery_epics.created_at, delivery_epics.updated_at,
(SELECT COUNT(*) FROM delivery_epic_work_items AS membership
 JOIN delivery_work_items AS work_item ON work_item.id = membership.work_item_id AND work_item.project_id = delivery_epics.project_id AND work_item.deleted_at IS NULL
 WHERE membership.epic_id = delivery_epics.id AND membership.project_id = delivery_epics.project_id AND membership.deleted_at IS NULL) AS task_count`

func loadEpicWorkItemPage(epicID, projectID uuid.UUID, limit int, state string, cursor *deliveryEpicCursor) (deliveryEpicTaskPage, error) {
	page := deliveryEpicTaskPage{Items: make([]deliveryEpicTaskSummary, 0, limit), Limit: limit}
	countQuery := configuration.DB.Table("delivery_epic_work_items AS membership").
		Joins("JOIN delivery_work_items AS work_item ON work_item.id = membership.work_item_id AND work_item.project_id = membership.project_id AND work_item.deleted_at IS NULL").
		Where("membership.epic_id = ? AND membership.project_id = ? AND membership.deleted_at IS NULL", epicID, projectID)
	if state != "" {
		countQuery = countQuery.Where("work_item.state = ?", state)
	}
	if err := countQuery.Count(&page.Total).Error; err != nil {
		return page, err
	}
	query := configuration.DB.Table("delivery_epic_work_items AS membership").
		Select("membership.id AS membership_id, membership.created_at AS added_at, work_item.id AS id, work_item.title AS title, work_item.state AS state, work_item.created_at AS created_at, work_item.updated_at AS updated_at").
		Joins("JOIN delivery_work_items AS work_item ON work_item.id = membership.work_item_id AND work_item.project_id = membership.project_id AND work_item.deleted_at IS NULL").
		Where("membership.epic_id = ? AND membership.project_id = ? AND membership.deleted_at IS NULL", epicID, projectID)
	if state != "" {
		query = query.Where("work_item.state = ?", state)
	}
	if cursor != nil {
		query = query.Where("(membership.created_at < ? OR (membership.created_at = ? AND membership.id < ?))", cursor.CreatedAt, cursor.CreatedAt, uuid.Must(uuid.FromString(cursor.ID)))
	}
	var rows []deliveryEpicTaskRow
	if err := query.Order("membership.created_at DESC, membership.id DESC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return page, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodeDeliveryEpicCursor(deliveryEpicCursor{Version: 1, Scope: epicTaskCursorScope(epicID, state), CreatedAt: last.AddedAt, ID: last.MembershipID.String()})
	}
	for _, row := range rows {
		page.Items = append(page.Items, deliveryEpicTaskSummary{ID: row.ID, Title: row.Title, State: row.State, AddedAt: row.AddedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
	}
	return page, nil
}

func epicSummaryFromRow(row deliveryEpicListRow) deliveryEpicSummary {
	return deliveryEpicSummary{ID: row.ID, ProjectID: row.ProjectID, Title: row.Title, Summary: row.Summary, Status: row.Status, TaskCount: row.TaskCount, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func epicSummaryFromModel(epic models.DeliveryEpic, taskCount int64) deliveryEpicSummary {
	return deliveryEpicSummary{ID: epic.ID, ProjectID: epic.ProjectID, Title: epic.Title, Summary: epic.Summary, Status: epic.Status, TaskCount: taskCount, CreatedAt: epic.CreatedAt, UpdatedAt: epic.UpdatedAt}
}

func epicListCursorScope(projectID uuid.UUID, status string) string {
	return "project:" + projectID.String() + ":status:" + status
}

func epicTaskCursorScope(epicID uuid.UUID, state string) string {
	return "epic:" + epicID.String() + ":work-items:state:" + state
}

func epicTaskStateFilter(values []string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("tasks_state must appear at most once")
	}
	state := values[0]
	if len(state) > maxEpicTaskStateBytes {
		return "", fmt.Errorf("tasks_state must be at most %d bytes", maxEpicTaskStateBytes)
	}
	if state == "" {
		return "", nil
	}
	if !validDeliveryWorkItemState(state) {
		return "", fmt.Errorf("tasks_state is not a supported work-item state")
	}
	return state, nil
}

func validDeliveryWorkItemState(value string) bool {
	switch value {
	case deliveryworkflow.StatePlanning,
		deliveryworkflow.StatePlanReview,
		deliveryworkflow.StateImplementation,
		deliveryworkflow.StateCodeReview,
		deliveryworkflow.StatePreviewPending,
		deliveryworkflow.StateQARunning,
		deliveryworkflow.StateQAReview,
		deliveryworkflow.StateReleaseReview,
		deliveryworkflow.StateReleased,
		deliveryworkflow.StateBlocked,
		deliveryworkflow.StateCancelled:
		return true
	default:
		return false
	}
}

func encodeDeliveryEpicCursor(cursor deliveryEpicCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeDeliveryEpicCursor(raw, scope string) (*deliveryEpicCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxEpicCursorBytes {
		return nil, fmt.Errorf("cursor is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("cursor encoding is invalid")
	}
	var cursor deliveryEpicCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != 1 || cursor.Scope != scope || cursor.CreatedAt.IsZero() {
		return nil, fmt.Errorf("cursor is invalid or belongs to another result set")
	}
	id, err := uuid.FromString(cursor.ID)
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("cursor id must be a UUID")
	}
	cursor.CreatedAt = cursor.CreatedAt.UTC()
	return &cursor, nil
}

func epicPageSize(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultEpicPageSize, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 || value > maxEpicPageSize {
		return 0, fmt.Errorf("limit must be between 1 and %d", maxEpicPageSize)
	}
	return value, nil
}

func compactEpicText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func decodeDeliveryEpicUpdate(c echo.Context, input *updateDeliveryEpicInput) error {
	return decodeDeliveryEpicJSON(c, input)
}

func decodeDeliveryEpicJSON(c echo.Context, input any) error {
	request := c.Request()
	request.Body = http.MaxBytesReader(c.Response(), request.Body, maxEpicUpdateBodySize)
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxEpicUpdateBodySize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

var epicSensitiveMaterialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:api[_ -]?key|secret(?:[_ -]?key)?|token|password|credential|authorization|bearer|private[_ -]?key|access[_ -]?key)\s*[:=]`),
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]{12,}={0,2}`),
	regexp.MustCompile(`(?i)-----BEGIN(?: [A-Z0-9]+)* PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\b(?:sk|rk|pk|gh[pousr])[-_][a-z0-9_-]{16,}\b|\bAKIA[A-Z0-9]{16}\b|\bgithub_pat_[a-z0-9_]{20,}\b|\bglpat-[a-z0-9_-]{20,}\b|\bxox[baprs]-[a-z0-9-]{10,}\b|\bAIza[0-9A-Za-z_-]{30,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
	regexp.MustCompile(`(?i)(?:https?|ssh)://[^/\s:@]+:[^/\s@]+@`),
	regexp.MustCompile(`(?i)[?&](?:api[_-]?key|key|access[_-]?token|auth[_-]?token|client[_-]?secret|password|token|secret|authorization)=([^&#\s]+)`),
}

func containsEpicSensitiveMaterial(value string) bool {
	for _, pattern := range epicSensitiveMaterialPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func validDeliveryEpicStatus(value string) bool {
	switch value {
	case "planned", "active", "blocked", "completed", "cancelled", "archived":
		return true
	default:
		return false
	}
}
