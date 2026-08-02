package organizationcontext

import (
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrganizationContextTokenIsBoundToSessionAndOrganization(t *testing.T) {
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", "organization-context-test-secret-at-least-32-bytes")
	organizationID := uuid.Must(uuid.NewV4())
	token, expiresAt, err := Generate("subject-1", "EventiApp", organizationID, 5*time.Minute)
	require.NoError(t, err)
	assert.True(t, expiresAt.After(time.Now()))
	assert.True(t, Validate(token, "subject-1", "eventiapp", organizationID))
	assert.False(t, Validate(token, "subject-2", "eventiapp", organizationID))
	assert.False(t, Validate(token, "subject-1", "itbem", organizationID))
	assert.False(t, Validate(token, "subject-1", "eventiapp", uuid.Must(uuid.NewV4())))
}

func TestOrganizationContextTokenFailsClosedWithoutDedicatedSecret(t *testing.T) {
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", "")
	_, _, err := Generate("subject-1", "eventiapp", uuid.Must(uuid.NewV4()), time.Minute)
	require.Error(t, err)
}

func TestOrganizationContextTokenSupportsBoundedSecretRotation(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", "old-organization-context-secret-at-least-32-bytes")
	oldToken, _, err := Generate("subject-1", "eventiapp", organizationID, DefaultTTL)
	require.NoError(t, err)

	t.Setenv("ORGANIZATION_CONTEXT_SECRET", "new-organization-context-secret-at-least-32-bytes")
	t.Setenv("ORGANIZATION_CONTEXT_SECRET_PREVIOUS", "old-organization-context-secret-at-least-32-bytes")
	assert.True(t, Validate(oldToken, "subject-1", "eventiapp", organizationID))

	newToken, _, err := Generate("subject-1", "eventiapp", organizationID, DefaultTTL)
	require.NoError(t, err)
	assert.True(t, Validate(newToken, "subject-1", "eventiapp", organizationID))

	t.Setenv("ORGANIZATION_CONTEXT_SECRET_PREVIOUS", "")
	assert.False(t, Validate(oldToken, "subject-1", "eventiapp", organizationID))
}

func TestOrganizationContextTokenRejectsInvalidIssuance(t *testing.T) {
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", "organization-context-test-secret-at-least-32-bytes")
	organizationID := uuid.Must(uuid.NewV4())
	tests := []struct {
		name, subject, application string
		organizationID             uuid.UUID
		ttl                        time.Duration
	}{
		{name: "empty subject", application: "eventiapp", organizationID: organizationID, ttl: DefaultTTL},
		{name: "empty application", subject: "subject-1", organizationID: organizationID, ttl: DefaultTTL},
		{name: "nil organization", subject: "subject-1", application: "eventiapp", ttl: DefaultTTL},
		{name: "zero ttl", subject: "subject-1", application: "eventiapp", organizationID: organizationID},
		{name: "excessive ttl", subject: "subject-1", application: "eventiapp", organizationID: organizationID, ttl: MaxTTL + time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := Generate(test.subject, test.application, test.organizationID, test.ttl)
			require.Error(t, err)
		})
	}
}

func TestOrganizationContextTokenRejectsMalformedInput(t *testing.T) {
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", "organization-context-test-secret-at-least-32-bytes")
	organizationID := uuid.Must(uuid.NewV4())
	for name, token := range map[string]string{
		"empty":             "",
		"missing signature": "payload",
		"extra segment":     "payload.signature.extra",
		"oversized":         string(make([]byte, MaxTokenLength+1)),
	} {
		t.Run(name, func(t *testing.T) {
			assert.False(t, Validate(token, "subject-1", "eventiapp", organizationID))
		})
	}
	assert.False(t, Validate("payload.signature", "", "eventiapp", organizationID))
	assert.False(t, Validate("payload.signature", "subject-1", "", organizationID))
	assert.False(t, Validate("payload.signature", "subject-1", "eventiapp", uuid.Nil))
}

func BenchmarkValidate(b *testing.B) {
	b.Setenv("ORGANIZATION_CONTEXT_SECRET", "organization-context-test-secret-at-least-32-bytes")
	organizationID := uuid.Must(uuid.NewV4())
	token, _, err := Generate("subject-1", "eventiapp", organizationID, DefaultTTL)
	require.NoError(b, err)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !Validate(token, "subject-1", "eventiapp", organizationID) {
			b.Fatal("valid token rejected")
		}
	}
}
