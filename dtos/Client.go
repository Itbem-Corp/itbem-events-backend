package dtos

import (
	"events-stocks/models"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

type ClientSummaryResponse struct {
	ID           uuid.UUID           `json:"id"`
	Name         string              `json:"name"`
	Code         string              `json:"code"`
	ClientTypeID uuid.UUID           `json:"client_type_id"`
	ClientType   *ClientTypeResponse `json:"client_type,omitempty"`
	Logo         string              `json:"logo"`
	IsActive     bool                `json:"is_active"`
	AccessRole   string              `json:"access_role,omitempty"`
	// Inherited is true when access comes from an Owner/Admin membership on an
	// ancestor organization rather than a direct assignment (access_role then
	// starts with "INHERITED_"). Only meaningful on caller/user-scoped lists.
	Inherited bool       `json:"inherited"`
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

type ClientResponse struct {
	ClientSummaryResponse
	Parent   *ClientSummaryResponse  `json:"parent,omitempty"`
	Children []ClientSummaryResponse `json:"children,omitempty"`
}

type ClientsListQuery struct {
	Page       int
	PageSize   int
	Search     string
	TenantCode string
}

type ClientsPageResponse struct {
	Data       []ClientResponse `json:"data"`
	Total      int64            `json:"total"`
	Page       int              `json:"page"`
	PageSize   int              `json:"page_size"`
	TotalPages int              `json:"total_pages"`
	// Active/Inactive count organizations by their is_active flag, over the
	// same set as Total (direct + inherited). Direct/Inherited split Total by
	// how the access is obtained. They are filled on user-scoped pages only.
	Active    int64 `json:"active,omitempty"`
	Inactive  int64 `json:"inactive,omitempty"`
	Direct    int64 `json:"direct,omitempty"`
	Inherited int64 `json:"inherited,omitempty"`
}

// UserClientCounts summarises the organizations a user can reach.
type UserClientCounts struct {
	Active    int64
	Inactive  int64
	Direct    int64
	Inherited int64
}

func clientTypeResponsePtr(clientType models.ClientType) *ClientTypeResponse {
	if clientType.ID == uuid.Nil {
		return nil
	}
	response := NewClientTypeResponse(clientType)
	return &response
}

func NewClientSummaryResponse(client models.Client) ClientSummaryResponse {
	return ClientSummaryResponse{
		ID:           client.ID,
		Name:         client.Name,
		Code:         client.Code,
		ClientTypeID: client.ClientTypeID,
		ClientType:   clientTypeResponsePtr(client.ClientType),
		Logo:         client.Logo,
		IsActive:     client.IsActive,
		AccessRole:   client.AccessRole,
		Inherited:    strings.HasPrefix(strings.ToUpper(client.AccessRole), "INHERITED_"),
		ParentID:     client.ParentID,
		CreatedAt:    client.CreatedAt,
		UpdatedAt:    client.UpdatedAt,
	}
}

func NewClientResponse(client *models.Client) ClientResponse {
	if client == nil {
		return ClientResponse{}
	}

	response := ClientResponse{
		ClientSummaryResponse: NewClientSummaryResponse(*client),
	}

	if client.Parent != nil {
		parent := NewClientSummaryResponse(*client.Parent)
		response.Parent = &parent
	}

	if len(client.Children) > 0 {
		response.Children = make([]ClientSummaryResponse, 0, len(client.Children))
		for _, child := range client.Children {
			response.Children = append(response.Children, NewClientSummaryResponse(child))
		}
	}

	return response
}

func NewClientResponses(clients []models.Client) []ClientResponse {
	response := make([]ClientResponse, 0, len(clients))
	for i := range clients {
		response = append(response, NewClientResponse(&clients[i]))
	}
	return response
}
