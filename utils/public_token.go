package utils

import (
	"strings"

	"github.com/labstack/echo/v4"
)

var publicInvitationQueryKeys = []string{"token", "Token", "invitation_token", "invitationToken", "InvitationToken", "pretty_token", "prettyToken", "PrettyToken"}
var publicEventAccessQueryKeys = []string{"event_access_token", "eventAccessToken", "EventAccessToken"}
var publicPreviewQueryKeys = []string{"preview_token", "previewToken", "PreviewToken"}

const HeaderEventAccessToken = "X-Event-Access-Token"

func PublicInvitationQueryToken(c echo.Context) string {
	for _, key := range publicInvitationQueryKeys {
		if token := strings.TrimSpace(c.QueryParam(key)); token != "" {
			return token
		}
	}
	return ""
}

func PublicInvitationToken(c echo.Context) string {
	if token := strings.TrimSpace(c.Param("token")); token != "" {
		return token
	}
	return PublicInvitationQueryToken(c)
}

func PublicEventAccessToken(c echo.Context) string {
	if token := strings.TrimSpace(c.Request().Header.Get(HeaderEventAccessToken)); token != "" {
		return token
	}
	for _, key := range publicEventAccessQueryKeys {
		if token := strings.TrimSpace(c.QueryParam(key)); token != "" {
			return token
		}
	}
	return ""
}

func PublicPreviewToken(c echo.Context) string {
	for _, key := range publicPreviewQueryKeys {
		if token := strings.TrimSpace(c.QueryParam(key)); token != "" {
			return token
		}
	}
	return ""
}
