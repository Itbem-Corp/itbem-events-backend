package utils

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func newTokenTestContext(path string) echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}

func TestPublicInvitationQueryTokenReadsAliases(t *testing.T) {
	assert.Equal(t, "RAW", PublicInvitationQueryToken(newTokenTestContext("/api/events/demo/meta?token=%20RAW%20")))
	assert.Equal(t, "INV", PublicInvitationQueryToken(newTokenTestContext("/api/events/demo/meta?invitation_token=%20INV%20")))
	assert.Equal(t, "INV_CAMEL", PublicInvitationQueryToken(newTokenTestContext("/api/events/demo/meta?invitationToken=%20INV_CAMEL%20")))
	assert.Equal(t, "INV_PASCAL", PublicInvitationQueryToken(newTokenTestContext("/api/events/demo/meta?InvitationToken=%20INV_PASCAL%20")))
	assert.Equal(t, "PRETTY", PublicInvitationQueryToken(newTokenTestContext("/api/events/demo/meta?pretty_token=%20PRETTY%20")))
	assert.Equal(t, "CAMEL", PublicInvitationQueryToken(newTokenTestContext("/api/events/demo/meta?prettyToken=%20CAMEL%20")))
	assert.Equal(t, "PASCAL", PublicInvitationQueryToken(newTokenTestContext("/api/events/demo/meta?PrettyToken=%20PASCAL%20")))
	assert.Equal(t, "RAW_PASCAL", PublicInvitationQueryToken(newTokenTestContext("/api/events/demo/meta?Token=%20RAW_PASCAL%20")))
}

func TestPublicInvitationQueryTokenPrefersCanonicalToken(t *testing.T) {
	c := newTokenTestContext("/api/events/demo/meta?pretty_token=PRETTY&prettyToken=CAMEL&invitation_token=INV&token=RAW")

	assert.Equal(t, "RAW", PublicInvitationQueryToken(c))
}

func TestPublicInvitationQueryTokenPrefersInvitationAliasOverPrettyAliases(t *testing.T) {
	c := newTokenTestContext("/api/events/demo/meta?pretty_token=PRETTY&prettyToken=CAMEL&invitationToken=INV")

	assert.Equal(t, "INV", PublicInvitationQueryToken(c))
}

func TestPublicInvitationTokenPrefersRouteParam(t *testing.T) {
	c := newTokenTestContext("/api/invitations/ByToken/PATH?token=RAW")
	c.SetParamNames("token")
	c.SetParamValues("PATH")

	assert.Equal(t, "PATH", PublicInvitationToken(c))
}

func TestPublicEventAccessTokenPrefersHeader(t *testing.T) {
	c := newTokenTestContext("/api/events/demo/page-spec?event_access_token=QUERY")
	c.Request().Header.Set(HeaderEventAccessToken, " HEADER ")

	assert.Equal(t, "HEADER", PublicEventAccessToken(c))
}

func TestPublicEventAccessTokenReadsQueryAliases(t *testing.T) {
	assert.Equal(t, "QUERY", PublicEventAccessToken(newTokenTestContext("/api/events/demo/page-spec?event_access_token=%20QUERY%20")))
	assert.Equal(t, "CAMEL", PublicEventAccessToken(newTokenTestContext("/api/events/demo/page-spec?eventAccessToken=%20CAMEL%20")))
	assert.Equal(t, "PASCAL", PublicEventAccessToken(newTokenTestContext("/api/events/demo/page-spec?EventAccessToken=%20PASCAL%20")))
}

func TestPublicPreviewTokenReadsQueryAliases(t *testing.T) {
	assert.Equal(t, "PREVIEW", PublicPreviewToken(newTokenTestContext("/api/events/demo/page-spec?preview_token=%20PREVIEW%20")))
	assert.Equal(t, "CAMEL", PublicPreviewToken(newTokenTestContext("/api/events/demo/page-spec?previewToken=%20CAMEL%20")))
	assert.Equal(t, "PASCAL", PublicPreviewToken(newTokenTestContext("/api/events/demo/page-spec?PreviewToken=%20PASCAL%20")))
}

func TestPublicPreviewTokenPrefersCanonicalToken(t *testing.T) {
	c := newTokenTestContext("/api/events/demo/page-spec?preview_token=PREVIEW&previewToken=CAMEL&PreviewToken=PASCAL")

	assert.Equal(t, "PREVIEW", PublicPreviewToken(c))
}
