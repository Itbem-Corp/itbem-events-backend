package publicaccessproof

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"github.com/redis/go-redis/v9"
)

// Brute-force limits for POST /api/events/:identifier/verify-access.
//
//   - Per event + client (IPv6 grouped by /64): only FAILED attempts count.
//     A slot is reserved before the password is compared (so parallel bursts
//     cannot overshoot) and refunded when the password is right, so guests
//     sharing one venue Wi-Fi or CGNAT address are never blocked by correct
//     logins.
//   - Per event, all IPs together: failures are counted too, but reaching that
//     budget never blocks the event. It only tightens the per-IP failure cap to
//     PasswordFailuresPerIPUnderAttack, so a distributed guesser is slowed down
//     while a guest with the right password still gets in.
const (
	PasswordAttemptsPerIP            = 10
	PasswordAttemptsPerIPWindow      = 10 * time.Minute
	PasswordFailuresPerIPUnderAttack = 3
	PasswordFailuresPerEvent         = 100
	PasswordFailuresEventWindow      = 15 * time.Minute
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

var attemptRefundScript = redis.NewScript(`
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
if current > 0 then
  return redis.call('DECR', KEYS[1])
end
return 0
`)

// ClientKey normalizes a client IP for the per-IP counters: IPv4 (and
// IPv4-mapped IPv6) addresses are used as is, IPv6 addresses are grouped by
// their /64 prefix, because one subscriber usually owns a whole /64 and can
// rotate addresses inside it for free.
func ClientKey(ip string) string {
	trimmed := strings.TrimSpace(ip)
	parsed := net.ParseIP(trimmed)
	if parsed == nil {
		return trimmed
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String()
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

func ipFailureIdentifier(eventID uuid.UUID, ip string) string {
	return eventID.String() + "|" + ClientKey(ip)
}

// ReserveIPFailure reserves one failure slot for event+IP before the password
// is compared and reports whether it is within the limit, plus how long until
// the window resets. underAttack lowers the limit to
// PasswordFailuresPerIPUnderAttack. Call RefundIPFailure when the password
// turns out to be right, so only failures stay counted. A refused reservation
// is refunded here.
func (l *AttemptLimiter) ReserveIPFailure(eventID uuid.UUID, ip string, underAttack bool) (bool, time.Duration) {
	limit := int64(PasswordAttemptsPerIP)
	if underAttack {
		limit = PasswordFailuresPerIPUnderAttack
	}
	identifier := ipFailureIdentifier(eventID, ip)
	count, retry := l.add("event-password-ip", identifier, 1, PasswordAttemptsPerIPWindow)
	if count <= limit {
		return true, retry
	}
	l.refund("event-password-ip", identifier)
	return false, retry
}

// RefundIPFailure returns a slot reserved by ReserveIPFailure (the password
// was right, so the attempt is not a failure).
func (l *AttemptLimiter) RefundIPFailure(eventID uuid.UUID, ip string) {
	l.refund("event-password-ip", ipFailureIdentifier(eventID, ip))
}

// EventFailuresExceeded reports whether the event already reached its
// event-wide failed-attempt budget, without counting a new failure. Callers
// use it to tighten the per-IP cap, never to block the event outright.
func (l *AttemptLimiter) EventFailuresExceeded(eventID uuid.UUID) (bool, time.Duration) {
	count, retry := l.add("event-password-fail", eventID.String(), 0, PasswordFailuresEventWindow)
	return count >= PasswordFailuresPerEvent, retry
}

// RecordFailure counts one failed attempt against the event-wide budget.
func (l *AttemptLimiter) RecordFailure(eventID uuid.UUID) {
	l.add("event-password-fail", eventID.String(), 1, PasswordFailuresEventWindow)
}

func attemptKey(name, identifier string) string {
	digest := sha256.Sum256([]byte(identifier))
	return "security:rate-limit:" + name + ":" + hex.EncodeToString(digest[:])
}

// refund decrements a counter without going below zero and without touching
// its expiry; a missing or expired window is left alone.
func (l *AttemptLimiter) refund(name, identifier string) {
	key := attemptKey(name, identifier)
	if l.redis != nil {
		if client := l.redis(); client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			err := attemptRefundScript.Run(ctx, client, []string{key}).Err()
			if err == nil {
				return
			}
			slog.Error("event password limiter: redis refund failed, using memory", "limiter", name, "error", err)
		}
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	current, ok := l.memory[key]
	if !ok || !now.Before(current.expiresAt) || current.count <= 0 {
		return
	}
	current.count--
	l.memory[key] = current
}

func (l *AttemptLimiter) add(name, identifier string, delta int64, window time.Duration) (int64, time.Duration) {
	key := attemptKey(name, identifier)
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
