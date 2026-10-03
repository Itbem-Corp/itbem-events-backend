package evidencejson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Validate rejects duplicate keys, Unicode case aliases, invalid Unicode escapes,
// excess nesting and multiple documents.
// Callers must bound input size and perform their own typed/schema validation.
// Go's struct decoder accepts repeated keys and case-insensitive field aliases.
// Reject ambiguous objects before decoding either side of a comparison.
func Validate(payload []byte) error {
	if !utf8.Valid(payload) {
		return fmt.Errorf("evidence JSON contains invalid UTF-8")
	}
	if err := validateUnicodeEscapes(payload); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return fmt.Errorf("evidence JSON nesting exceeds limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok {
					return fmt.Errorf("evidence JSON object key is invalid")
				}
				// Lowercasing misses Unicode aliases such as long s and Kelvin K.
				// Canonicalize the complete SimpleFold cycle, matching EqualFold.
				normalized := strings.Map(func(r rune) rune {
					minimum := r
					for alias := unicode.SimpleFold(r); alias != r; alias = unicode.SimpleFold(alias) {
						if alias < minimum {
							minimum = alias
						}
					}
					return minimum
				}, key)
				if seen[normalized] {
					return fmt.Errorf("evidence JSON object contains a duplicate or case-aliased field")
				}
				seen[normalized] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("evidence JSON delimiter is invalid")
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("evidence JSON must contain one document")
	}
	return nil
}

// encoding/json replaces unpaired surrogate escapes with U+FFFD. Reject them
// before decoding so evidence strings cannot silently change their meaning.
// Literal replacement characters and correctly paired surrogate escapes remain valid.
func validateUnicodeEscapes(payload []byte) error {
	inString := false
	for i := 0; i < len(payload); i++ {
		if payload[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || payload[i] != '\\' {
			continue
		}
		if i+1 >= len(payload) {
			return fmt.Errorf("evidence JSON contains an incomplete escape")
		}
		if payload[i+1] != 'u' {
			i++ // Escaped quotes and backslashes do not delimit strings.
			continue
		}
		if i+6 > len(payload) {
			return fmt.Errorf("evidence JSON contains an incomplete Unicode escape")
		}
		code, err := strconv.ParseUint(string(payload[i+2:i+6]), 16, 16)
		if err != nil {
			return fmt.Errorf("evidence JSON contains an invalid Unicode escape")
		}
		if code >= 0xdc00 && code <= 0xdfff {
			return fmt.Errorf("evidence JSON contains an unpaired low surrogate")
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+12 > len(payload) || payload[i+6] != '\\' || payload[i+7] != 'u' {
				return fmt.Errorf("evidence JSON contains an unpaired high surrogate")
			}
			low, err := strconv.ParseUint(string(payload[i+8:i+12]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return fmt.Errorf("evidence JSON contains an invalid surrogate pair")
			}
			i += 11
		} else {
			i += 5
		}
	}
	return nil
}
