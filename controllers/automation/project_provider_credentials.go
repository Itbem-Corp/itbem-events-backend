package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"events-stocks/configuration"
	"events-stocks/internal/aicredentials"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/organizationscope"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const aiCredentialBundleAdvisoryLockKey = "itbem/ai-credential-bundle/v1"

// withAICredentialBundleWriteLock serializes whole-bundle Secrets Manager
// read/modify/write operations across API replicas. A process mutex alone
// cannot protect two controllers from overwriting each other's project keys.
// The transaction lock is released by PostgreSQL even if the request process
// exits while talking to Secrets Manager; it adds no cloud service or key.
func withAICredentialBundleWriteLock(ctx context.Context, write func() error) error {
	db := configuration.DB
	if db == nil || write == nil || db.Name() != "postgres" {
		return errors.New("AI credential bundle write lock is unavailable")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", aiCredentialBundleAdvisoryLockKey).Error; err != nil {
			return err
		}
		return write()
	})
}

type projectAPIKeyResolver interface {
	APIKeyForProject(context.Context, string, string) (string, error)
}

type projectCredentialResolver interface {
	projectAPIKeyResolver
	HasAPIKeyForProject(context.Context, string, string) (bool, error)
	ReplaceProjectAPIKey(context.Context, string, string, string) error
	DeleteProjectAPIKey(context.Context, string, string) error
}

// GetProjectProviderCredentialStatus returns only whether a project-specific
// credential exists. The central bundle itself is never exposed to project
// members, and a global/legacy provider key is not reported as project access.
func GetProjectProviderCredentialStatus(c echo.Context) error {
	projectID, provider, ok := parseProjectCredentialRoute(c)
	if !ok {
		return utils.Error(c, http.StatusBadRequest, "Invalid project provider credential", "project_id or provider is invalid")
	}
	if err := authorizeProjectCredential(c, projectID, false); err != nil || c.Response().Committed {
		return err
	}
	resolver, ok := inferenceCredentials.(projectCredentialResolver)
	if !ok || resolver == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Project AI credential storage unavailable", "")
	}
	configured, err := resolver.HasAPIKeyForProject(c.Request().Context(), projectID.String(), provider)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Project AI credential status unavailable", "")
	}
	status := "not_configured"
	if configured {
		status = "stored"
	}
	return utils.Success(c, http.StatusOK, "Project provider credential status", map[string]string{"project_id": projectID.String(), "provider": provider, "status": status})
}

// UpsertProjectProviderCredential writes only the provider entry inside the
// named project scope of the one central production bundle. Keys are accepted
// once and never returned; project membership is checked server-side.
func UpsertProjectProviderCredential(c echo.Context) error {
	projectID, provider, ok := parseProjectCredentialRoute(c)
	if !ok {
		return utils.Error(c, http.StatusBadRequest, "Invalid project provider credential", "project_id or provider is invalid")
	}
	if err := authorizeProjectCredential(c, projectID, true); err != nil || c.Response().Committed {
		return err
	}
	resolver, ok := inferenceCredentials.(projectCredentialResolver)
	if !ok || resolver == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Project AI credential storage unavailable", "")
	}
	request, err := decodeProviderCredentialRequest(c)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid project provider credential", "")
	}
	if err := withAICredentialBundleWriteLock(c.Request().Context(), func() error {
		return resolver.ReplaceProjectAPIKey(c.Request().Context(), projectID.String(), provider, request.APIKey)
	}); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Project provider credential could not be stored", "")
	}
	return utils.Success(c, http.StatusOK, "Project provider credential stored", map[string]string{"project_id": projectID.String(), "provider": provider, "status": "stored"})
}

// DeleteProjectProviderCredential removes only the selected project/provider
// key. It cannot affect a provider credential belonging to another project.
func DeleteProjectProviderCredential(c echo.Context) error {
	projectID, provider, ok := parseProjectCredentialRoute(c)
	if !ok {
		return utils.Error(c, http.StatusBadRequest, "Invalid project provider credential", "project_id or provider is invalid")
	}
	if err := authorizeProjectCredential(c, projectID, true); err != nil || c.Response().Committed {
		return err
	}
	resolver, ok := inferenceCredentials.(projectCredentialResolver)
	if !ok || resolver == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Project AI credential storage unavailable", "")
	}
	if err := withAICredentialBundleWriteLock(c.Request().Context(), func() error {
		return resolver.DeleteProjectAPIKey(c.Request().Context(), projectID.String(), provider)
	}); err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Project provider credential could not be removed", "")
	}
	return utils.Success(c, http.StatusOK, "Project provider credential removed", map[string]string{"project_id": projectID.String(), "provider": provider, "status": "not_configured"})
}

func parseProjectCredentialRoute(c echo.Context) (uuid.UUID, string, bool) {
	projectID, err := uuid.FromString(strings.TrimSpace(c.Param("projectId")))
	if err != nil || projectID == uuid.Nil {
		return uuid.Nil, "", false
	}
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	switch provider {
	case string(automationagent.ProviderMiniMax), string(automationagent.ProviderDeepSeek), string(automationagent.ProviderOpenRouter),
		string(automationagent.ProviderOpenAI), string(automationagent.ProviderAnthropic), string(automationagent.ProviderOpenCodeGo):
		return projectID, provider, true
	default:
		return uuid.Nil, "", false
	}
}

func decodeProviderCredentialRequest(c echo.Context) (providerCredentialRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, (16<<10)+1))
	if err != nil || len(raw) == 0 || len(raw) > 16<<10 {
		return providerCredentialRequest{}, errors.New("invalid provider credential payload")
	}
	if err := rejectDuplicateAPIKeyFields(raw); err != nil {
		return providerCredentialRequest{}, errors.New("invalid provider credential payload")
	}
	var request providerCredentialRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return providerCredentialRequest{}, errors.New("invalid provider credential payload")
	}
	return request, nil
}

func authorizeProjectCredential(c echo.Context, projectID uuid.UUID, requireManage bool) error {
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Project authorization unavailable", "")
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	var project models.DeliveryProject
	if err := configuration.DB.Select("id", "client_id").First(&project, "id = ?", projectID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return projectCredentialNotFound(c)
		}
		return utils.Error(c, http.StatusInternalServerError, "Project authorization unavailable", "")
	}
	// The selected workspace is part of resource authorization, not just a UI
	// filter. Intersect it before any bundle resolver is consulted, including
	// for platform administrators operating inside a customer workspace.
	workspaceMode, _ := c.Get("workspace_mode").(string)
	switch strings.ToLower(strings.TrimSpace(workspaceMode)) {
	case "organization":
		organizationID, ok := c.Get("organization_id").(uuid.UUID)
		if !ok || organizationID == uuid.Nil {
			return projectCredentialNotFound(c)
		}
		clientIDs, err := organizationscope.ClientIDs(configuration.DB, organizationID)
		if err != nil {
			if errors.Is(err, organizationscope.ErrOrganizationNotFound) {
				return projectCredentialNotFound(c)
			}
			return utils.Error(c, http.StatusInternalServerError, "Project authorization unavailable", "")
		}
		inWorkspace := false
		for _, clientID := range clientIDs {
			if project.ClientID == clientID {
				inWorkspace = true
				break
			}
		}
		if !inWorkspace {
			return projectCredentialNotFound(c)
		}
	case "platform":
		if !user.IsPlatformAdmin() {
			return projectCredentialNotFound(c)
		}
	default:
		return projectCredentialNotFound(c)
	}
	if user.IsPlatformAdmin() {
		return nil
	}
	var member models.DeliveryProjectMember
	if err := configuration.DB.Select("role", "permissions").Where("project_id = ? AND cognito_sub = ?", projectID, user.CognitoSub).First(&member).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusForbidden, "Project access denied", "You are not assigned to this project")
		}
		return utils.Error(c, http.StatusInternalServerError, "Project authorization unavailable", "")
	}
	if !requireManage {
		return nil
	}
	role := strings.ToLower(strings.TrimSpace(member.Role))
	if role == "owner" || role == "delivery_manager" || projectCredentialPermission(member.Permissions) {
		return nil
	}
	return utils.Error(c, http.StatusForbidden, "Project access denied", "Your project role cannot manage AI provider credentials")
}

func projectCredentialNotFound(c echo.Context) error {
	return utils.Error(c, http.StatusNotFound, "Project provider credential unavailable", "")
}

func projectCredentialPermission(raw string) bool {
	var permissions []string
	if json.Unmarshal([]byte(raw), &permissions) != nil {
		return false
	}
	for _, permission := range permissions {
		switch strings.ToLower(strings.TrimSpace(permission)) {
		case "ai:credentials:manage", "automation:credentials:manage":
			return true
		}
	}
	return false
}

var _ projectCredentialResolver = (*aicredentials.Resolver)(nil)
