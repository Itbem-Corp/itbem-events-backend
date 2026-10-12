package dtos

import (
	"encoding/json"
	"strings"
	"testing"

	"events-stocks/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func applyPasswordPatch(t *testing.T, cfg *models.EventConfig, password string) error {
	t.Helper()
	raw, err := json.Marshal(password)
	require.NoError(t, err)
	return EventConfigPatch{"auth_password_preview": raw}.ApplyTo(cfg)
}

func TestEventConfigPatch_PasswordMinimumLength(t *testing.T) {
	cfg := &models.EventConfig{}
	err := applyPasswordPatch(t, cfg, "12345")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth_password_preview")
	assert.Contains(t, err.Error(), "at least 6")
	assert.Empty(t, cfg.AuthPasswordPreview)

	require.NoError(t, applyPasswordPatch(t, cfg, " 123456 "))
	assert.Equal(t, "123456", cfg.AuthPasswordPreview)
}

func TestEventConfigPatch_PasswordCountsRunesAndCapsLength(t *testing.T) {
	cfg := &models.EventConfig{}
	require.NoError(t, applyPasswordPatch(t, cfg, "ñandúé"))
	require.Error(t, applyPasswordPatch(t, &models.EventConfig{}, strings.Repeat("a", 129)))
}

func TestEventConfigPatch_PasswordKeepsLegacyShortValueAndAllowsRemoval(t *testing.T) {
	cfg := &models.EventConfig{AuthPasswordPreview: "1234"}
	require.NoError(t, applyPasswordPatch(t, cfg, "1234"), "re-sending the unchanged legacy password is allowed")
	require.NoError(t, applyPasswordPatch(t, cfg, ""), "clearing the password is allowed")
	assert.Empty(t, cfg.AuthPasswordPreview)
}
