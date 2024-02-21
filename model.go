package core

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/xs23933/uid"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func NewModel(conf Options, debug, colorful bool) (*DB, error) {
	var (
		db  *DB
		err error
	)
	tp := conf.GetString("type")
	dbType = tp
	dsn := conf.GetString("dsn")
	if dsn == "" {
		return nil, ErrNoConfig
	}
	var dial gorm.Dialector
	switch tp {
	case "mysql":
		dial = mysql.Open(dsn)
	case "pg":
		dial = postgres.Open(dsn)
	case "sqlite", "sqlite3":
		dial = sqlite.Open(dsn)
	}
	if !debug {
		db, err = gorm.Open(dial)
	} else {
		writer := new(Writers)
		db, err = gorm.Open(dial, &gorm.Config{
			Logger: logger.New(writer, logger.Config{
				SlowThreshold:             time.Second,
				LogLevel:                  logger.Info,
				IgnoreRecordNotFoundError: true,
				ParameterizedQueries:      false,
				Colorful:                  colorful,
			}),
		})
	}
	if err != nil {
		return nil, err
	}
	if debug {
		db = db.Debug()
	}
	D("%s Connected", tp)
	conn = db
	return db, err
}

type Model struct {
	ID        uid.UID         `gorm:"size:12;primaryKey" json:"id,omitempty"`
	CreatedAt time.Time       `json:"created_at" gorm:"<-:create"`
	UpdatedAt time.Time       `json:"updated_at"`
	DeletedAt *gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

func (m *Model) BeforeCreate(tx *DB) error {
	if m.ID.IsEmpty() {
		m.ID = uid.New()
	}
	return nil
}

type UUID struct {
	uuid.UUID
}

var UuidNil = UUID{uuid.Nil}

func NewUUID() UUID {
	uuid.EnableRandPool()
	return UUID{uuid.New()}
}

func (u UUID) IsEmpty() bool {
	return UUID{uuid.Nil} == u
}

func (u UUID) String() string {
	var js [32]byte
	encodeHex(js[:], u)
	if js == nullUUID {
		return ""
	}
	return string(js[:])
}

func (u UUID) Bytes() []byte {
	return u.UUID[:]
}

// Scan implements sql.Scanner so UUIDs can be read from databases transparently.
// Currently, database types that map to string and []byte are supported. Please
// consult database-specific driver documentation for matching types.
func (uuid *UUID) Scan(src any) error {
	switch src := src.(type) {
	case nil:
		return nil

	case string:
		// if an empty UUID comes from a table, we return a null UUID
		if src == "" {
			return nil
		}

		// see Parse for required string format
		u, err := UUIDFromString(src)
		if err != nil {
			return fmt.Errorf("Scan: %v", err)
		}

		*uuid = u

	case []byte:
		// if an empty UUID comes from a table, we return a null UUID
		if len(src) == 0 {
			return nil
		}

		// assumes a simple slice of bytes if 16 bytes
		// otherwise attempts to parse
		if len(src) != 16 {
			return uuid.Scan(string(src))
		}
		copy((uuid.UUID)[:], src)

	default:
		return fmt.Errorf("Scan: unable to scan type %T into UUID", src)
	}

	return nil
}

// Value implements sql.Valuer so that UUIDs can be written to databases
// transparently. Currently, UUIDs map to strings. Please consult
// database-specific driver documentation for matching types.
func (uuid UUID) Value() (driver.Value, error) {
	return uuid.String(), nil
}

// GormDataType gorm common data type
func (uuid UUID) GormDataType() string {
	return "CHAR(32)"
}

func (UUID) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	// use field.Tag, field.TagSettings gets field's tags
	// checkout https://github.com/go-gorm/gorm/blob/master/schema/field.go for all options

	// returns different database type based on driver name
	switch db.Dialector.Name() {
	case "mysql":
		return "CHAR(32)"
	case "sqlite":
		return "TEXT"
	case "postgres":
		return "CHAR(32)"
	}
	return ""
}

type JSON json.RawMessage

// Scan scan value into Jsonb, implements sql.Scanner interface
func (j *JSON) Scan(value interface{}) error {
	if value == nil {
		*j = JSON("null")
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		if len(v) > 0 {
			bytes = make([]byte, len(v))
			copy(bytes, v)
		}
	case string:
		bytes = []byte(v)
	default:
		return errors.New(fmt.Sprint("Failed to unmarshal JSONB value:", value))
	}

	result := json.RawMessage(bytes)
	*j = JSON(result)
	return nil
}

// Value return json value, implement driver.Valuer interface
func (j JSON) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return string(j), nil
}

// MarshalJSON to output non base64 encoded []byte
func (j JSON) MarshalJSON() ([]byte, error) {
	return json.RawMessage(j).MarshalJSON()
}

// UnmarshalJSON to deserialize []byte
func (j *JSON) UnmarshalJSON(b []byte) error {
	result := json.RawMessage{}
	err := result.UnmarshalJSON(b)
	*j = JSON(result)
	return err
}

func (j JSON) String() string {
	return string(j)
}

// GormDataType gorm common data type
func (JSON) GormDataType() string {
	return "json"
}
func (JSON) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	// use field.Tag, field.TagSettings gets field's tags
	// checkout https://github.com/go-gorm/gorm/blob/master/schema/field.go for all options

	// returns different database type based on driver name
	switch db.Dialector.Name() {
	case "mysql", "sqlite":
		return "JSON"
	case "postgres":
		return "JSONB"
	}
	return ""
}

func (js JSON) GormValue(ctx context.Context, db *gorm.DB) clause.Expr {
	if len(js) == 0 {
		return gorm.Expr("NULL")
	}

	data, _ := js.MarshalJSON()

	switch db.Dialector.Name() {
	case "mysql":
		if v, ok := db.Dialector.(*mysql.Dialector); ok && !strings.Contains(v.ServerVersion, "MariaDB") {
			return gorm.Expr("CAST(? AS JSON)", string(data))
		}
	}

	return gorm.Expr("?", string(data))
}

type Money float64

// ToFixed 保留几位小数
// Param fraction int
// return float64
func (m Money) ToFixed(fraction ...int) Money {
	places := 2
	if len(fraction) > 0 {
		places = fraction[0]
	}
	shift := math.Pow(10, float64(places))
	fv := 0.0000000001 + float64(m) //对浮点数产生.xxx999999999 计算不准进行处理
	return Money(math.Floor(fv*shift) / shift)
}

// ToFloor 保留 p 位小数, 向下取整
func (m Money) ToFloor(p int) Money {
	base := math.Pow(10, float64(p))
	return Money(math.Floor(float64(m)*base) / base)
}

// ToRound 保留 p 位小数,四舍五入
func (m Money) ToRound(p int) Money {
	base := math.Pow(10, float64(p))
	return Money(math.Round(float64(m)*base) / base)
}

func (m Money) IsEqual(x Money, fixed ...int) bool {
	fix := 3
	if len(fixed) > 0 {
		fix = fixed[0]
	}
	return m.ToFixed(fix) == x.ToFixed(fix)
}

// DivInt 除以整数
// m / in
// fraction in 保留小数位
func (m Money) DivInt(in int, fraction ...int) Money {
	out := m / Money(float64(in))
	if len(fraction) > 0 {
		return out.ToFixed(fraction...)
	}
	return out
}

// MulInt 乘以整数
// m * in
// fraction in 保留小数位
func (m Money) MulInt(in int, fraction ...int) Money {
	out := m * Money(float64(in))
	if len(fraction) > 0 {
		return out.ToFixed(fraction...)
	}
	return out
}

// AddInt 加整数
// m + in
// fraction in 保留小数位
func (m Money) AddInt(in int, fraction ...int) Money {
	out := m + Money(float64(in))
	if len(fraction) > 0 {
		return out.ToFixed(fraction...)
	}
	return out
}

// SubInt 减整数
// m - in
// fraction in 保留小数位
func (m Money) SubInt(in int, fraction ...int) Money {
	out := m - Money(float64(in))
	if len(fraction) > 0 {
		return out.ToFixed(fraction...)
	}
	return out
}

// Float64 输出 float64
func (m Money) Float64() float64 {
	return float64(m)
}

// GormDataType schema.Field DataType
func (Money) GormDataType() string {
	return "DOUBLE"
}

func (m *Money) UnmarshalJSON(data []byte) error {
	str := string(data)
	var err error
	if bytes.HasPrefix(data, []byte{'"'}) {
		str, err = strconv.Unquote(str)
		if err != nil {
			return err
		}
	}
	tmp, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return err
	}
	*m = Money(tmp)
	return nil
}

type Int int64

// GormDataType schema.Field DataType
func (Int) GormDataType() string {
	return "INT"
}

// 转换结果为标准的 int64
func (m Int) Int64() int64 {
	return int64(m)
}

// 转换结果为标准的 int
func (m Int) Int() int {
	return int(m)
}

func (m *Int) UnmarshalJSON(data []byte) error {
	str := string(data)
	var err error
	if bytes.HasPrefix(data, []byte{'"'}) {
		str, err = strconv.Unquote(str)
		if err != nil {
			return err
		}
	}
	tmp, err := strconv.Atoi(str)
	if err != nil {
		return err
	}
	*m = Int(tmp)
	return nil
}

// xvalues returns the value of a byte as a hexadecimal digit or 255.
var xvalues = [256]byte{
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 255, 255, 255, 255, 255, 255,
	255, 10, 11, 12, 13, 14, 15, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 10, 11, 12, 13, 14, 15, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
}

// xtob converts hex characters x1 and x2 into a byte.
func xtob(x1, x2 byte) (byte, bool) {
	b1 := xvalues[x1]
	b2 := xvalues[x2]
	return (b1 << 4) | b2, b1 != 255 && b2 != 255
}

type invalidLengthError struct{ len int }

func (err invalidLengthError) Error() string {
	return fmt.Sprintf("invalid UUID length: %d", err.len)
}

// IsInvalidLengthError is matcher function for custom error invalidLengthError
func IsInvalidLengthError(err error) bool {
	_, ok := err.(invalidLengthError)
	return ok
}

var nullUUID = [32]byte{
	0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30,
	0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30,
	0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30,
	0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30,
}

func encodeHex(dst []byte, uuid UUID) {
	hex.Encode(dst, uuid.UUID[:])
}

// ParseBytes is like Parse, except it parses a byte slice instead of a string.
func ParseBytes(b []byte) (UUID, error) {
	var uuid UUID
	switch len(b) {
	case 36: // xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	case 36 + 9: // urn:uuid:xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
		if !bytes.Equal(bytes.ToLower(b[:9]), []byte("urn:uuid:")) {
			return uuid, fmt.Errorf("invalid urn prefix: %q", b[:9])
		}
		b = b[9:]
	case 36 + 2: // {xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx}
		b = b[1:]
	case 32: // xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
		var ok bool
		for i := 0; i < 32; i += 2 {
			uuid.UUID[i/2], ok = xtob(b[i], b[i+1])
			if !ok {
				return uuid, errors.New("invalid UUID format")
			}
		}
		return uuid, nil
	default:
		return uuid, invalidLengthError{len(b)}
	}
	// s is now at least 36 bytes long
	// it must be of the form  xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	if b[8] != '-' || b[13] != '-' || b[18] != '-' || b[23] != '-' {
		return uuid, errors.New("invalid UUID format")
	}
	for i, x := range [16]int{
		0, 2, 4, 6,
		9, 11,
		14, 16,
		19, 21,
		24, 26, 28, 30, 32, 34} {
		v, ok := xtob(b[x], b[x+1])
		if !ok {
			return uuid, errors.New("invalid UUID format")
		}
		uuid.UUID[i] = v
	}
	return uuid, nil
}

// MarshalText implements encoding.TextMarshaler.
func (id UUID) MarshalText() ([]byte, error) {
	var js [32]byte
	encodeHex(js[:], id)
	if js == nullUUID {
		return nil, nil
	}
	return js[:], nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (uuid *UUID) UnmarshalText(data []byte) error {
	if len(data) == 0 {
		*uuid = UuidNil
		return nil
	}
	id, err := ParseBytes(data)
	if err != nil {
		return err
	}
	*uuid = id
	return nil
}

func UUIDFromString(s string) (UUID, error) {
	uu, err := uuid.Parse(s)
	if err != nil {
		return UuidNil, err
	}
	return UUID{uu}, nil
}

type Models struct {
	ID        UUID            `json:"id,omitempty" gorm:"size:32;primaryKey"`
	CreatedAt time.Time       `json:"created_at" gorm:"<-:create"`
	UpdatedAt time.Time       `json:"updated_at"`
	DeletedAt *gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

func (m *Models) BeforeCreate(tx *DB) error {
	if m.ID.IsEmpty() {
		m.ID = NewUUID()
	}
	return nil
}

type Pages struct {
	P     int   `json:"p"`
	L     int   `json:"l"`
	Total int64 `json:"total"`
	Data  any   `json:"data"`
	Extra any   `json:"extra,omitempty"`
}

// FindPage Gorm find to page process whr
func FindPage(whr *Map, out any, db ...*DB) (result Pages, err error) {
	var (
		total    int64
		tx       *DB
		pos, lmt int
	)
	if len(db) > 0 {
		tx, pos, lmt = Where(whr, db[0])
	} else {
		tx, pos, lmt = Where(whr)
	}
	err = tx.Find(out).Offset(-1).Limit(-1).Count(&total).Error
	result = Pages{
		P: pos, L: lmt,
		Total: total,
		Data:  out,
	}
	return
}

type NextPages struct {
	P    int  `json:"p"`
	L    int  `json:"l"`
	Next bool `json:"next"`
	Prev bool `json:"prev"`
	Data any  `json:"data"`
}

func FindNext(whr *Map, out any, db ...*DB) (result NextPages, err error) {
	var (
		lmt = 20
		pos = 1
		tx  *DB
	)
	if len(db) > 0 {
		tx, pos, lmt = Where(whr, db[0])
	} else {
		tx, pos, lmt = Where(whr)
	}
	act := tx.Limit(lmt + 1).Find(out)
	rows := act.RowsAffected
	err = act.Error
	result = NextPages{
		P: pos, L: lmt,
		Next: rows > int64(lmt),
		Prev: pos > 1,
		Data: out,
	}
	return
}

// Find find all data record max 10000
func Find(out any, args ...any) error {
	wher := make(Map)
	db := Conn()
	for _, arg := range args {
		switch a := arg.(type) {
		case *Map:
			wher = *a
		case *DB:
			db = a
		}
	}
	if _, ok := wher["l"]; !ok {
		wher["l"] = 10000
	}
	db, _, _ = Where(&wher, db)
	return db.Find(out).Error
}

// Where build page query
//
//	whr *Map
//	db  *DB optional
//	return *DB, pos, lmt
func Where(whr *Map, db ...*DB) (*DB, int, int) {
	var tx *DB
	if len(db) > 0 {
		tx = db[0]
	} else {
		tx = conn
	}

	wher := map[string]any(*whr)

	lmt := 20
	l, ok := wher["l"]
	if ok {
		delete(wher, "l") //删除lmt
		switch v := l.(type) {
		case int:
			lmt = v
		case float64:
			lmt = int(v)
		}
	}
	tx = tx.Limit(lmt)

	p, ok := wher["p"]
	pos := 1
	if ok {
		delete(wher, "p") // 删除 pos
		switch v := p.(type) {
		case int:
			pos = v
		case float64:
			pos = int(v)
		}
		if pos < 1 {
			pos = 1
		}
		tx = tx.Offset((pos - 1) * lmt)
	}

	asc, ok := wher["asc"].(string)
	if ok {
		delete(wher, "asc")
		tx = tx.Order(asc)
	}
	desc, ok := wher["desc"].(string)
	if ok {
		delete(wher, "desc")
		tx = tx.Order(fmt.Sprintf("%s desc", desc))
	}

	if name, ok := wher["name"]; ok {
		delete(wher, "name")
		if name != "" {
			tx = tx.Where("name like ?", fmt.Sprintf("%%%s%%", name))
		}
	}

	if omit, ok := wher["omitFields"]; ok { // 排除相应字段 多个,号隔开
		delete(wher, "omitFields")
		tx = tx.Omit(omit.(string))
	}

	// 过滤掉字符串等于空 的搜索
	if len(wher) > 0 {
		for k, v := range wher {
			if v == nil {
				delete(wher, k)
				continue
			}
			if x, ok := v.(string); ok && len(x) == 0 {
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, " NOTIN") {
				tx = tx.Where(fmt.Sprintf("%s NOT IN (?)", strings.TrimSuffix(k, " NOTIN")), v)
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, " IN") {
				tx = tx.Where(fmt.Sprintf("%s in (?)", strings.TrimSuffix(k, " IN")), v)
				delete(wher, k)
				continue
			}
			if strings.HasPrefix(k, "^") {
				tx = tx.Where(fmt.Sprintf("%s like ?", strings.TrimPrefix(k, "^")), fmt.Sprintf("%s%%", v))
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, "$") {
				tx = tx.Where(fmt.Sprintf("%s like ?", strings.TrimSuffix(k, "$")), fmt.Sprintf("%%%s", v))
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, "*") {
				tx = tx.Where(fmt.Sprintf("%s like ?", strings.TrimSuffix(k, "*")), fmt.Sprintf("%%%s%%", v))
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, " !=") {
				tx = tx.Where(fmt.Sprintf("`%s` <> ?", strings.TrimSuffix(k, " !=")), v)
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, " >") || strings.HasSuffix(k, " <") ||
				strings.HasSuffix(k, " >=") || strings.HasSuffix(k, " <=") {
				ks := strings.Split(k, " ")
				ks[0] = fmt.Sprintf("`%s`", ks[0])
				ks = append(ks, "?")
				tx = tx.Where(strings.Join(ks, " "), v)
				delete(wher, k)
				continue
			}
		}
		tx = tx.Where(wher)
	}
	return tx, pos, lmt
}

func Conn() *DB {
	if conn != nil {
		return conn
	}
	Erro("database connect failed")
	return nil
}

func DBType() string {
	return dbType
}

var (
	conn   *DB
	dbType string
)

type DB = gorm.DB
