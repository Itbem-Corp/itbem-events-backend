package invitations

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/guestrepository"
	"events-stocks/repositories/invitationaccesstokenrepository"
	"events-stocks/repositories/invitationlogrepository"
	"events-stocks/repositories/invitationrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"fmt"
	"time"

	"github.com/gofrs/uuid"
)

type InvitationWithGuest struct {
	Invitation  models.Invitation `json:"invitation"`
	Guest       models.Guest      `json:"guest"`
	PrettyToken string            `json:"pretty_token,omitempty"`
}

func GetInvitationByToken(token string) (*InvitationWithGuest, error) {
	// 1. Buscar token
	accessToken, err := invitationaccesstokenrepository.GetByToken(token)
	if err != nil {
		return nil, err
	}
	if accessToken == nil {
		return nil, fmt.Errorf("access token not found")
	}

	// 2. Validar expiración
	if accessToken.ExpiresAt != nil && time.Now().After(*accessToken.ExpiresAt) {
		return nil, fmt.Errorf("token expired")
	}

	// 3. Traer la Invitation real (con preload del Event, etc.)
	invitation, err := invitationrepository.GetInvitationByID(accessToken.InvitationID)
	if err != nil {
		return nil, err
	}

	// 4. Traer el Guest ligado a esa Invitation
	guest, err := guestrepository.GetGuestByInvitationID(accessToken.InvitationID)
	if err != nil {
		return nil, err
	}

	// 5. Log de acceso (no crítico, pero registramos si falla)
	logErr := invitationlogrepository.CreateInvitationLog(&models.InvitationLog{
		InvitationID: invitation.ID,
		Channel:      "token",
		Action:       "accessed",
		Status:       "success",
		Response:     "Invitation accessed via token",
		Timestamp:    time.Now(),
		CreatedAt:    time.Now(),
	})
	if logErr != nil {
		fmt.Printf("failed to create invitation log: %v\n", logErr)
	}

	// 6. Retornar DTO con PrettyToken incluido
	return &InvitationWithGuest{
		Invitation:  *invitation,
		Guest:       *guest,
		PrettyToken: accessToken.PrettyToken,
	}, nil
}

func ConfirmRSVP(prettyToken string, status string, method string, guestCount int) (*models.Guest, error) {
	// 1. Buscar el accessToken por PrettyToken
	accessToken, err := invitationaccesstokenrepository.GetByPrettyToken(prettyToken)
	if err != nil || accessToken == nil {
		return nil, fmt.Errorf("invalid or expired token")
	}

	// 2. Traer la Invitation asociada (para validar maxGuests)
	invitation, err := invitationrepository.GetInvitationByID(accessToken.InvitationID)
	if err != nil || invitation == nil {
		return nil, fmt.Errorf("invitation not found for token")
	}

	// 3. Validar límite de personas
	if guestCount > invitation.MaxGuests {
		return nil, fmt.Errorf("guest count (%d) exceeds allowed max (%d)", guestCount, invitation.MaxGuests)
	}

	// 4. Buscar el Guest
	guest, err := guestrepository.GetGuestByInvitationID(accessToken.InvitationID)
	if err != nil || guest == nil {
		return nil, fmt.Errorf("guest not found for token")
	}

	// 5. Actualizar RSVP
	now := time.Now()
	guest.RSVPStatus = status
	guest.RSVPAt = &now
	guest.RSVPMethod = method
	guest.RSVPTokenID = &accessToken.ID
	guest.RSVPGuestCount = guestCount

	if err := guestrepository.UpdateGuest(guest); err != nil {
		return nil, err
	}

	// 6. Log de confirmación
	_ = invitationlogrepository.CreateInvitationLog(&models.InvitationLog{
		InvitationID: accessToken.InvitationID,
		Channel:      "rsvp",
		Action:       "confirmed",
		Status:       status,
		Response: fmt.Sprintf(
			"RSVP confirmed via %s with pretty_token %s, guest_count=%d",
			method, prettyToken, guestCount,
		),
		Timestamp: now,
		CreatedAt: now,
	})

	return guest, nil
}

func ListInvitations() ([]models.Invitation, error) {
	cacheKey := "all:invitations"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.Invitation
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := invitationrepository.ListInvitations()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["invitations"])

	return data, nil
}

func CreateInvitation(obj *models.Invitation) error {
	if err := invitationrepository.CreateInvitation(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("invitations", "all")
}

func UpdateInvitation(obj *models.Invitation) error {
	if err := invitationrepository.UpdateInvitation(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("invitations", "all")
}

func DeleteInvitation(id uuid.UUID) error {
	if err := invitationrepository.DeleteInvitation(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("invitations", "all")
}
