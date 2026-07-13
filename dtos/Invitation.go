package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type InvitationResponse struct {
	ID                      uuid.UUID `json:"id"`
	EventID                 uuid.UUID `json:"event_id"`
	Type                    string    `json:"type"`
	SubType                 string    `json:"sub_type"`
	InvitationEmailSent     bool      `json:"invitation_email_sent"`
	InvitationWhatsAppSent  bool      `json:"invitation_whatsapp_sent"`
	InvitationSent          bool      `json:"invitation_sent"`
	MaxGuests               int       `json:"max_guests"`
	MomentEmailRequested    bool      `json:"moment_email_requested"`
	MomentWhatsAppRequested bool      `json:"moment_whatsapp_requested"`
	MomentRequestSent       bool      `json:"moment_request_sent"`
	MomentEmailDelivered    bool      `json:"moment_email_delivered"`
	MomentWhatsAppDelivered bool      `json:"moment_whatsapp_delivered"`
	MomentDelivered         bool      `json:"moment_delivered"`
	EnableEmail             bool      `json:"enable_email"`
	EnableWhatsApp          bool      `json:"enable_whatsapp"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

func NewInvitationResponse(invitation models.Invitation) InvitationResponse {
	return InvitationResponse{
		ID:                      invitation.ID,
		EventID:                 invitation.EventID,
		Type:                    invitation.Type,
		SubType:                 invitation.SubType,
		InvitationEmailSent:     invitation.InvitationEmailSent,
		InvitationWhatsAppSent:  invitation.InvitationWhatsAppSent,
		InvitationSent:          invitation.InvitationSent,
		MaxGuests:               invitation.MaxGuests,
		MomentEmailRequested:    invitation.MomentEmailRequested,
		MomentWhatsAppRequested: invitation.MomentWhatsAppRequested,
		MomentRequestSent:       invitation.MomentRequestSent,
		MomentEmailDelivered:    invitation.MomentEmailDelivered,
		MomentWhatsAppDelivered: invitation.MomentWhatsAppDelivered,
		MomentDelivered:         invitation.MomentDelivered,
		EnableEmail:             invitation.EnableEmail,
		EnableWhatsApp:          invitation.EnableWhatsApp,
		CreatedAt:               invitation.CreatedAt,
		UpdatedAt:               invitation.UpdatedAt,
	}
}

func NewInvitationResponses(invitations []models.Invitation) []InvitationResponse {
	response := make([]InvitationResponse, 0, len(invitations))
	for _, invitation := range invitations {
		response = append(response, NewInvitationResponse(invitation))
	}
	return response
}
