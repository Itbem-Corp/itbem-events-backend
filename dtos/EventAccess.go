package dtos

import "time"

type EventAccessVerificationResponse struct {
	PasswordProtected bool       `json:"passwordProtected"`
	AccessToken       string     `json:"accessToken,omitempty"`
	AccessTokenType   string     `json:"accessTokenType,omitempty"`
	AccessVersion     string     `json:"accessVersion,omitempty"`
	ExpiresAt         *time.Time `json:"expiresAt,omitempty"`
}
