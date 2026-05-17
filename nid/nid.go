package nid

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

const (
	Alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	Size     = 6
	MaxValue = 36 * 36 * 36 * 36 * 36 * 36
)

var (
	ErrExhausted        = errors.New("nid exhausted")
	ErrInvalidLength    = errors.New("invalid nid length")
	ErrInvalidCharacter = errors.New("invalid nid character")
	ErrOutOfRange       = errors.New("nid out of range")

	Default = New()
)

type ID int64

func (id ID) Int64() int64 {
	return int64(id)
}

func (id ID) String() string {
	return strconv.FormatInt(id.Int64(), 10)
}

func (id ID) Encode() string {
	if id < 0 || id >= MaxValue {
		return ""
	}

	n := int64(id)
	var out [Size]byte
	for i := Size - 1; i >= 0; i-- {
		out[i] = Alphabet[n%int64(len(Alphabet))]
		n /= int64(len(Alphabet))
	}
	return string(out[:])
}

func (id ID) IsZero() bool {
	return id == 0
}

func (id ID) IsValid() bool {
	return id >= 0 && id < MaxValue
}

func (id ID) MarshalJSON() ([]byte, error) {
	return []byte(`"` + id.String() + `"`), nil
}

func (id *ID) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || string(data) == "\"\"" {
		*id = 0
		return nil
	}

	var raw string
	if len(data) >= 2 && data[0] == '"' && data[len(data)-1] == '"' {
		raw = string(data[1 : len(data)-1])
	} else if len(data) > 0 {
		raw = string(data)
	} else {
		return fmt.Errorf("invalid nid JSON: %q", string(data))
	}

	parsed, err := ParseString(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id ID) MarshalBinary() ([]byte, error) {
	encoded := id.Encode()
	if encoded == "" {
		return nil, ErrOutOfRange
	}
	return []byte(encoded), nil
}

func (id *ID) UnmarshalBinary(data []byte) error {
	parsed, err := Decode(string(data))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (ID) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	switch db.Dialector.Name() {
	case "sqlite":
		return "INTEGER"
	case "clickhouse":
		return "Int64"
	default:
		return "BIGINT"
	}
}

func (id ID) Value() (driver.Value, error) {
	return id.Int64(), nil
}

func (id *ID) Scan(value any) error {
	if value == nil {
		*id = 0
		return nil
	}

	switch v := value.(type) {
	case int64:
		return id.set(ID(v))
	case int32:
		return id.set(ID(v))
	case int:
		return id.set(ID(v))
	case float64:
		parsed, err := idFromFloat64(v)
		if err != nil {
			return err
		}
		return id.set(parsed)
	case float32:
		parsed, err := idFromFloat64(float64(v))
		if err != nil {
			return err
		}
		return id.set(parsed)
	case string:
		return id.parse(v)
	case []byte:
		return id.parse(string(v))
	default:
		return fmt.Errorf("unsupported nid scan type: %T", value)
	}
}

func (id ID) GormValue(ctx context.Context, db *gorm.DB) clause.Expr {
	return clause.Expr{
		SQL:  "?",
		Vars: []any{id.Int64()},
	}
}

func (id *ID) set(v ID) error {
	if !v.IsValid() {
		return ErrOutOfRange
	}
	*id = v
	return nil
}

func (id *ID) parse(s string) error {
	parsed, err := ParseString(s)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func ParseString(s string) (ID, error) {
	if s == "" {
		return 0, ErrInvalidLength
	}
	if isDigits(s) {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, err
		}
		id := ID(v)
		if !id.IsValid() {
			return 0, ErrOutOfRange
		}
		return id, nil
	}
	if isFloatDigits(s) {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, err
		}
		id, err := idFromFloat64(v)
		if err != nil {
			return 0, err
		}
		if !id.IsValid() {
			return 0, ErrOutOfRange
		}
		return id, nil
	}
	return Decode(s)
}

func Decode(s string) (ID, error) {
	if len(s) != Size {
		return 0, ErrInvalidLength
	}

	var id ID
	for i := 0; i < len(s); i++ {
		idx := decodeByte(s[i])
		if idx < 0 {
			return 0, ErrInvalidCharacter
		}
		id = id*ID(len(Alphabet)) + ID(idx)
	}
	return id, nil
}

func decodeByte(b byte) int {
	switch {
	case b >= 'a' && b <= 'z':
		return int(b - 'a')
	case b >= '0' && b <= '9':
		return int(b-'0') + 26
	default:
		return -1
	}
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isFloatDigits(s string) bool {
	dot := false
	digit := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.':
			if dot || i == 0 || i == len(s)-1 {
				return false
			}
			dot = true
		default:
			if s[i] < '0' || s[i] > '9' {
				return false
			}
			digit = true
		}
	}
	return dot && digit
}

func idFromFloat64(v float64) (ID, error) {
	id := ID(v)
	if float64(id) != v {
		return 0, fmt.Errorf("invalid non-integer nid: %v", v)
	}
	return id, nil
}

type Config struct {
	Start ID
}

type Generator struct {
	mu    sync.Mutex
	next  ID
	used  map[ID]struct{}
	count int64
}

func New(config ...Config) *Generator {
	start := randomStart()
	if len(config) > 0 && config[0].Start.IsValid() {
		start = config[0].Start
	}

	return &Generator{
		next: start,
		used: make(map[ID]struct{}),
	}
}

func Generate() (ID, error) {
	return Default.Generate()
}

func MustGenerate() ID {
	return Default.MustGenerate()
}

func (g *Generator) Generate() (ID, error) {
	if g == nil {
		return 0, errors.New("nil nid generator")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.count >= MaxValue {
		return 0, ErrExhausted
	}

	for {
		id := g.next
		g.next = (g.next + 1) % MaxValue
		if _, ok := g.used[id]; ok {
			continue
		}
		g.used[id] = struct{}{}
		g.count++
		return id, nil
	}
}

func (g *Generator) MustGenerate() ID {
	id, err := g.Generate()
	if err != nil {
		panic(err)
	}
	return id
}

func (g *Generator) Reserve(id ID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !id.IsValid() {
		return false
	}
	if _, ok := g.used[id]; ok {
		return false
	}
	g.used[id] = struct{}{}
	g.count++
	return true
}

func randomStart() ID {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return ID(binary.BigEndian.Uint64(b[:]) % MaxValue)
}
