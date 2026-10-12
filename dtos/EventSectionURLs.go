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

// ValidateEventSectionConfigURLs walks a section config (JSON) and checks
// every string value whose key looks like a link (…url, …href, …link) at any
// depth. It only runs on create/update; stored configs are never rejected on
// read (STU-N4).
func ValidateEventSectionConfigURLs(config string) error {
	trimmed := strings.TrimSpace(config)
	if trimmed == "" {
		return nil
	}
	var value interface{}
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil // JSON validity is NormalizeEventSectionConfig's job.
	}
	return validateSectionURLValue("", value)
}

func validateSectionURLValue(key string, value interface{}) error {
	switch typed := value.(type) {
	case map[string]interface{}:
		for childKey, child := range typed {
			if err := validateSectionURLValue(childKey, child); err != nil {
				return err
			}
		}
	case []interface{}:
		for _, child := range typed {
			if err := validateSectionURLValue(key, child); err != nil {
				return err
			}
		}
	case string:
		if isSectionLinkKey(key) && !sectionURLSchemeAllowed(typed) {
			return InvalidSectionURLError{Field: key}
		}
	}
	return nil
}

func isSectionLinkKey(key string) bool {
	lower := strings.ToLower(key)
	return strings.HasSuffix(lower, "url") || strings.HasSuffix(lower, "href") || strings.HasSuffix(lower, "link")
}

// sectionURLSchemeAllowed mirrors how browsers read a URL: ASCII whitespace
// and control characters are dropped before the scheme is taken, so
// "java\tscript:" is still javascript:.
func sectionURLSchemeAllowed(raw string) bool {
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
		return true
	}
	if cut := strings.IndexAny(value, "/?#"); cut >= 0 && cut < colon {
		return true // the colon belongs to a path, query or fragment
	}
	scheme := value[:colon]
	for i, r := range scheme {
		isLetter := r >= 'a' && r <= 'z'
		isOther := (r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.'
		if !isLetter && (i == 0 || !isOther) {
			return true // not a scheme (e.g. "10:30")
		}
	}
	_, ok := allowedSectionURLSchemes[scheme]
	return ok
}
