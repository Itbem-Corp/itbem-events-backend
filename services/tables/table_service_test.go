package tables_test

import (
	"testing"

	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/tables"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockTableRepo struct{ mock.Mock }

func (m *mockTableRepo) ListByEventID(id uuid.UUID) ([]models.Table, error) {
	args := m.Called(id)
	return args.Get(0).([]models.Table), args.Error(1)
}
func (m *mockTableRepo) Create(t *models.Table) error { return m.Called(t).Error(0) }
func (m *mockTableRepo) Update(t *models.Table) error { return m.Called(t).Error(0) }
func (m *mockTableRepo) Delete(id uuid.UUID) error    { return m.Called(id).Error(0) }
func (m *mockTableRepo) GetByID(id uuid.UUID) (*models.Table, error) {
	args := m.Called(id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.Table), args.Error(1)
}
func (m *mockTableRepo) BatchAssign(a []dtos.SeatAssignment) error { return m.Called(a).Error(0) }

func TestListByEventID_ReturnsTablesFromRepo(t *testing.T) {
	repo := &mockTableRepo{}
	svc := tables.NewTableService(repo)

	eventID, _ := uuid.NewV4()
	expected := []models.Table{{Name: "Mesa 1", Capacity: 10}}
	repo.On("ListByEventID", eventID).Return(expected, nil)

	result, err := svc.ListByEventID(eventID)

	assert.NoError(t, err)
	assert.Equal(t, expected, result)
	repo.AssertExpectations(t)
}

func TestCreate_SetsEventIDAndCallsRepo(t *testing.T) {
	repo := &mockTableRepo{}
	svc := tables.NewTableService(repo)

	eventID, _ := uuid.NewV4()
	table := &models.Table{Name: "VIP", Capacity: 8}
	repo.On("Create", mock.MatchedBy(func(t *models.Table) bool {
		return t.EventID == eventID && t.Name == "VIP"
	})).Return(nil)

	err := svc.Create(eventID, table)

	assert.NoError(t, err)
	assert.Equal(t, eventID, table.EventID)
	repo.AssertExpectations(t)
}

func TestBatchAssign_DelegatesToRepo(t *testing.T) {
	repo := &mockTableRepo{}
	svc := tables.NewTableService(repo)

	tid := "some-table-id"
	assignments := []dtos.SeatAssignment{
		{GuestID: "guest-1", TableID: &tid},
		{GuestID: "guest-2", TableID: nil},
	}
	repo.On("BatchAssign", assignments).Return(nil)

	err := svc.BatchAssign(assignments)
	assert.NoError(t, err)
	repo.AssertExpectations(t)
}
