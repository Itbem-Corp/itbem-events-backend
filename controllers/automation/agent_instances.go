package automation

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type agentInstanceRegistrationRequest struct {
	AgentKey  string `json:"agent_key"`
	MachineID string `json:"machine_id"`
	PublicKey string `json:"public_key"`
}

type agentInstanceDTO struct {
	ID                   string     `json:"id"`
	AgentKey             string     `json:"agent_key"`
	MachineID            string     `json:"machine_id,omitempty"`
	PublicKeyFingerprint string     `json:"public_key_fingerprint"`
	Status               string     `json:"status"`
	CreatedAt            time.Time  `json:"created_at"`
	LastSeenAt           *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt            *time.Time `json:"revoked_at,omitempty"`
}

type agentInstanceCursor struct {
	Version   int       `json:"version"`
	Scope     string    `json:"scope"`
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

// RegisterAgentInstance enrolls only a public key. The private key never
// crosses this API, and the key fingerprint is safe to show in the UI.
func RegisterAgentInstance(c echo.Context) error {
	if err := requireAgentPlatformWorkspace(c, true); err != nil {
		return authz.Respond(c, err)
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Agent registry unavailable", "")
	}
	var request agentInstanceRegistrationRequest
	if err := decodeAgentInstanceRequest(c.Request().Body, &request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent instance", "")
	}
	request.AgentKey = strings.TrimSpace(request.AgentKey)
	request.MachineID = strings.TrimSpace(request.MachineID)
	request.PublicKey = strings.TrimSpace(request.PublicKey)
	if !agentProfileKeyPattern.MatchString(request.AgentKey) {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent instance", "")
	}
	machineID, err := uuid.FromString(request.MachineID)
	if err != nil || machineID == uuid.Nil || machineID.String() != request.MachineID {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent instance", "")
	}
	publicKey, err := agentcallbackauth.DecodePublicKey(request.PublicKey)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent instance", "")
	}
	fingerprint, err := agentcallbackauth.PublicKeyFingerprint(publicKey)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent instance", "")
	}
	profile, err := findActiveAgentProfile(configuration.DB, request.AgentKey)
	if err != nil || profile == nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent instance", "The agent profile is unavailable")
	}

	var instance models.AutomationAgentInstance
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var existing models.AutomationAgentInstance
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("agent_key = ? AND machine_id = ? AND status = ?", request.AgentKey, request.MachineID, "active").First(&existing).Error
		if err == nil {
			if existing.PublicKey != request.PublicKey {
				return errAgentInstanceKeyConflict
			}
			instance = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		instance = models.AutomationAgentInstance{AgentKey: request.AgentKey, MachineID: request.MachineID, PublicKey: request.PublicKey, PublicKeyFingerprint: fingerprint, Status: "active"}
		return tx.Create(&instance).Error
	})
	if err != nil {
		if errors.Is(err, errAgentInstanceKeyConflict) {
			return utils.Error(c, http.StatusConflict, "Agent instance already enrolled", "Revoke the existing instance before registering a different key")
		}
		return utils.Error(c, http.StatusInternalServerError, "Agent instance could not be registered", "")
	}
	return utils.Success(c, http.StatusOK, "Agent instance registered", map[string]any{"instance": projectAgentInstance(instance)})
}

var errAgentInstanceKeyConflict = errors.New("agent instance has a different enrolled key")

// ListAgentInstances exposes only public identity metadata to the primary root.
// Keyset pagination is stable even as workers heartbeat concurrently.
func ListAgentInstances(c echo.Context) error {
	if err := requireAgentPlatformWorkspace(c, true); err != nil {
		return authz.Respond(c, err)
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Agent registry unavailable", "")
	}
	limit := 100
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			return utils.Error(c, http.StatusBadRequest, "Invalid agent instance query", "")
		}
		limit = parsed
	}
	query := configuration.DB.Model(&models.AutomationAgentInstance{})
	agentKey := strings.TrimSpace(c.QueryParam("agent_key"))
	if agentKey != "" {
		if !agentProfileKeyPattern.MatchString(agentKey) {
			return utils.Error(c, http.StatusBadRequest, "Invalid agent instance query", "")
		}
		query = query.Where("agent_key = ?", agentKey)
	}
	status := strings.TrimSpace(c.QueryParam("status"))
	if status != "" {
		if status != "active" && status != "revoked" {
			return utils.Error(c, http.StatusBadRequest, "Invalid agent instance query", "")
		}
		query = query.Where("status = ?", status)
	}
	cursorScope := agentInstanceCursorScope(agentKey, status)
	if rawCursor := strings.TrimSpace(c.QueryParam("cursor")); rawCursor != "" {
		cursor, err := decodeAgentInstanceCursor(rawCursor, cursorScope)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid agent instance cursor", "")
		}
		cursorID, _ := uuid.FromString(cursor.ID)
		query = query.Where("(created_at, id) < (?, ?)", cursor.CreatedAt, cursorID)
	}
	var instances []models.AutomationAgentInstance
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&instances).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Agent registry unavailable", "")
	}
	response := map[string]any{"instances": make([]agentInstanceDTO, 0, len(instances)), "next_cursor": ""}
	nextCursor := ""
	if len(instances) > limit {
		instances = instances[:limit]
		last := instances[len(instances)-1]
		nextCursor = encodeAgentInstanceCursor(agentInstanceCursor{Version: 1, Scope: cursorScope, CreatedAt: last.CreatedAt, ID: last.ID.String()})
	}
	projected := make([]agentInstanceDTO, 0, len(instances))
	for _, instance := range instances {
		projected = append(projected, projectAgentInstance(instance))
	}
	response["instances"] = projected
	response["next_cursor"] = nextCursor
	return utils.Success(c, http.StatusOK, "Agent instances", response)
}

func RevokeAgentInstance(c echo.Context) error {
	if err := requireAgentPlatformWorkspace(c, true); err != nil {
		return authz.Respond(c, err)
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Agent registry unavailable", "")
	}
	id, err := uuid.FromString(strings.TrimSpace(c.Param("id")))
	if err != nil || id == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent instance", "")
	}
	now := time.Now().UTC()
	var instance models.AutomationAgentInstance
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&instance, "id = ?", id).Error; err != nil {
			return err
		}
		if instance.Status == "active" {
			instance.Status = "revoked"
			instance.RevokedAt = &now
			return tx.Save(&instance).Error
		}
		return nil
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Agent instance not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Agent instance could not be revoked", "")
	}
	return utils.Success(c, http.StatusOK, "Agent instance revoked", map[string]any{"instance": projectAgentInstance(instance)})
}

func projectAgentInstance(instance models.AutomationAgentInstance) agentInstanceDTO {
	return agentInstanceDTO{ID: instance.ID.String(), AgentKey: instance.AgentKey, MachineID: canonicalOpaqueMachineID(instance.MachineID), PublicKeyFingerprint: instance.PublicKeyFingerprint, Status: instance.Status, CreatedAt: instance.CreatedAt, LastSeenAt: instance.LastSeenAt, RevokedAt: instance.RevokedAt}
}

func encodeAgentInstanceCursor(cursor agentInstanceCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func agentInstanceCursorScope(agentKey, status string) string {
	input := struct {
		AgentKey string `json:"agent_key,omitempty"`
		Status   string `json:"status,omitempty"`
	}{AgentKey: strings.TrimSpace(agentKey), Status: strings.TrimSpace(status)}
	encoded, _ := json.Marshal(input)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func decodeAgentInstanceCursor(value, expectedScope string) (agentInstanceCursor, error) {
	var cursor agentInstanceCursor
	if len(value) > 1024 {
		return cursor, errors.New("invalid cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return agentInstanceCursor{}, errors.New("invalid cursor")
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 || cursor.Scope != expectedScope || cursor.CreatedAt.IsZero() {
		return agentInstanceCursor{}, errors.New("invalid cursor")
	}
	if len(cursor.Scope) != sha256.Size*2 {
		return agentInstanceCursor{}, errors.New("invalid cursor")
	}
	if _, err := hex.DecodeString(cursor.Scope); err != nil {
		return agentInstanceCursor{}, errors.New("invalid cursor")
	}
	id, err := uuid.FromString(cursor.ID)
	if err != nil || id == uuid.Nil || id.String() != cursor.ID {
		return agentInstanceCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}
