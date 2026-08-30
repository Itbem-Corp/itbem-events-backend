package gormrepository

import (
	"errors"
	"strings"
)

// validateQueryOptions keeps the generic repository safe when a caller builds
// options from request data. Values remain parameterized by GORM; identifiers
// and association paths need an explicit allow-list grammar because SQL cannot
// bind them as values.
func validateQueryOptions(opts QueryOptions) error {
	for field := range opts.Filters {
		if !isSafeIdentifierPath(field) {
			return errors.New("invalid filter field")
		}
	}
	for _, relation := range opts.Preload {
		if !isSafeIdentifierPath(relation) {
			return errors.New("invalid preload relation")
		}
	}
	if opts.OrderBy != "" && !isSafeIdentifierPath(opts.OrderBy) {
		return errors.New("invalid order field")
	}
	if opts.OrderDir != "" && normalizedOrderDirection(opts.OrderDir) == "" {
		return errors.New("invalid order direction")
	}
	return nil
}

func normalizedOrderDirection(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "ASC":
		return "ASC"
	case "DESC":
		return "DESC"
	default:
		return ""
	}
}

// isSafeIdentifierPath accepts ordinary identifiers, quoted PostgreSQL
// identifiers (needed for the legacy "order" column), and dotted paths used
// for GORM associations. It deliberately rejects SQL expressions.
func isSafeIdentifierPath(value string) bool {
	if value == "" {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if !isSafeIdentifier(part) {
			return false
		}
	}
	return true
}

func isSafeIdentifier(value string) bool {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	if value == "" {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}
