package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

type CursorDirection string

const (
	CursorDirectionNext CursorDirection = "next"
	CursorDirectionPrev CursorDirection = "prev"
)

// CursorSpec defines a stable keyset order and the optional position to resume
// from. Fields must contain either a unique column or an ordered column followed
// by a unique tie-breaker, for example []string{"id"} or
// []string{"created_at", "id"}.
type CursorSpec struct {
	Token     string
	Direction CursorDirection
	Fields    []string
	Desc      bool
}

var ErrInvalidCursor = errors.New("core: invalid cursor")

const cursorTokenVersion = 1

var cursorFieldPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?$`)

type cursorToken struct {
	Version int               `json:"v"`
	Fields  []string          `json:"f"`
	Desc    bool              `json:"d"`
	Values  []json.RawMessage `json:"k"`
}

// EncodeCursorToken returns an opaque, URL-safe token that is bound to the
// supplied fields and sort direction. Values must follow the same order as
// fields.
func EncodeCursorToken(fields []string, desc bool, values ...any) (string, error) {
	if err := validateCursorFields(fields); err != nil {
		return "", err
	}
	if len(values) != len(fields) {
		return "", fmt.Errorf("%w: got %d values for %d fields", ErrInvalidCursor, len(values), len(fields))
	}

	rawValues := make([]json.RawMessage, len(values))
	for i, value := range values {
		if isNilCursorValue(value) {
			return "", fmt.Errorf("%w: field %q has a nil value", ErrInvalidCursor, fields[i])
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("%w: encode field %q: %v", ErrInvalidCursor, fields[i], err)
		}
		rawValues[i] = raw
	}

	raw, err := json.Marshal(cursorToken{
		Version: cursorTokenVersion,
		Fields:  append([]string(nil), fields...),
		Desc:    desc,
		Values:  rawValues,
	})
	if err != nil {
		return "", fmt.Errorf("%w: encode token: %v", ErrInvalidCursor, err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeCursorToken validates a token against spec and returns its ordered key
// values. JSON numbers are returned as json.Number so 64-bit IDs stay exact.
func DecodeCursorToken(spec CursorSpec) ([]any, error) {
	token, err := decodeCursorToken(spec)
	if err != nil {
		return nil, err
	}

	values := make([]any, len(token.Values))
	for i, raw := range token.Values {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&values[i]); err != nil {
			return nil, fmt.Errorf("%w: decode field %q", ErrInvalidCursor, spec.Fields[i])
		}
		if isNilCursorValue(values[i]) {
			return nil, fmt.Errorf("%w: field %q has a nil value", ErrInvalidCursor, spec.Fields[i])
		}
	}
	return values, nil
}

func findCursor[T any](params FindsParams, whr Map) (FindsResult[T], error) {
	result := FindsResult[T]{
		P:     1,
		L:     20,
		Data:  make([]T, 0),
		Extra: params.Extra,
	}
	if params.Cursor == nil {
		return result, fmt.Errorf("%w: cursor spec is required", ErrInvalidCursor)
	}
	spec := *params.Cursor
	if spec.Direction == "" {
		spec.Direction = CursorDirectionNext
	}
	if spec.Direction != CursorDirectionNext && spec.Direction != CursorDirectionPrev {
		return result, fmt.Errorf("%w: unsupported direction %q", ErrInvalidCursor, spec.Direction)
	}
	if err := validateCursorFields(spec.Fields); err != nil {
		return result, err
	}

	delete(whr, "p")
	delete(whr, "asc")
	delete(whr, "desc")

	var tx *DB
	var lmt int
	if params.DB != nil {
		tx, _, lmt = Where(&whr, params.DB)
	} else {
		tx, _, lmt = Where(&whr)
	}
	// Cursor mode is always keyset based. Clear an Offset inherited from a
	// caller-supplied GORM scope so the public no-OFFSET contract also holds
	// when Finds receives an already-scoped DB.
	tx = tx.Offset(-1)
	if lmt <= 0 {
		lmt = 20
	}
	result.L = lmt

	fields, err := cursorSchemaFields[T](tx, spec.Fields)
	if err != nil {
		return result, err
	}
	if spec.Token != "" {
		token, err := decodeCursorToken(spec)
		if err != nil {
			return result, err
		}
		values, err := cursorQueryValues(token.Values, fields)
		if err != nil {
			return result, err
		}
		tx = applyCursorBoundary(tx, spec, values)
	}

	queryDesc := spec.Desc
	if spec.Direction == CursorDirectionPrev {
		queryDesc = !queryDesc
	}
	for i, name := range spec.Fields {
		tx = tx.Order(clause.OrderByColumn{Column: cursorColumn(name), Desc: queryDesc, Reorder: i == 0})
	}

	data := make([]T, 0, lmt+1)
	act := tx.Limit(lmt + 1).Find(&data)
	if act.Error != nil {
		return result, act.Error
	}
	more := len(data) > lmt
	if more {
		data = data[:lmt]
	}
	if spec.Direction == CursorDirectionPrev {
		reverseCursorData(data)
	}

	hasNext := more
	hasPrev := false
	if spec.Direction == CursorDirectionPrev {
		hasPrev = more
		hasNext = spec.Token != ""
	} else {
		hasPrev = spec.Token != ""
	}
	result.CursorHasNext = &hasNext
	result.CursorHasPrev = &hasPrev
	result.Data = data
	if len(data) == 0 {
		return result, nil
	}

	if hasPrev {
		values, err := cursorValuesFromRow(data[0], fields)
		if err != nil {
			return result, err
		}
		result.PrevCursor, err = EncodeCursorToken(spec.Fields, spec.Desc, values...)
		if err != nil {
			return result, err
		}
	}
	if hasNext {
		values, err := cursorValuesFromRow(data[len(data)-1], fields)
		if err != nil {
			return result, err
		}
		result.NextCursor, err = EncodeCursorToken(spec.Fields, spec.Desc, values...)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func decodeCursorToken(spec CursorSpec) (cursorToken, error) {
	var token cursorToken
	if err := validateCursorFields(spec.Fields); err != nil {
		return token, err
	}
	if spec.Token == "" || len(spec.Token) > 8192 {
		return token, fmt.Errorf("%w: empty or oversized token", ErrInvalidCursor)
	}
	raw, err := base64.RawURLEncoding.DecodeString(spec.Token)
	if err != nil || len(raw) > 8192 {
		return token, fmt.Errorf("%w: malformed token", ErrInvalidCursor)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&token); err != nil {
		return token, fmt.Errorf("%w: malformed token", ErrInvalidCursor)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return token, fmt.Errorf("%w: malformed token", ErrInvalidCursor)
	}
	if token.Version != cursorTokenVersion || token.Desc != spec.Desc ||
		!reflect.DeepEqual(token.Fields, spec.Fields) || len(token.Values) != len(spec.Fields) {
		return cursorToken{}, fmt.Errorf("%w: token does not match cursor specification", ErrInvalidCursor)
	}
	return token, nil
}

func validateCursorFields(fields []string) error {
	if len(fields) < 1 || len(fields) > 2 {
		return fmt.Errorf("%w: cursor requires one or two fields", ErrInvalidCursor)
	}
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if !cursorFieldPattern.MatchString(field) {
			return fmt.Errorf("%w: unsafe field %q", ErrInvalidCursor, field)
		}
		if _, ok := seen[field]; ok {
			return fmt.Errorf("%w: duplicate field %q", ErrInvalidCursor, field)
		}
		seen[field] = struct{}{}
	}
	return nil
}

func cursorSchemaFields[T any](tx *gorm.DB, names []string) ([]*schema.Field, error) {
	statement := &gorm.Statement{DB: tx}
	var model T
	if err := statement.Parse(&model); err != nil {
		return nil, fmt.Errorf("%w: parse result model: %v", ErrInvalidCursor, err)
	}
	fields := make([]*schema.Field, len(names))
	for i, name := range names {
		column := name[strings.LastIndex(name, ".")+1:]
		field := statement.Schema.LookUpField(column)
		if field == nil {
			for _, candidate := range statement.Schema.Fields {
				if candidate.DBName == column {
					field = candidate
					break
				}
			}
		}
		if field == nil {
			return nil, fmt.Errorf("%w: field %q is not present in result model", ErrInvalidCursor, name)
		}
		fields[i] = field
	}
	return fields, nil
}

func cursorQueryValues(rawValues []json.RawMessage, fields []*schema.Field) ([]any, error) {
	values := make([]any, len(fields))
	for i, field := range fields {
		value := reflect.New(field.FieldType)
		if err := json.Unmarshal(rawValues[i], value.Interface()); err != nil {
			return nil, fmt.Errorf("%w: decode field %q", ErrInvalidCursor, field.DBName)
		}
		values[i] = value.Elem().Interface()
		if isNilCursorValue(values[i]) {
			return nil, fmt.Errorf("%w: field %q has a nil value", ErrInvalidCursor, field.DBName)
		}
	}
	return values, nil
}

func applyCursorBoundary(tx *gorm.DB, spec CursorSpec, values []any) *gorm.DB {
	operator := ">"
	if spec.Desc {
		operator = "<"
	}
	if spec.Direction == CursorDirectionPrev {
		if operator == ">" {
			operator = "<"
		} else {
			operator = ">"
		}
	}
	first := quoteCursorColumn(tx, spec.Fields[0])
	if len(spec.Fields) == 1 {
		return tx.Where(first+" "+operator+" ?", values[0])
	}
	second := quoteCursorColumn(tx, spec.Fields[1])
	return tx.Where(
		"("+first+" "+operator+" ?) OR ("+first+" = ? AND "+second+" "+operator+" ?)",
		values[0], values[0], values[1],
	)
}

func cursorColumn(name string) clause.Column {
	parts := strings.Split(name, ".")
	if len(parts) == 2 {
		return clause.Column{Table: parts[0], Name: parts[1]}
	}
	return clause.Column{Name: name}
}

func quoteCursorColumn(tx *gorm.DB, name string) string {
	var builder strings.Builder
	parts := strings.Split(name, ".")
	for i, part := range parts {
		if i > 0 {
			builder.WriteByte('.')
		}
		tx.Dialector.QuoteTo(&builder, part)
	}
	return builder.String()
}

func cursorValuesFromRow[T any](row T, fields []*schema.Field) ([]any, error) {
	value := reflect.ValueOf(row)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, fmt.Errorf("%w: nil result row", ErrInvalidCursor)
		}
		value = value.Elem()
	}
	values := make([]any, len(fields))
	for i, field := range fields {
		fieldValue, zero := field.ValueOf(context.Background(), value)
		if zero || isNilCursorValue(fieldValue) {
			return nil, fmt.Errorf("%w: field %q has an empty value", ErrInvalidCursor, field.DBName)
		}
		values[i] = fieldValue
	}
	return values, nil
}

func reverseCursorData[T any](data []T) {
	for left, right := 0, len(data)-1; left < right; left, right = left+1, right-1 {
		data[left], data[right] = data[right], data[left]
	}
}

func isNilCursorValue(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
