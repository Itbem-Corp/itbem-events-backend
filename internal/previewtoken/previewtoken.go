package previewtoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

func Generate(eventID uuid.UUID, ttl time.Duration) (string, error) {
	token, _, err := GenerateWithExpiry(eventID, ttl)
	return token, err
}

func GenerateWithExpiry(eventID uuid.UUID, ttl time.Duration) (string, time.Time, error) {
	secret := previewSecret()
	if secret == "" {
		return "", time.Time{}, fmt.Errorf("preview token secret is not configured")
	}
	expiresAt := time.Now().Add(ttl).UTC()
	payload := fmt.Sprintf("%s.%d", eventID.String(), expiresAt.Unix())
	signature := sign(payload, secret)
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "." + signature)), expiresAt, nil
}

func Validate(token string, eventID uuid.UUID) bool {
	secret := previewSecret()
	token = strings.TrimSpace(token)
	if token == "" || secret == "" {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	parts := strings.Split(string(decoded), ".")
	if len(parts) != 3 {
		return false
	}
	if parts[0] != eventID.String() {
		return false
	}
	expiresAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > expiresAt {
		return false
	}
	payload := parts[0] + "." + parts[1]
	expected := sign(payload, secret)
	return hmac.Equal([]byte(expected), []byte(parts[2]))
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func previewSecret() string {
	// Preview links are bearer credentials. Keep their signing key independent
	// from callback authentication and AWS/Cognito credentials so a compromise
	// in one trust boundary cannot mint credentials for another.
	return strings.TrimSpace(os.Getenv("EVENT_PREVIEW_SECRET"))
}
