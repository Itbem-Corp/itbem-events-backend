package observability

import (
	"context"
	"regexp"
	"strings"
)

type correlationKey struct{}

var sensitiveErrorPart = regexp.MustCompile(`(?i)(https?://\S+|(?:password|token|secret|webhook|authorization|cookie)\s*[=:]\s*\S+)`)
var validCorrelationID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func WithCorrelationID(ctx context.Context, id string) context.Context {
	if ctx == nil || id == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationKey{}, id)
}

func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	message := sensitiveErrorPart.ReplaceAllString(err.Error(), "<redacted>")
	message = strings.TrimSpace(message)
	if len(message) > 1024 {
		message = message[:1024]
	}
	return message
}

func CorrelationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

func NormalizeCorrelationID(value string) string {
	value = strings.TrimSpace(value)
	if !validCorrelationID.MatchString(value) {
		return ""
	}
	return value
}
