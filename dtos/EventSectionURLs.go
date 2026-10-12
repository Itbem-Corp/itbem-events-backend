package dtos

import (
	"encoding/json"
	"fmt"
	"strings"
)

// InvalidSectionURLCode is the "error" code of a section config rejected
// because a link field uses a scheme other than the allowed ones (400).
const InvalidSectionURLCode = "invalid_section_url"

// InvalidSectionURLError names the config field whose URL was rejected.
type InvalidSectionURLError struct {
	Field string
}

func (e InvalidSectionURLError) Error() string {
	return fmt.Sprintf("El enlace de «%s» no es válido: usa una dirección que empiece con https:// o http://.", e.Field)
}

// allowedSectionURLSchemes are the schemes a section link may use. Anything
// else (javascript:, data:, vbscript:, file:...) is rejected on write; a value
// without a scheme (a relative path or a storage key) is accepted.
var allowedSectionURLSchemes = map[string]struct{}{
	"http": {}, "https": {}, "mailto": {}, "tel": {}, "geo": {},
}

// dangerousSectionURLSchemes are rejected in any string nested under a link
// container (e.g. socialLinks: {instagram: "javascript:..."}), where the
// child keys are free names and plain text (labels) must still be accepted.
var dangerousSectionURLSchemes = map[string]struct{}{
	"javascript": {}, "vbscript": {}, "data": {}, "file": {},
}

// sectionLinkKeySuffixes are the key endings treated as link fields (case
// insensitive): mapUrl, imageUrls, buttonLink, socialLinks, href, imgSrc,
// website...
var sectionLinkKeySuffixes = []string{"url", "urls", "href", "hrefs", "link", "links", "src", "website", "websites"}

// ValidateEventSectionConfigURLs walks a section config (JSON) and checks,
// at any depth:
//   - every string whose key looks like a link (see sectionLinkKeySuffixes),
//     including the items of an array under such a key, against the scheme
//     allowlist (http, https, mailto, tel, geo, or no scheme/relative);
//   - every other string nested in an object or array under a link key
//     against the dangerous schemes (javascript:, vbscript:, data:, file:).
//
// It only runs on create/update; stored configs are never rejected on read
// (STU-N4).
func ValidateEventSectionConfigURLs(config string) error {
	trimmed := strings.TrimSpace(config)
	if trimmed == "" {
		return nil
	}
	var value interface{}
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil // JSON validity is NormalizeEventSectionConfig's job.
	}
	return validateSectionURLValue("", "", value)
}

// validateSectionURLValue checks value stored under key; linkScope is the
// nearest enclosing link key ("" when none).
func validateSectionURLValue(key, linkScope string, value interface{}) error {
	switch typed := value.(type) {
	case map[string]interface{}:
		scope := linkScope
		if isSectionLinkKey(key) {
			scope = key
		}
		for childKey, child := range typed {
			if err := validateSectionURLValue(childKey, scope, child); err != nil {
				return err
			}
		}
	case []interface{}:
		for _, child := range typed {
			if err := validateSectionURLValue(key, linkScope, child); err != nil {
				return err
			}
		}
	case string:
		if isSectionLinkKey(key) {
			if !sectionURLSchemeAllowed(typed) {
				return InvalidSectionURLError{Field: key}
			}
			return nil
		}
		if linkScope != "" {
			if scheme, ok := sectionURLScheme(typed); ok {
				if _, dangerous := dangerousSectionURLSchemes[scheme]; dangerous {
					return InvalidSectionURLError{Field: linkScope}
				}
			}
		}
	}
	return nil
}

func isSectionLinkKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return false
	}
	for _, suffix := range sectionLinkKeySuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// sectionURLSchemeAllowed reports whether raw has no scheme (relative path,
// storage key, "10:30") or an allowed one.
func sectionURLSchemeAllowed(raw string) bool {
	scheme, ok := sectionURLScheme(raw)
	if !ok {
		return true
	}
	_, allowed := allowedSectionURLSchemes[scheme]
	return allowed
}

// sectionURLScheme mirrors how browsers read a URL: ASCII whitespace and
// control characters are dropped before the scheme is taken, so
// "java\tscript:" is still javascript:. ok is false when the value has no
// scheme.
func sectionURLScheme(raw string) (string, bool) {
	var cleaned strings.Builder
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f {
			continue
		}
		cleaned.WriteRune(r)
	}
	value := strings.ToLower(cleaned.String())
	colon := strings.IndexByte(value, ':')
	if colon <= 0 {
		return "", false
	}
	if cut := strings.IndexAny(value, "/?#"); cut >= 0 && cut < colon {
		return "", false // the colon belongs to a path, query or fragment
	}
	scheme := value[:colon]
	for i, r := range scheme {
		isLetter := r >= 'a' && r <= 'z'
		isOther := (r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.'
		if !isLetter && (i == 0 || !isOther) {
			return "", false // not a scheme (e.g. "10:30")
		}
	}
	return scheme, true
}
