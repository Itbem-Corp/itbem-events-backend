package automation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	agentCallbackIdentityContextKey = "automation_agent_callback_identity"
	maxSignedAgentCallbackBody      = 1 << 20
	agentCallbackNonceRetention     = 5 * time.Minute
)

type authenticatedAgentCallback struct {
	InstanceID uuid.UUID
	AgentKey   string
	MachineID  string
}

// AgentCallbackAuthentication authenticates a local agent instance over TLS
// using its enrolled Ed25519 key. The signature covers the exact request body,
// method, URI, timestamp and nonce. Nonces are persisted before dispatch so a
// replay cannot reach the handler even when requests hit different replicas.
func AgentCallbackAuthentication(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if configuration.DB == nil {
			return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "")
		}
		request := c.Request()
		if request.ContentLength > maxSignedAgentCallbackBody {
			return utils.Error(c, http.StatusRequestEntityTooLarge, "Agent callback rejected", "")
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxSignedAgentCallbackBody+1))
		if err != nil || len(body) > maxSignedAgentCallbackBody {
			return utils.Error(c, http.StatusRequestEntityTooLarge, "Agent callback rejected", "")
		}
		request.Body = io.NopCloser(bytes.NewReader(body))

		instanceRaw := strings.TrimSpace(request.Header.Get(agentcallbackauth.InstanceIDHeader))
		instanceID, err := uuid.FromString(instanceRaw)
		if err != nil || instanceID == uuid.Nil || instanceID.String() != instanceRaw {
			return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
		}
		timestamp, err := strconv.ParseInt(strings.TrimSpace(request.Header.Get(agentcallbackauth.TimestampHeader)), 10, 64)
		if err != nil || timestamp <= 0 {
			return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
		}
		nonceRaw := strings.TrimSpace(request.Header.Get(agentcallbackauth.NonceHeader))
		nonce, err := uuid.FromString(nonceRaw)
		if err != nil || nonce == uuid.Nil || nonce.String() != nonceRaw {
			return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
		}
		signature, err := agentcallbackauth.DecodeSignature(request.Header.Get(agentcallbackauth.SignatureHeader))
		if err != nil {
			return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
		}

		var registered models.AutomationAgentInstance
		if err := configuration.DB.Where("id = ? AND status = ?", instanceID, "active").First(&registered).Error; err != nil {
			return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
		}
		publicKey, err := agentcallbackauth.DecodePublicKey(registered.PublicKey)
		if err != nil || agentcallbackauth.VerifyRequest(publicKey, signature, instanceID.String(), request.Method, request.URL.RequestURI(), timestamp, nonce.String(), body, time.Now().UTC()) != nil {
			return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
		}

		now := time.Now().UTC()
		identity := authenticatedAgentCallback{InstanceID: instanceID, AgentKey: registered.AgentKey, MachineID: registered.MachineID}
		err = configuration.DB.Transaction(func(tx *gorm.DB) error {
			var current models.AutomationAgentInstance
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", instanceID).First(&current).Error; err != nil {
				return err
			}
			if current.Status != "active" || current.PublicKey != registered.PublicKey {
				return gorm.ErrRecordNotFound
			}
			nonceRecord := models.AutomationAgentCallbackNonce{InstanceID: instanceID, Nonce: nonce.String(), CreatedAt: now}
			insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&nonceRecord)
			if insert.Error != nil {
				return insert.Error
			}
			if insert.RowsAffected != 1 {
				return errAgentCallbackReplay
			}
			if err := tx.Model(&models.AutomationAgentInstance{}).Where("id = ? AND status = ?", instanceID, "active").Update("last_seen_at", now).Error; err != nil {
				return err
			}
			// Delete expired replay guards opportunistically on authenticated
			// traffic. The indexed predicate keeps cleanup bounded and avoids a
			// separate cache whose contents diverge between API replicas.
			return tx.Where("created_at < ?", now.Add(-agentCallbackNonceRetention)).Delete(&models.AutomationAgentCallbackNonce{}).Error
		})
		if err != nil {
			if err == errAgentCallbackReplay {
				return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
			}
			if err == gorm.ErrRecordNotFound {
				return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
			}
			return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "")
		}
		c.Set(agentCallbackIdentityContextKey, identity)
		return next(c)
	}
}

var errAgentCallbackReplay = errors.New("agent callback nonce was already used")

func currentAgentCallbackIdentity(c echo.Context) (authenticatedAgentCallback, bool) {
	identity, ok := c.Get(agentCallbackIdentityContextKey).(authenticatedAgentCallback)
	return identity, ok && identity.InstanceID != uuid.Nil && identity.AgentKey != "" && identity.MachineID != ""
}

func requireAgentCallbackIdentity(c echo.Context) (authenticatedAgentCallback, bool) {
	identity, ok := currentAgentCallbackIdentity(c)
	if !ok {
		_ = utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
		return authenticatedAgentCallback{}, false
	}
	return identity, true
}

func bindCallbackProfileIdentity(c echo.Context, agentKey, machineID *string) (authenticatedAgentCallback, bool) {
	identity, ok := requireAgentCallbackIdentity(c)
	if !ok {
		return authenticatedAgentCallback{}, false
	}
	if agentKey == nil || machineID == nil {
		_ = utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
		return authenticatedAgentCallback{}, false
	}
	if supplied := strings.TrimSpace(*agentKey); supplied != "" && supplied != identity.AgentKey {
		_ = utils.Error(c, http.StatusForbidden, "Agent identity mismatch", "")
		return authenticatedAgentCallback{}, false
	}
	if supplied := strings.TrimSpace(*machineID); supplied != "" && supplied != identity.MachineID {
		_ = utils.Error(c, http.StatusForbidden, "Agent identity mismatch", "")
		return authenticatedAgentCallback{}, false
	}
	*agentKey = identity.AgentKey
	*machineID = identity.MachineID
	return identity, true
}

// DecodeAgentInstanceRequest is shared by the registration handler and its
// tests to reject trailing JSON or fields that are not part of this contract.
func decodeAgentInstanceRequest(body io.Reader, target any) error {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return io.ErrUnexpectedEOF
	}
	return nil
}
