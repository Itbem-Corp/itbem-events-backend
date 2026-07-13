package publicaccessproof

import (
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateValidateAccessProof(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	eventID := uuid.Must(uuid.NewV4())

	token, expiresAt, err := Generate(eventID, "v1", time.Minute)
	require.NoError(t, err)
	assert.True(t, time.Now().Before(expiresAt))
	assert.True(t, Validate(token, eventID, "v1"))
}

func TestGenerateDoesNotFallBackToOtherCredentialSecrets(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "")
	t.Setenv("EVENT_PREVIEW_SECRET", strings.Repeat("p", 32))
	t.Setenv("INTERNAL_API_SECRET", strings.Repeat("i", 32))
	t.Setenv("COGNITO_CLIENT_SECRET", strings.Repeat("c", 32))

	_, _, err := Generate(uuid.Must(uuid.NewV4()), "v1", time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestValidateRejectsOtherEventOrAccessVersion(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	eventID := uuid.Must(uuid.NewV4())

	token, _, err := Generate(eventID, "v1", time.Minute)
	require.NoError(t, err)

	assert.False(t, Validate(token, uuid.Must(uuid.NewV4()), "v1"))
	assert.False(t, Validate(token, eventID, "v2"))
}

func TestValidateRejectsExpiredToken(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	eventID := uuid.Must(uuid.NewV4())

	token, _, err := Generate(eventID, "v1", -time.Minute)
	require.NoError(t, err)

	assert.False(t, Validate(token, eventID, "v1"))
}
