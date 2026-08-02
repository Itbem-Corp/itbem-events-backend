package eventmembers

import (
	"errors"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memberRepoStub struct{ upsertRole string }

func (s *memberRepoStub) List(uuid.UUID) ([]models.EventMember, error) { return nil, nil }
func (s *memberRepoStub) Upsert(eventID, userID uuid.UUID, role string) (*models.EventMember, error) {
	s.upsertRole = role
	return &models.EventMember{EventID: eventID, UserID: userID, Role: role}, nil
}
func (s *memberRepoStub) Remove(uuid.UUID, uuid.UUID) error { return nil }

type clientReaderStub struct{ err error }

func (s clientReaderStub) GetClientDetails(uuid.UUID, uuid.UUID) (*models.Client, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &models.Client{}, nil
}

func TestEventMemberServiceUpsertNormalizesRoleAndChecksOrganizationAccess(t *testing.T) {
	repo := &memberRepoStub{}
	clientID := uuid.Must(uuid.NewV4())
	member, err := NewEventMemberService(repo, clientReaderStub{}).Upsert(uuid.Must(uuid.NewV4()), &clientID, uuid.Must(uuid.NewV4()), " manager ")
	require.NoError(t, err)
	assert.Equal(t, "MANAGER", repo.upsertRole)
	assert.Equal(t, "MANAGER", member.Role)
}

func TestEventMemberServiceUpsertRejectsInvalidInputBeforeWriting(t *testing.T) {
	repo := &memberRepoStub{}
	_, err := NewEventMemberService(repo, clientReaderStub{}).Upsert(uuid.Must(uuid.NewV4()), nil, uuid.Nil, "owner")
	require.ErrorIs(t, err, ErrInvalidMemberAssignment)
	assert.Empty(t, repo.upsertRole)
}

func TestEventMemberServiceUpsertExposesOrganizationMembershipFailure(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	_, err := NewEventMemberService(&memberRepoStub{}, clientReaderStub{err: errors.New("forbidden")}).Upsert(uuid.Must(uuid.NewV4()), &clientID, uuid.Must(uuid.NewV4()), "viewer")
	require.ErrorIs(t, err, ErrOrganizationMembership)
}
