package invitations

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/invitationaccesstokenrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListInvitationAccessTokens() ([]models.InvitationAccessToken, error) {
	cacheKey := "all:invitations"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.InvitationAccessToken
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := invitationaccesstokenrepository.ListInvitationAccessTokens()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["invitations"])

	return data, nil
}

func GetInvitationAccessTokenByID(id uuid.UUID) (*models.InvitationAccessToken, error) {
	return invitationaccesstokenrepository.GetInvitationAccessTokenByID(id)
}

func CreateInvitationAccessToken(obj *models.InvitationAccessToken) error {
	if err := invitationaccesstokenrepository.CreateInvitationAccessToken(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("invitations", "all")
}

func UpdateInvitationAccessToken(obj *models.InvitationAccessToken) error {
	if err := invitationaccesstokenrepository.UpdateInvitationAccessToken(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("invitations", "all")
}

func DeleteInvitationAccessToken(id uuid.UUID) error {
	if err := invitationaccesstokenrepository.DeleteInvitationAccessToken(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("invitations", "all")
}
