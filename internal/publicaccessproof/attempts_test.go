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
		ok, _ := limiter.ReserveIPFailure(eventID, "203.0.113.7", false)
		assert.True(t, ok, "attempt %d", i+1)
	}
	ok, retry := limiter.ReserveIPFailure(eventID, "203.0.113.7", false)
	assert.False(t, ok)
	assert.Equal(t, PasswordAttemptsPerIPWindow, retry)

	ok, _ = limiter.ReserveIPFailure(eventID, "198.51.100.9", false)
	assert.True(t, ok, "another IP has its own budget")
	ok, _ = limiter.ReserveIPFailure(otherEvent, "203.0.113.7", false)
	assert.True(t, ok, "another event has its own budget")

	now = now.Add(PasswordAttemptsPerIPWindow)
	ok, _ = limiter.ReserveIPFailure(eventID, "203.0.113.7", false)
	assert.True(t, ok, "window resets")
}

func TestAttemptLimiterRefundKeepsOnlyFailures(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	limiter := NewAttemptLimiter(nil, func() time.Time { return now })
	eventID := uuid.Must(uuid.NewV4())

	for i := 0; i < PasswordAttemptsPerIP*5; i++ {
		ok, _ := limiter.ReserveIPFailure(eventID, "203.0.113.7", false)
		assert.True(t, ok, "successful attempt %d", i+1)
		limiter.RefundIPFailure(eventID, "203.0.113.7")
	}
	// A refused reservation is refunded, so an IP sitting at the cap stays at it.
	for i := 0; i < PasswordAttemptsPerIP; i++ {
		ok, _ := limiter.ReserveIPFailure(eventID, "203.0.113.7", false)
		assert.True(t, ok)
	}
	for i := 0; i < 3; i++ {
		ok, _ := limiter.ReserveIPFailure(eventID, "203.0.113.7", false)
		assert.False(t, ok)
	}
	// A refund never goes below zero.
	other := uuid.Must(uuid.NewV4())
	limiter.RefundIPFailure(other, "203.0.113.7")
	limiter.RefundIPFailure(other, "203.0.113.7")
	for i := 0; i < PasswordAttemptsPerIP; i++ {
		ok, _ := limiter.ReserveIPFailure(other, "203.0.113.7", false)
		assert.True(t, ok)
	}
	ok, _ := limiter.ReserveIPFailure(other, "203.0.113.7", false)
	assert.False(t, ok)
}

func TestAttemptLimiterUnderAttackTightensPerIPCap(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	limiter := NewAttemptLimiter(nil, func() time.Time { return now })
	eventID := uuid.Must(uuid.NewV4())

	for i := 0; i < PasswordFailuresPerIPUnderAttack; i++ {
		ok, _ := limiter.ReserveIPFailure(eventID, "203.0.113.7", true)
		assert.True(t, ok)
	}
	ok, _ := limiter.ReserveIPFailure(eventID, "203.0.113.7", true)
	assert.False(t, ok)
	ok, _ = limiter.ReserveIPFailure(eventID, "198.51.100.9", true)
	assert.True(t, ok, "a fresh IP still gets its tightened budget")
}

func TestClientKeyGroupsIPv6By64(t *testing.T) {
	assert.Equal(t, "203.0.113.7", ClientKey(" 203.0.113.7 "))
	assert.Equal(t, "203.0.113.7", ClientKey("::ffff:203.0.113.7"))
	assert.Equal(t, ClientKey("2001:db8:1:2::1"), ClientKey("2001:db8:1:2:ffff:abcd:1234:9"))
	assert.Equal(t, "2001:db8:1:2::/64", ClientKey("2001:db8:1:2::1"))
	assert.NotEqual(t, ClientKey("2001:db8:1:2::1"), ClientKey("2001:db8:1:3::1"))
	assert.Equal(t, "not-an-ip", ClientKey("not-an-ip"))
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
