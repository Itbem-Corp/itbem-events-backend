package automation

import (
	"errors"
	"strings"

	"events-stocks/configuration"
	deliverycontroller "events-stocks/controllers/delivery"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

var errAutomationHealthWorkspaceUnavailable = errors.New("automation health workspace unavailable")

type automationHealthWorkspace struct {
	Mode                   string
	ClientIDs              []uuid.UUID
	AuthorizedProjectIDs   []uuid.UUID
	AuthorizedProjectsOnly bool
	PlatformTelemetry      bool
}

func resolveAutomationHealthWorkspace(c echo.Context, user *models.User) (automationHealthWorkspace, error) {
	mode, _ := c.Get("workspace_mode").(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "platform":
		if user == nil || !user.IsPlatformAdmin() {
			return automationHealthWorkspace{}, errAutomationHealthWorkspaceUnavailable
		}
		if selected := c.Get("organization_id"); selected != nil {
			organizationID, ok := selected.(uuid.UUID)
			if !ok || organizationID != uuid.Nil {
				return automationHealthWorkspace{}, errAutomationHealthWorkspaceUnavailable
			}
		}
		return automationHealthWorkspace{Mode: mode, PlatformTelemetry: true}, nil
	case "organization":
		organizationID, hasOrganizationID := c.Get("organization_id").(uuid.UUID)
		clientIDs, err := automationCostWorkspaceClientIDs(configuration.DB, mode, organizationID, hasOrganizationID)
		if err != nil {
			return automationHealthWorkspace{}, err
		}
		workspace := automationHealthWorkspace{Mode: mode, ClientIDs: clientIDs}
		if user == nil || !user.IsPlatformAdmin() {
			projectIDs, err := deliverycontroller.VisibleProjectIDsForActor(c, user, clientIDs)
			if err != nil {
				return automationHealthWorkspace{}, err
			}
			workspace.AuthorizedProjectsOnly = true
			workspace.AuthorizedProjectIDs = projectIDs
		}
		return workspace, nil
	default:
		return automationHealthWorkspace{}, errAutomationHealthWorkspaceUnavailable
	}
}

func applyAutomationHealthTaskWorkspaceScope(query *gorm.DB, workspace automationHealthWorkspace) (*gorm.DB, error) {
	switch workspace.Mode {
	case "platform":
		if !workspace.PlatformTelemetry {
			return query, errAutomationHealthWorkspaceUnavailable
		}
		return query, nil
	case "organization":
		if len(workspace.ClientIDs) == 0 {
			return query, errAutomationHealthWorkspaceUnavailable
		}
		query = query.
			Joins("JOIN delivery_work_items AS health_work_item ON health_work_item.id = automation_tasks.delivery_work_item_id AND health_work_item.deleted_at IS NULL").
			Joins("JOIN delivery_projects AS health_project ON health_project.id = health_work_item.project_id AND health_project.deleted_at IS NULL").
			Where("health_project.client_id IN ?", workspace.ClientIDs)
		if workspace.AuthorizedProjectsOnly {
			if len(workspace.AuthorizedProjectIDs) == 0 {
				query = query.Where("1 = 0")
			} else {
				query = query.Where("health_project.id IN ?", workspace.AuthorizedProjectIDs)
			}
		}
		return query, nil
	default:
		return query, errAutomationHealthWorkspaceUnavailable
	}
}

func applyAutomationHealthSpendWorkspaceScope(query *gorm.DB, workspace automationHealthWorkspace) (*gorm.DB, error) {
	switch workspace.Mode {
	case "platform":
		if !workspace.PlatformTelemetry {
			return query, errAutomationHealthWorkspaceUnavailable
		}
		return query, nil
	case "organization":
		if len(workspace.ClientIDs) == 0 {
			return query, errAutomationHealthWorkspaceUnavailable
		}
		query = query.
			Joins("JOIN delivery_work_items AS health_work_item ON health_work_item.id = execution.delivery_work_item_id AND health_work_item.deleted_at IS NULL").
			Joins("JOIN delivery_projects AS health_project ON health_project.id = health_work_item.project_id AND health_project.deleted_at IS NULL").
			Where("health_project.client_id IN ?", workspace.ClientIDs)
		if workspace.AuthorizedProjectsOnly {
			if len(workspace.AuthorizedProjectIDs) == 0 {
				query = query.Where("1 = 0")
			} else {
				query = query.Where("health_project.id IN ?", workspace.AuthorizedProjectIDs)
			}
		}
		return query, nil
	default:
		return query, errAutomationHealthWorkspaceUnavailable
	}
}
