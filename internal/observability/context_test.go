package observability

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCorrelationIDRoundTripsThroughContext(t *testing.T) {
	ctx := WithCorrelationID(context.Background(), "request-123")
	assert.Equal(t, "request-123", CorrelationID(ctx))
	assert.Empty(t, CorrelationID(context.Background()))
}

func TestSanitizeErrorRemovesURLsAndCredentialValues(t *testing.T) {
	got := SanitizeError(errors.New("POST https://hooks.slack.test/secret failed token=abc123"))
	assert.NotContains(t, got, "hooks.slack")
	assert.NotContains(t, got, "abc123")
}

func TestNormalizeCorrelationIDRejectsUnboundedOrUnsafeInput(t *testing.T) {
	assert.Equal(t, "request-123", NormalizeCorrelationID(" request-123 "))
	assert.Empty(t, NormalizeCorrelationID("contains spaces and ?query=secret"))
	assert.Empty(t, NormalizeCorrelationID(strings.Repeat("a", 65)))
}
