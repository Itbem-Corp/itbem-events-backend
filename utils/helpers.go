package utils

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

func PtrInt(v int) *int {
	return &v
}

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9-]+`)
var multiDash = regexp.MustCompile(`-{2,}`)

// Slugify converts a string to a URL-friendly slug.
// "Boda Ivanna y Andrés 2" → "boda-ivanna-y-andres-2"
func Slugify(s string) string {
	// Remove diacritics (accents)
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	result, _, _ := transform.String(t, s)

	result = strings.ToLower(strings.TrimSpace(result))
	result = strings.ReplaceAll(result, " ", "-")
	result = nonAlphaNum.ReplaceAllString(result, "")
	result = multiDash.ReplaceAllString(result, "-")
	result = strings.Trim(result, "-")
	return result
}
