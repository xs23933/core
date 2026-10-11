package core

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gorm.io/gorm/clause"
)

// ErrInvalidQuery indicates an invalid filter field or query control parameter.
var ErrInvalidQuery = errors.New("core: invalid query")

var queryFieldPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// queryFilterField accepts only the documented filter grammar. SQL fragments
// belong in trusted, caller-built GORM scopes, never in request Map keys.
func queryFilterField(key string) string {
	for _, suffix := range []string{" NOTIN", " IN", " !=", " >=", " <=", " >", " <", "*", "$"} {
		if field, ok := strings.CutSuffix(key, suffix); ok {
			return field
		}
	}
	return strings.TrimPrefix(key, "^")
}

func queryColumns(value any) ([]string, error) {
	raw, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("%w: column list must be a string", ErrInvalidQuery)
	}
	// An empty string retains the existing no-op ordering/omission behavior.
	if raw == "" {
		return nil, nil
	}
	columns := strings.Split(raw, ",")
	for i, column := range columns {
		columns[i] = strings.TrimSpace(column)
		if !queryFieldPattern.MatchString(columns[i]) {
			return nil, fmt.Errorf("%w: unsafe column list", ErrInvalidQuery)
		}
	}
	return columns, nil
}

// Validate every key before consuming controls or dropping empty values. An
// invalid filter must fail the whole query, rather than silently widen it.
func validateQueryWhere(where Map) error {
	for key, value := range where {
		switch key {
		case "p", "l", "name":
			continue
		case "asc", "desc", "omitFields":
			if _, err := queryColumns(value); err != nil {
				return fmt.Errorf("%w: invalid %s", ErrInvalidQuery, key)
			}
		default:
			if !queryFieldPattern.MatchString(queryFilterField(key)) {
				return fmt.Errorf("%w: unsafe filter field", ErrInvalidQuery)
			}
		}
	}
	return nil
}

func queryColumn(name string) clause.Column {
	parts := strings.Split(name, ".")
	if len(parts) == 2 {
		return clause.Column{Table: parts[0], Name: parts[1]}
	}
	return clause.Column{Name: name}
}

func quoteQueryColumn(tx *DB, name string) string {
	var builder strings.Builder
	for i, part := range strings.Split(name, ".") {
		if i > 0 {
			builder.WriteByte('.')
		}
		tx.Dialector.QuoteTo(&builder, part)
	}
	return builder.String()
}
