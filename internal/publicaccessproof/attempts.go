package publicaccessproof

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"github.com/redis/go-redis/v9"
)

// Brute-force limits for POST /api/events/:identifier/verify-access.
//
//   - Per event + client IP: every attempt counts (reserved before the
//     password is compared, so parallel bursts cannot overshoot).
//   - Per event, all IPs together: only failed attempts count, so hundreds of
//     guests opening the invitation at once with the right password are never
//     blocked, while a distributed guesser is capped.
const (
	PasswordAttemptsPerIP       = 10
	PasswordAttemptsPerIPWindow = 10 * time.Minute
	PasswordFailuresPerEvent    = 100
	PasswordFailuresEventWindow = 15 * time.Minute
)

// MinPasswordLength is the minimum length (in runes) of a newly set event password.
const MinPasswordLength = 6

// MaxPasswordLength bounds a newly set event password.
const MaxPasswordLength = 128

// PasswordMatches compares two passwords in constant time over their SHA-256
// digests, so neither the content nor the length of the stored value leaks
// through response timing.
func PasswordMatches(stored, candidate string) bool {
	storedDigest := sha256.Sum256([]byte(strings.TrimSpace(stored)))
	candidateDigest := sha256.Sum256([]byte(strings.TrimSpace(candidate)))
	return subtle.ConstantTimeCompare(storedDigest[:], candidateDigest[:]) == 1
}

// AttemptLimiter is a fixed-window counter keyed by event (and IP). It uses
// Redis/Valkey when a client is configured, so limits hold across API
// instances, and falls back to process memory when Redis is absent or fails.
type AttemptLimiter struct {
	redis  func() *redis.Client
	now    func() time.Time
	mu     sync.Mutex
	memory map[string]memoryWindow
}

type memoryWindow struct {
	count     int64
	expiresAt time.Time
}

// NewAttemptLimiter builds a limiter. redisClient may return nil.
func NewAttemptLimiter(redisClient func() *redis.Client, now func() time.Time) *AttemptLimiter {
	if now == nil {
		now = time.Now
	}
	return &AttemptLimiter{redis: redisClient, now: now, memory: map[string]memoryWindow{}}
}

var attemptWindowScript = redis.NewScript(`
local current = redis.call('INCRBY', KEYS[1], ARGV[2])
if current == tonumber(ARGV[2]) then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
return {current, ttl}
`)

// ReserveIPAttempt counts one attempt for event+IP and reports whether it is
// within the limit, plus how long until the window resets.
func (l *AttemptLimiter) ReserveIPAttempt(eventID uuid.UUID, ip string) (bool, time.Duration) {
	count, retry := l.add("event-password-ip", eventID.String()+"|"+strings.TrimSpace(ip), 1, PasswordAttemptsPerIPWindow)
	return count <= PasswordAttemptsPerIP, retry
}

// EventFailuresExceeded reports whether the event already reached its global
// failed-attempt budget, without counting a new failure.
func (l *AttemptLimiter) EventFailuresExceeded(eventID uuid.UUID) (bool, time.Duration) {
	count, retry := l.add("event-password-fail", eventID.String(), 0, PasswordFailuresEventWindow)
	return count >= PasswordFailuresPerEvent, retry
}

// RecordFailure counts one failed attempt against the event-wide budget.
func (l *AttemptLimiter) RecordFailure(eventID uuid.UUID) {
	l.add("event-password-fail", eventID.String(), 1, PasswordFailuresEventWindow)
}

func (l *AttemptLimiter) add(name, identifier string, delta int64, window time.Duration) (int64, time.Duration) {
	digest := sha256.Sum256([]byte(identifier))
	key := "security:rate-limit:" + name + ":" + hex.EncodeToString(digest[:])
	if l.redis != nil {
		if client := l.redis(); client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			values, err := attemptWindowScript.Run(ctx, client, []string{key}, window.Milliseconds(), delta).Int64Slice()
			if err == nil && len(values) == 2 {
				retry := time.Duration(values[1]) * time.Millisecond
				if retry < 0 {
					retry = 0
				}
				return values[0], retry
			}
			slog.Error("event password limiter: redis unavailable, using memory", "limiter", name, "error", err)
		}
	}
	return l.addMemory(key, delta, window)
}

func (l *AttemptLimiter) addMemory(key string, delta int64, window time.Duration) (int64, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.memory) > 50000 {
		for k, w := range l.memory {
			if !now.Before(w.expiresAt) {
				delete(l.memory, k)
			}
		}
	}
	current, ok := l.memory[key]
	if !ok || !now.Before(current.expiresAt) {
		if delta == 0 {
			return 0, 0
		}
		current = memoryWindow{expiresAt: now.Add(window)}
	}
	current.count += delta
	l.memory[key] = current
	return current.count, current.expiresAt.Sub(now)
}
