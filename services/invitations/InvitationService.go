package invitations

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/invitationrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

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

func GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
	return invitationrepository.GetInvitationByID(id)
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
