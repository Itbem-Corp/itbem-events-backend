package publicaccessproof

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

const purpose = "event-password-access"

type claims struct {
	Purpose       string `json:"purpose"`
	EventID       string `json:"eventId"`
	AccessVersion string `json:"accessVersion"`
	ExpiresAt     int64  `json:"expiresAt"`
}

func Generate(eventID uuid.UUID, accessVersion string, ttl time.Duration) (string, time.Time, error) {
	secret := accessSecret()
	if secret == "" {
		return "", time.Time{}, fmt.Errorf("event access token secret is not configured")
	}
	expiresAt := time.Now().Add(ttl).UTC()
	payload, err := json.Marshal(claims{
		Purpose:       purpose,
		EventID:       eventID.String(),
		AccessVersion: accessVersion,
		ExpiresAt:     expiresAt.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signature := sign(encodedPayload, secret)
	return encodedPayload + "." + signature, expiresAt, nil
}

func Validate(token string, eventID uuid.UUID, accessVersion string) bool {
	secret := accessSecret()
	token = strings.TrimSpace(token)
	if token == "" || secret == "" {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	if !hmac.Equal([]byte(sign(parts[0], secret)), []byte(parts[1])) {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var parsed claims
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return false
	}
	if parsed.Purpose != purpose || parsed.EventID != eventID.String() || parsed.AccessVersion != accessVersion {
		return false
	}
	return time.Now().Unix() <= parsed.ExpiresAt
}

// passwordVersionPurpose domain-separates the password version HMAC from the
// proof signature, which uses the same secret.
const passwordVersionPurpose = "event-password-version"

// PasswordVersion derives the access version for a password-protected event
// from the password itself, keyed with EVENT_ACCESS_SECRET. It changes only
// when the password changes (or the event differs), so unrelated config saves
// no longer rotate proofs. The value is public (meta.access.accessVersion); the
// server-side key keeps it from being an offline oracle for the password.
// Returns "" when there is no password or no secret (no proof can be minted).
func PasswordVersion(eventID uuid.UUID, password string) string {
	secret := accessSecret()
	password = strings.TrimSpace(password)
	if secret == "" || password == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(passwordVersionPurpose))
	mac.Write([]byte{0})
	mac.Write([]byte(eventID.String()))
	mac.Write([]byte{0})
	mac.Write([]byte(password))
	return "pw1." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))[:22]
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func accessSecret() string {
	// Password proofs and Studio previews are separate credential classes. Do
	// not silently reuse preview, callback, or AWS/Cognito secrets here.
	return strings.TrimSpace(os.Getenv("EVENT_ACCESS_SECRET"))
}
