package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type GuestResponse struct {
	ID                  uuid.UUID            `json:"id"`
	EventID             uuid.UUID            `json:"event_id"`
	TableID             *uuid.UUID           `json:"table_id,omitempty"`
	Table               *EventTableResponse  `json:"table,omitempty"`
	TableNumber         string               `json:"table_number,omitempty"`
	InvitationID        *uuid.UUID           `json:"invitation_id,omitempty"`
	PrettyToken         string               `json:"pretty_token,omitempty"`
	FirstName           string               `json:"first_name"`
	LastName            string               `json:"last_name"`
	Nickname            string               `json:"nickname"`
	Email               string               `json:"email"`
	Phone               string               `json:"phone"`
	ShowContactInfo     bool                 `json:"show_contact_info"`
	Role                string               `json:"role"`
	Bio                 string               `json:"bio"`
	Order               int                  `json:"order"`
	ImageURL            string               `json:"image_url"`
	Image1URL           string               `json:"image1_url"`
	Image2URL           string               `json:"image2_url"`
	Image3URL           string               `json:"image3_url"`
	Image1AliasURL      string               `json:"image_1_url"`
	Image2AliasURL      string               `json:"image_2_url"`
	Image3AliasURL      string               `json:"image_3_url"`
	Headline            string               `json:"headline"`
	Signature           string               `json:"signature"`
	GuestStatusID       uuid.UUID            `json:"guest_status_id"`
	GuestStatus         *GuestStatusResponse `json:"guest_status,omitempty"`
	StatusID            uuid.UUID            `json:"status_id"`
	Status              *GuestStatusResponse `json:"status,omitempty"`
	GuestsCount         int                  `json:"guests_count"`
	IsHost              bool                 `json:"is_host"`
	MaxGuests           int                  `json:"max_guests,omitempty"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
	RSVPStatus          string               `json:"rsvp_status"`
	RSVPAt              *time.Time           `json:"rsvp_at,omitempty"`
	RSVPMethod          string               `json:"rsvp_method"`
	RSVPTokenID         *uuid.UUID           `json:"rsvp_token_id,omitempty"`
	RSVPGuestCount      int                  `json:"rsvp_guest_count"`
	DietaryRestrictions string               `json:"dietary_restrictions"`
	RSVPNotes           string               `json:"rsvp_notes"`
	Notes               string               `json:"notes"`
}

type CheckinGuestsListQuery struct {
	Page      int
	PageSize  int
	Search    string
	Filter    string
	QR        string
	Sort      string
	Direction string
	// SkipTotal is reserved for composed workspace loads that obtain the
	// authoritative total from the event summary in the same response.
	SkipTotal bool
}

type CheckinGuestsPageResponse struct {
	Data         []GuestResponse    `json:"data"`
	Total        int64              `json:"total"`
	Page         int                `json:"page"`
	PageSize     int                `json:"page_size"`
	TotalPages   int                `json:"total_pages"`
	Summary      *GuestSummary      `json:"summary,omitempty"`
	ShareSummary *GuestShareSummary `json:"share_summary,omitempty"`
}

type CheckinWorkspaceResponse struct {
	Event    EventResponse             `json:"event"`
	Statuses []GuestStatusResponse     `json:"statuses"`
	Guests   CheckinGuestsPageResponse `json:"guests"`
}

func eventTableResponsePtr(table *models.EventTable) *EventTableResponse {
	if table == nil {
		return nil
	}
	response := NewEventTableResponse(*table)
	return &response
}

func guestStatusForResponse(guest models.Guest) *GuestStatusResponse {
	if status := guestStatusResponsePtr(guest.GuestStatus); status != nil {
		return status
	}
	if guest.Status != nil {
		return guestStatusResponsePtr(*guest.Status)
	}
	return nil
}

func guestStatusIDForResponse(guest models.Guest) uuid.UUID {
	if guest.StatusID != uuid.Nil {
		return guest.StatusID
	}
	return guest.GuestStatusID
}

func NewGuestResponse(guest *models.Guest) GuestResponse {
	if guest == nil {
		return GuestResponse{}
	}

	statusID := guestStatusIDForResponse(*guest)
	status := guestStatusForResponse(*guest)

	return GuestResponse{
		ID:                  guest.ID,
		EventID:             guest.EventID,
		TableID:             guest.TableID,
		Table:               eventTableResponsePtr(guest.Table),
		TableNumber:         guest.TableNumber,
		InvitationID:        guest.InvitationID,
		PrettyToken:         guest.PrettyToken,
		FirstName:           guest.FirstName,
		LastName:            guest.LastName,
		Nickname:            guest.Nickname,
		Email:               guest.Email,
		Phone:               guest.Phone,
		ShowContactInfo:     guest.ShowContactInfo,
		Role:                guest.Role,
		Bio:                 guest.Bio,
		Order:               guest.Order,
		ImageURL:            guest.ImageURL,
		Image1URL:           guest.Image1URL,
		Image2URL:           guest.Image2URL,
		Image3URL:           guest.Image3URL,
		Image1AliasURL:      guest.Image1URL,
		Image2AliasURL:      guest.Image2URL,
		Image3AliasURL:      guest.Image3URL,
		Headline:            guest.Headline,
		Signature:           guest.Signature,
		GuestStatusID:       statusID,
		GuestStatus:         status,
		StatusID:            statusID,
		Status:              status,
		GuestsCount:         guest.GuestsCount,
		IsHost:              guest.IsHost,
		MaxGuests:           guest.MaxGuests,
		CreatedAt:           guest.CreatedAt,
		UpdatedAt:           guest.UpdatedAt,
		RSVPStatus:          guest.RSVPStatus,
		RSVPAt:              guest.RSVPAt,
		RSVPMethod:          guest.RSVPMethod,
		RSVPTokenID:         guest.RSVPTokenID,
		RSVPGuestCount:      guest.RSVPGuestCount,
		DietaryRestrictions: guest.DietaryRestrictions,
		RSVPNotes:           guest.RSVPNotes,
		Notes:               guest.Notes,
	}
}

func NewGuestResponses(guests []models.Guest) []GuestResponse {
	response := make([]GuestResponse, 0, len(guests))
	for i := range guests {
		response = append(response, NewGuestResponse(&guests[i]))
	}
	return response
}
