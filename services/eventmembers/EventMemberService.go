package eventmembers

import (
	"errors"
	"fmt"
	"strings"

	"events-stocks/models"
	"events-stocks/services/ports"

	"github.com/gofrs/uuid"
)

var (
	ErrInvalidMemberAssignment = errors.New("invalid event member assignment")
	ErrOrganizationMembership  = errors.New("event member lacks organization access")
)

// OrganizationMembershipReader is the minimum client-domain capability needed
// to assign somebody to an event owned by an organization.
type OrganizationMembershipReader interface {
	GetClientDetails(clientID, userID uuid.UUID) (*models.Client, error)
}

type EventMemberService struct {
	repo    ports.EventMemberRepository
	clients OrganizationMembershipReader
}

func NewEventMemberService(repo ports.EventMemberRepository, clients OrganizationMembershipReader) *EventMemberService {
	return &EventMemberService{repo: repo, clients: clients}
}

func (s *EventMemberService) List(eventID uuid.UUID) ([]models.EventMember, error) {
	return s.repo.List(eventID)
}

func (s *EventMemberService) Upsert(eventID uuid.UUID, clientID *uuid.UUID, userID uuid.UUID, role string) (*models.EventMember, error) {
	role = strings.ToUpper(strings.TrimSpace(role))
	if userID == uuid.Nil || !validRole(role) || clientID == nil || *clientID == uuid.Nil {
		return nil, ErrInvalidMemberAssignment
	}
	if s.clients == nil {
		return nil, fmt.Errorf("organization membership reader: %w", ErrOrganizationMembership)
	}
	if _, err := s.clients.GetClientDetails(*clientID, userID); err != nil {
		return nil, fmt.Errorf("organization membership: %w: %v", ErrOrganizationMembership, err)
	}
	return s.repo.Upsert(eventID, userID, role)
}

func (s *EventMemberService) Remove(eventID, userID uuid.UUID) error {
	return s.repo.Remove(eventID, userID)
}

func validRole(role string) bool {
	switch role {
	case "EVENT_OWNER", "MANAGER", "EDITOR", "CHECKIN", "ANALYST", "VIEWER":
		return true
	default:
		return false
	}
}
