package publicaccess

import (
	"errors"
	"time"

	"events-stocks/internal/accesstoken"
	"events-stocks/internal/previewtoken"
	"events-stocks/internal/publicaccessproof"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/services/ports"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

type EventReadDeps struct {
	ConfigRepo     ports.EventConfigRepository
	TokenRepo      ports.AccessTokenRepository
	InvitationRepo ports.InvitationRepository
	IsEventActive  func(eventID uuid.UUID) (bool, error)
	Now            func() time.Time

	// RequirePasswordProof protects public section data for password-protected
	// events. Preview tokens still bypass this gate for Studio previews.
	RequirePasswordProof bool
	PasswordProofToken   string
}

type EventReadResult struct {
	Allowed bool
	Config  *models.EventConfig
}

func AllowEventReadFromRequest(c echo.Context, eventID uuid.UUID, deps EventReadDeps) (bool, error) {
	deps = eventReadDepsWithRequestProof(c, deps)
	return AllowEventRead(eventID, utils.PublicPreviewToken(c), utils.PublicInvitationQueryToken(c), deps)
}

func AllowEventRead(eventID uuid.UUID, previewToken, invitationToken string, deps EventReadDeps) (bool, error) {
	result, err := AllowEventReadWithConfig(eventID, previewToken, invitationToken, deps)
	return result.Allowed, err
}

func AllowEventReadFromRequestWithConfig(c echo.Context, eventID uuid.UUID, deps EventReadDeps) (EventReadResult, error) {
	deps = eventReadDepsWithRequestProof(c, deps)
	return AllowEventReadWithConfig(eventID, utils.PublicPreviewToken(c), utils.PublicInvitationQueryToken(c), deps)
}

func AllowEventReadWithConfig(eventID uuid.UUID, previewToken, invitationToken string, deps EventReadDeps) (EventReadResult, error) {
	if deps.ConfigRepo == nil {
		return EventReadResult{}, errors.New("event config repository unavailable")
	}
	previewAllowed := previewtoken.Validate(previewToken, eventID)
	now := eventReadNow(deps)
	cfg, err := deps.ConfigRepo.GetEventConfigByID(eventID)
	if err != nil {
		return EventReadResult{}, err
	}
	cfg = effectiveEventReadConfig(cfg)
	result := EventReadResult{Config: cfg}
	if deps.IsEventActive != nil {
		active, err := deps.IsEventActive(eventID)
		if err != nil {
			return result, err
		}
		if !active {
			result.Allowed = previewAllowed
			return result, nil
		}
	}
	if !EventAccessWindowOpen(cfg, now) {
		result.Allowed = previewAllowed
		return result, nil
	}
	if eventPasswordProofRequired(eventID, cfg, deps) && !previewAllowed {
		result.Allowed = false
		return result, nil
	}
	if cfg != nil && cfg.IsPublic {
		result.Allowed = true
		return result, nil
	}
	result.Allowed = previewAllowed || allowInvitationToken(eventID, invitationToken, deps, now)
	return result, nil
}

func effectiveEventReadConfig(cfg *models.EventConfig) *models.EventConfig {
	if cfg == nil {
		return nil
	}
	return cfg.WithVisibilityDefaults()
}

func eventReadDepsWithRequestProof(c echo.Context, deps EventReadDeps) EventReadDeps {
	if proof := utils.PublicEventAccessToken(c); proof != "" {
		deps.PasswordProofToken = proof
	}
	return deps
}

func eventPasswordProofRequired(eventID uuid.UUID, cfg *models.EventConfig, deps EventReadDeps) bool {
	if !deps.RequirePasswordProof || cfg == nil || !cfg.HasAuthPasswordPreview() {
		return false
	}
	return !publicaccessproof.Validate(
		deps.PasswordProofToken,
		eventID,
		eventsService.EventConfigAccessVersion(cfg),
	)
}

func eventReadNow(deps EventReadDeps) time.Time {
	if deps.Now != nil {
		return deps.Now()
	}
	return time.Now()
}

func EventAccessWindowOpen(cfg *models.EventConfig, now time.Time) bool {
	if cfg == nil {
		return true
	}
	if eventAccessTimeSet(cfg.ActiveFrom) && now.Before(cfg.ActiveFrom) {
		return false
	}
	if cfg.ActiveUntil != nil && eventAccessTimeSet(*cfg.ActiveUntil) && now.After(*cfg.ActiveUntil) {
		return false
	}
	return true
}

func eventAccessTimeSet(value time.Time) bool {
	return !value.IsZero() && value.Year() > 1970
}

func allowInvitationToken(eventID uuid.UUID, token string, deps EventReadDeps, now time.Time) bool {
	if token == "" || deps.TokenRepo == nil || deps.InvitationRepo == nil {
		return false
	}
	accessToken, err := accesstoken.Lookup(deps.TokenRepo, token)
	if err != nil || accessToken == nil || isExpiredAccessToken(accessToken, now) {
		return false
	}
	invitation, err := deps.InvitationRepo.GetInvitationByIDLite(accessToken.InvitationID)
	if err != nil || invitation == nil {
		return false
	}
	return invitation.EventID == eventID
}

func isExpiredAccessToken(accessToken *models.InvitationAccessToken, now time.Time) bool {
	return accessToken != nil && accessToken.ExpiresAt != nil && now.After(*accessToken.ExpiresAt)
}
