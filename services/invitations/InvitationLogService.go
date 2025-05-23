package invitations

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/invitationlogrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListInvitationLogs() ([]models.InvitationLog, error) {
	cacheKey := "all:invitations"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.InvitationLog
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := invitationlogrepository.ListInvitationLogs()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["invitations"])

	return data, nil
}

func GetInvitationLogByID(id uuid.UUID) (*models.InvitationLog, error) {
	return invitationlogrepository.GetInvitationLogByID(id)
}

func CreateInvitationLog(obj *models.InvitationLog) error {
	if err := invitationlogrepository.CreateInvitationLog(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("invitations", "all")
}

func UpdateInvitationLog(obj *models.InvitationLog) error {
	if err := invitationlogrepository.UpdateInvitationLog(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("invitations", "all")
}

func DeleteInvitationLog(id uuid.UUID) error {
	if err := invitationlogrepository.DeleteInvitationLog(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("invitations", "all")
}
