package invitationlogrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

func CreateInvitationLog(m *models.InvitationLog) error {
	return gormrepository.Insert(m)
}

func CreateManyInvitationLogs(models []models.InvitationLog) error {
	return gormrepository.InsertMany(models)
}

func UpdateInvitationLog(m *models.InvitationLog) error {
	return gormrepository.Update(m, m.ID)
}

func DeleteInvitationLog(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.InvitationLog{})
}

func GetInvitationLogByID(id uuid.UUID) (*models.InvitationLog, error) {
	var model models.InvitationLog
	err := gormrepository.GetByID(&model, id)
	return &model, err
}

func ListInvitationLogs() ([]models.InvitationLog, error) {
	var list []models.InvitationLog
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
	return list, err
}

// InvitationLogRepo implements ports.InvitationLogRepository.
type InvitationLogRepo struct{}

func NewInvitationLogRepo() *InvitationLogRepo { return &InvitationLogRepo{} }

func (r *InvitationLogRepo) CreateInvitationLog(m *models.InvitationLog) error {
	return CreateInvitationLog(m)
}
func (r *InvitationLogRepo) CreateManyInvitationLogs(logs []models.InvitationLog) error {
	return CreateManyInvitationLogs(logs)
}
