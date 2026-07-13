package previewtoken

import (
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAcceptsGeneratedPreviewToken(t *testing.T) {
	t.Setenv("EVENT_PREVIEW_SECRET", "test-secret")
	eventID := uuid.Must(uuid.NewV4())

	token, err := Generate(eventID, time.Minute)

	require.NoError(t, err)
	assert.True(t, Validate(token, eventID))
}

func TestGenerateDoesNotFallBackToInternalOrCognitoSecrets(t *testing.T) {
	t.Setenv("EVENT_PREVIEW_SECRET", "")
	t.Setenv("INTERNAL_API_SECRET", strings.Repeat("i", 32))
	t.Setenv("COGNITO_CLIENT_SECRET", strings.Repeat("c", 32))

	_, err := Generate(uuid.Must(uuid.NewV4()), time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestValidateTrimsPreviewTokenLikePublicClients(t *testing.T) {
	t.Setenv("EVENT_PREVIEW_SECRET", "test-secret")
	eventID := uuid.Must(uuid.NewV4())

	token, err := Generate(eventID, time.Minute)

	require.NoError(t, err)
	assert.True(t, Validate(" \t"+token+"\n ", eventID))
}

func TestValidateRejectsTokenForAnotherEvent(t *testing.T) {
	t.Setenv("EVENT_PREVIEW_SECRET", "test-secret")
	token, err := Generate(uuid.Must(uuid.NewV4()), time.Minute)

	require.NoError(t, err)
	assert.False(t, Validate(token, uuid.Must(uuid.NewV4())))
}
