package qaevidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// Go's struct decoder accepts repeated keys and case-insensitive field aliases.
// Reject ambiguous objects before decoding either side of a comparison.
func validateJSONIntegrity(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return fmt.Errorf("QA JSON nesting exceeds limit")
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
					return fmt.Errorf("QA JSON object key is invalid")
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
					return fmt.Errorf("QA JSON object contains a duplicate or case-aliased field")
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
			return fmt.Errorf("QA JSON delimiter is invalid")
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("QA JSON must contain one document")
	}
	return nil
}
