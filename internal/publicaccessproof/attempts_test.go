package publicaccessproof

import (
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
)

func TestPasswordMatchesIsExactAfterTrim(t *testing.T) {
	assert.True(t, PasswordMatches("secreto", "secreto"))
	assert.True(t, PasswordMatches("  secreto ", "secreto"))
	assert.False(t, PasswordMatches("secreto", "Secreto"))
	assert.False(t, PasswordMatches("secreto", "secret"))
	assert.False(t, PasswordMatches("secreto", ""))
}

func TestAttemptLimiterCapsAttemptsPerEventAndIP(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	limiter := NewAttemptLimiter(nil, func() time.Time { return now })
	eventID := uuid.Must(uuid.NewV4())
	otherEvent := uuid.Must(uuid.NewV4())

	for i := 0; i < PasswordAttemptsPerIP; i++ {
		ok, _ := limiter.ReserveIPAttempt(eventID, "203.0.113.7")
		assert.True(t, ok, "attempt %d", i+1)
	}
	ok, retry := limiter.ReserveIPAttempt(eventID, "203.0.113.7")
	assert.False(t, ok)
	assert.Equal(t, PasswordAttemptsPerIPWindow, retry)

	ok, _ = limiter.ReserveIPAttempt(eventID, "198.51.100.9")
	assert.True(t, ok, "another IP has its own budget")
	ok, _ = limiter.ReserveIPAttempt(otherEvent, "203.0.113.7")
	assert.True(t, ok, "another event has its own budget")

	now = now.Add(PasswordAttemptsPerIPWindow)
	ok, _ = limiter.ReserveIPAttempt(eventID, "203.0.113.7")
	assert.True(t, ok, "window resets")
}

func TestAttemptLimiterCapsFailuresPerEvent(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	limiter := NewAttemptLimiter(nil, func() time.Time { return now })
	eventID := uuid.Must(uuid.NewV4())

	exceeded, _ := limiter.EventFailuresExceeded(eventID)
	assert.False(t, exceeded)
	for i := 0; i < PasswordFailuresPerEvent-1; i++ {
		limiter.RecordFailure(eventID)
	}
	exceeded, _ = limiter.EventFailuresExceeded(eventID)
	assert.False(t, exceeded, "checking does not count a failure")
	limiter.RecordFailure(eventID)
	exceeded, retry := limiter.EventFailuresExceeded(eventID)
	assert.True(t, exceeded)
	assert.Equal(t, PasswordFailuresEventWindow, retry)

	now = now.Add(PasswordFailuresEventWindow)
	exceeded, _ = limiter.EventFailuresExceeded(eventID)
	assert.False(t, exceeded)
}
