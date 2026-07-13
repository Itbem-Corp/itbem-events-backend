package utils

import (
	"net/url"
	"strings"
)

func IsAbsoluteURLLike(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "//") {
		return true
	}
	parsed, err := url.Parse(trimmed)
	return err == nil && parsed.Scheme != ""
}
