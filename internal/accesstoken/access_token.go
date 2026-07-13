package accesstoken

import (
	"strings"

	"events-stocks/models"
	"events-stocks/services/ports"
)

// Lookup accepts either the stored raw token or the guest-facing pretty token.
func Lookup(repo ports.AccessTokenRepository, token string) (*models.InvitationAccessToken, error) {
	token = strings.TrimSpace(token)
	if repo == nil || token == "" {
		return nil, nil
	}

	accessToken, tokenErr := repo.GetByToken(token)
	if tokenErr == nil && accessToken != nil {
		return accessToken, nil
	}

	prettyToken, prettyErr := repo.GetByPrettyToken(token)
	if prettyErr == nil && prettyToken != nil {
		return prettyToken, nil
	}
	if tokenErr != nil {
		return nil, tokenErr
	}
	return nil, prettyErr
}
