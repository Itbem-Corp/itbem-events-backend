package automation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	automationqueue "events-stocks/repositories/automationqueuerepository"
	"events-stocks/utils"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const automationCapacitySignatureHeader = "X-Automation-Capacity-Signature"

type automationCapacityProjection struct {
	Scaling    automationScalingHealth `json:"scaling"`
	Reconciler string                  `json:"reconciler"`
	ObservedAt time.Time               `json:"observed_at"`
}

func writeSignedCapacityProjection(c echo.Context, projection automationCapacityProjection, secret string) error {
	response := utils.APIResponse{Status: http.StatusOK, Message: "Automation capacity projection", Data: projection}
	body, err := json.Marshal(response)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation capacity unavailable", "")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	c.Response().Header().Set(automationCapacitySignatureHeader, "sha256="+fmt.Sprintf("%x", mac.Sum(nil)))
	return c.JSONBlob(http.StatusOK, body)
}

func automationCapacitySigningSecret() string {
	return strings.TrimSpace(os.Getenv("AUTOMATION_CALLBACK_SECRET"))
}

func recommendedWorkerCount(queueDepth int64, targetMessagesPerWorker, maxWorkers int) int64 {
	if targetMessagesPerWorker <= 0 || maxWorkers <= 0 {
		return 0
	}
	if queueDepth < 0 {
		queueDepth = 0
	}
	desired := (queueDepth + int64(targetMessagesPerWorker) - 1) / int64(targetMessagesPerWorker)
	if desired < 1 {
		desired = 1
	}
	if desired > int64(maxWorkers) {
		desired = int64(maxWorkers)
	}
	return desired
}

// Capacity gives an authenticated local reconciler a signed, read-only
// recommendation. It does not provision, stop, or signal worker machines.
func Capacity(c echo.Context) error {
	providedSecret := c.Request().Header.Get("X-Automation-Secret")
	if !validCallbackSecret(providedSecret) {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	signingSecret := automationCapacitySigningSecret()
	if signingSecret == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "")
	}
	cfg, _ := c.Get("config").(*models.Config)
	projection := automationCapacityProjection{
		Scaling:    automationScalingHealth{Mode: "disabled", Reason: "scaling_policy_not_configured"},
		Reconciler: "external_supervisor", ObservedAt: time.Now().UTC(),
	}
	if cfg == nil || cfg.AutomationScaleTargetMessages <= 0 || cfg.AutomationScaleMaxWorkers <= 0 {
		return writeSignedCapacityProjection(c, projection, signingSecret)
	}
	queueHealth := automationqueue.QueueHealth(c.Request().Context())
	projection.Scaling = automationScalingHealth{
		Mode: "unknown", Reason: "queue_telemetry_unavailable",
		TargetMessagesPerWorker: cfg.AutomationScaleTargetMessages, MaxWorkers: cfg.AutomationScaleMaxWorkers,
	}
	if queueHealth.Available {
		projection.Scaling.Mode = "observe_only"
		projection.Scaling.Reason = "queue_empty_floor_one"
		projection.Scaling.QueueDepth = queueHealth.Visible + queueHealth.InFlight + queueHealth.Delayed
		projection.Scaling.DesiredWorkers = recommendedWorkerCount(projection.Scaling.QueueDepth, cfg.AutomationScaleTargetMessages, cfg.AutomationScaleMaxWorkers)
		if projection.Scaling.QueueDepth > 0 {
			projection.Scaling.Reason = "queue_backlog"
		}
	}
	workerQuery := configuration.DB.Model(&models.AutomationAgentHeartbeat{}).Where("last_seen_at >= ?", projection.ObservedAt.Add(-90*time.Second))
	if err := workerQuery.Count(&projection.Scaling.ActiveWorkers).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation capacity unavailable", "")
	}
	if err := workerQuery.Session(&gorm.Session{}).Where("draining = ?", false).Count(&projection.Scaling.AvailableWorkers).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation capacity unavailable", "")
	}
	if projection.Scaling.DesiredWorkers > 0 {
		projection.Scaling.WorkerGap = projection.Scaling.DesiredWorkers - projection.Scaling.AvailableWorkers
	}
	return writeSignedCapacityProjection(c, projection, signingSecret)
}
