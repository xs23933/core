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
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/xs23933/core/v3/sid"
	"github.com/xs23933/uid"
	"gorm.io/driver/clickhouse"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func NewModel(conf Options, debug, colorful bool) (map[string]*DB, error) {

	nodeID := Conf.GetInt64("node_id", 1)
	SnID, _ = sid.New(nodeID)

	if conf.GetString("type") != "" && conf.GetString("dsn") != "" {
		db, err := openDB(conf, debug, colorful)
		if err != nil {
			return nil, err
		}
		conns["default"] = db
		dbsType["default"] = conf.GetString("type")
		return conns, nil
	}

	confs := Conf.GetMap("database")
	for name, cfg := range confs {
		c := cfg.(Options)
		db, err := openDB(c, debug, colorful)
		if err != nil {
			return nil, fmt.Errorf("db %s init failed: %w", name, err)
		}
		dbsType[name] = c.GetString("type")
		conns[name] = db
		D("Opened database connection %s %s", dbsType[name], name)
	}
	return conns, nil
}

func NewSnID() sid.ID {
	return SnID.MustGenerate()
}

func openDB(conf Options, debug, colorful bool) (db *DB, err error) {
	tp := conf.GetString("type")
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
	case "clickhouse":
		dial = clickhouse.Open(dsn)
	default:
		Erro("Unknown database type: %s", tp)
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
	maxOpenConns := conf.GetInt("max_open_conns", 100)
	maxIdleConns := conf.GetInt("max_idle_conns", 20)
	connMaxLifetime := conf.GetString("conn_max_lifetime", "300s")

	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxIdleConns)
	maxLifeTime, err := time.ParseDuration(connMaxLifetime)
	if err != nil {
		maxLifeTime = time.Second * 300
	}
	sqlDB.SetConnMaxLifetime(maxLifeTime)

	if debug {
		db = db.Debug()
	}
	D("%s Connected", tp)
	return db, err
}

type Model struct {
	ID        uid.UID         `gorm:"size:12;primaryKey" json:"id,omitempty"`
	CreatedAt time.Time       `json:"created_at,omitempty" gorm:"<-:create"`
	UpdatedAt time.Time       `json:"updated_at,omitempty" gorm:"autoUpdateTime"`
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

func (u UUID) MarshalBinary() (data []byte, err error) {
	return []byte(u.String()), nil
}

func (u *UUID) UnmarshalBinary(data []byte) error {
	if len(data) != 36 {
		return errors.New("invalid uuid")
	}
	var err error
	*u, err = UUIDFromString(string(data))
	return err
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

// ToStrings 把任意实现了 fmt.Stringer 的类型切片转换成 []string
//
//	e.g: core.ToStrings(uuids)
func ToStrings[T fmt.Stringer](items []T) []string {
	ret := make([]string, len(items))
	for i, v := range items {
		ret[i] = v.String()
	}
	return ret
}

// ToAny 把任意类型切片转换成 []any
//
//	e.g: core.ToAny(uuids)
func ToAny[T any](items []T) []any {
	ret := make([]any, len(items))
	for i, v := range items {
		ret[i] = v
	}
	return ret
}

// ToStringsFromAny 把 []any 转换成 []string
func ToStringsFromAny(items []any) []string {
	ret := make([]string, len(items))
	for i, v := range items {
		ret[i] = fmt.Sprint(v) // 等价于 v.(string) 但更安全
	}
	return ret
}

// ToUUIDsFromAny 把 []any 转换成 []UUID
func ToUUIDsFromAny(items []any) []UUID {
	ret := make([]UUID, 0, len(items))
	for _, v := range items {
		switch val := v.(type) {
		case string:
			ret = append(ret, MustUUID(val)) // 你的 UUID 解析函数
		case []byte:
			ret = append(ret, MustUUID(string(val)))
		default:
			// 如果传进来不是 string/[]byte，就 fmt.Sprint 转换
			ret = append(ret, MustUUID(fmt.Sprint(val)))
		}
	}
	return ret
}

// SafeToUUIDs
func SafeToUUIDs(items any) []UUID {
	switch vv := items.(type) {
	case []any:
		return ToUUIDsFromAny(vv)
	case string:
		arr := make([]string, 0)
		if err := sonic.UnmarshalString(vv, &arr); err == nil {
			return ToUUIDsFromAny(ToAny(arr))
		}
		return ToUUIDsFromAny(ToAny(strings.Split(vv, ",")))
	case []string:
		return ToUUIDsFromAny(ToAny(vv))
	case []UUID:
		return vv
	default:
		return nil
	}
}

type HasUUID interface {
	GetUUID() UUID
}

func ExtractUUIDs[T HasUUID](items []T) []UUID {
	ids := make([]UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.GetUUID())
	}
	return ids
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
//
//	func (m Money) ToFixed(fraction ...int) Money {
//		places := 2
//		if len(fraction) > 0 {
//			places = fraction[0]
//		}
//		shift := math.Pow(10, float64(places))
//		fv := 0.0000000001 + float64(m) //对浮点数产生.xxx999999999 计算不准进行处理
//		return Money(math.Floor(fv*shift) / shift)
//	}
func (m Money) ToFixed(fraction ...int) Money {
	places := 2
	if len(fraction) > 0 {
		places = fraction[0]
	}

	str := strconv.FormatFloat(float64(m), 'f', places, 64)
	f, _ := strconv.ParseFloat(str, 64)
	return Money(f)
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
	fix := 2
	if len(fixed) > 0 {
		fix = fixed[0]
	}
	eps := 1 / math.Pow10(fix)
	return math.Abs(float64(m-x)) < eps
	// return m.ToFixed(fix) == x.ToFixed(fix)
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

func (m Money) Abs() Money {
	if m < 0 {
		return -m
	}
	return m
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

func (m Money) EmvAmount() string {
	amt := m.MulInt(100).ToFixed(0)
	return fmt.Sprintf("%012d", int(amt))
}

// Float64 输出 float64
func (m Money) Float64() float64 {
	return float64(m)
}

func (m Money) Int() Int {
	return Int(int64(math.Floor(m.Float64())))
}

// GormDataType schema.Field DataType
// func (Money) GormDataType() string {
// 	return "DECIMAL(18,6)"
// }

// GormDBDataType gorm 方言映射 (不同数据库可指定不同字段类型)
func (Money) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	switch db.Dialector.Name() {
	case "clickhouse":
		return "DECIMAL(10,3)"
	case "mysql":
		return "DECIMAL(10,3)"
	case "postgres":
		return "DECIMAL(10,3)"
	case "sqlite":
		return "DECIMAL(10,3)"
	default:
		return "DECIMAL(10,3)"
	}
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
	// 移除千分位逗号
	str = strings.ReplaceAll(str, ",", "")

	if str == "null" {
		*m = Money(0.0)
		return nil
	}

	tmp, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return err
	}
	*m = Money(tmp)
	return nil
}

func (m Money) MarshalJSON() ([]byte, error) {
	str := strconv.FormatFloat(float64(m), 'f', 2, 64) // 保留两位小数
	return []byte(str), nil
}

func (m Money) MarshalBinary() (data []byte, err error) {
	str := strconv.FormatFloat(float64(m), 'f', -1, 64) // 保留两位小数
	return []byte(str), nil
}

func (m *Money) UnmarshalBinary(data []byte) error {
	return m.UnmarshalJSON(data)
}

func ParseMoney(val any) Money {
	switch v := val.(type) {
	case string:
		v = strings.ReplaceAll(v, ",", "")
		f, _ := strconv.ParseFloat(v, 64)
		return Money(f)
	case float64:
		return Money(v)
	case int:
		return Money(v)
	case int64:
		return Money(v)
	default:
		f, _ := strconv.ParseFloat(fmt.Sprintf("%v", v), 64)
		return Money(f)
	}
}

type IntMoney int64

// NewIntMoneyFromFloat 创建 IntMoney（内部存储分）
func NewIntMoneyFromFloat(f float64) IntMoney {
	return IntMoney(math.Round(f * 100)) // 四舍五入取整
}

// Float64 转换为 float64 元
func (m IntMoney) Float64() float64 {
	return float64(m) / 100
}

// String 格式化输出
func (m IntMoney) String() string {
	return fmt.Sprintf("%.2f", m.Float64())
}

// ToFixed 保留 fraction 位小数（默认 2 位）
func (m IntMoney) ToFixed(fraction ...int) float64 {
	places := 2
	if len(fraction) > 0 {
		places = fraction[0]
	}
	shift := math.Pow(10, float64(places))
	fv := m.Float64()
	return math.Floor(fv*shift+0.0000001) / shift
}

// IsEqual 比较是否相等，允许指定小数位比较
func (m IntMoney) IsEqual(x IntMoney, fraction ...int) bool {
	return m.ToFixed(fraction...) == x.ToFixed(fraction...)
}

// 基础加减乘除运算（返回 IntMoney）

// 加法
func (m IntMoney) Add(x IntMoney) IntMoney { return m + x }

// 减法
func (m IntMoney) Sub(x IntMoney) IntMoney { return m - x }

// 乘法
func (m IntMoney) MulInt(n int64) IntMoney { return m * IntMoney(n) }

// 除法
func (m IntMoney) DivInt(n int64) IntMoney {
	if n == 0 {
		return 0
	}
	return m / IntMoney(n)
}
func (m IntMoney) Abs() IntMoney {
	if m < 0 {
		return -m
	}
	return m
}
func (m IntMoney) EmvAmount() string { return fmt.Sprintf("%012d", m) }
func (m IntMoney) Int64() int64      { return int64(m) }

// ---------------- JSON 解析 ----------------

// UnmarshalJSON 反序列化
func (m *IntMoney) UnmarshalJSON(data []byte) error {
	str := string(data)
	var err error
	if bytes.HasPrefix(data, []byte{'"'}) {
		str, err = strconv.Unquote(str)
		if err != nil {
			return err
		}
	}

	str = strings.ReplaceAll(str, ",", "")
	if str == "null" || str == "" {
		*m = 0
		return nil
	}

	f, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return err
	}
	*m = NewIntMoneyFromFloat(f)
	return nil
}

// MarshalJSON 序列化
func (m IntMoney) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%.2f", m.Float64())), nil
}

// ParseIntMoney 通用解析
func ParseIntMoney(val any) IntMoney {
	switch v := val.(type) {
	case string:
		v = strings.ReplaceAll(v, ",", "")
		f, _ := strconv.ParseFloat(v, 64)
		return NewIntMoneyFromFloat(f)
	case float64:
		return NewIntMoneyFromFloat(v)
	case int:
		return IntMoney(v * 100)
	case int64:
		return IntMoney(v * 100)
	default:
		f, _ := strconv.ParseFloat(fmt.Sprintf("%v", v), 64)
		return NewIntMoneyFromFloat(f)
	}
}

// ---------------- SQL/数据库接口 ----------------

// Value 实现 driver.Valuer (写入数据库时存储为分)
func (m IntMoney) Value() (driver.Value, error) {
	return int64(m), nil
}

// Scan 实现 sql.Scanner (数据库读取时转为 IntMoney) 不做 100 倍转换
func IntMoneyFromRedisString(s string) IntMoney {
	if s == "" || s == "null" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return IntMoney(v)
}

// Scan 实现 sql.Scanner (数据库读取时转为 IntMoney)
// 支持 int64 / float64 / string
func (m *IntMoney) Scan(value any) error {
	if value == nil {
		*m = 0
		return nil
	}
	switch v := value.(type) {
	case int64:
		*m = IntMoney(v)
	case float64:
		*m = NewIntMoneyFromFloat(v)
	case []byte:
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return err
		}
		*m = NewIntMoneyFromFloat(f)
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		*m = NewIntMoneyFromFloat(f)
	default:
		return fmt.Errorf("unsupported Scan type for IntMoney: %T", value)
	}
	return nil
}

// ---------------- GORM 接口 ----------------

// GormDataType gorm 通用数据类型 (用于生成表结构)
func (IntMoney) GormDataType() string {
	return "bigint"
}

// GormDBDataType gorm 方言映射 (不同数据库可指定不同字段类型)
func (IntMoney) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	switch db.Dialector.Name() {
	case "clickhouse":
		return "Int64"
	case "mysql":
		return "BIGINT"
	case "postgres":
		return "BIGINT"
	case "sqlite":
		return "INTEGER"
	default:
		return "BIGINT"
	}
}

func (m IntMoney) MarshalBinary() (data []byte, err error) {
	return fmt.Appendf(nil, "%d", m), nil
}

func (m *IntMoney) UnmarshalBinary(data []byte) error {
	*m = IntMoneyFromRedisString(string(data))
	return nil
}

type Int int64

// GormDataType schema.Field DataType
func (Int) GormDataType() string {
	return "BIGINT"
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
	if s == "" {
		return UuidNil, nil
	}
	uu, err := uuid.Parse(s)
	if err != nil {
		return UuidNil, err
	}
	return UUID{uu}, nil
}

func MustUUID(s string) UUID {
	u, err := UUIDFromString(s)
	if err != nil {
		Erro("invalid UUID: %s", s)
		return UuidNil
	}
	return u
}

type Date struct {
	time.Time
}

func (d *Date) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	// 解析日期(只到天)
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

func (d Date) MarshalJSON() ([]byte, error) {
	str := d.Format("2006-01-02")
	return []byte(str), nil
}

func (d Date) String() string {
	str := d.Format("2006-01-02")
	return str
}

func (d Date) Value() (driver.Value, error) {
	return d.Format("2006-01-02T15:04:05"), nil
}

func (d *Date) Scan(value interface{}) error {
	switch t := value.(type) {
	case string:
		t2, _ := time.Parse("2006-01-02", t)
		*d = Date{Time: t2}
	case []byte:
		t2, _ := time.Parse("2006-01-02", string(t))
		*d = Date{Time: t2}
	case time.Time:
		*d = Date{Time: t}
	default:
		return fmt.Errorf("can not convert %v to Date", value)
	}
	return nil
}

type Models struct {
	ID        UUID            `json:"id,omitzero" gorm:"size:32;primaryKey"`
	CreatedAt *time.Time      `json:"created_at,omitempty" gorm:"<-:create"`
	UpdatedAt *time.Time      `json:"updated_at,omitempty" gorm:"autoUpdateTime"`
	DeletedAt *gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

func (m *Models) BeforeCreate(tx *DB) error {
	if m.ID.IsEmpty() {
		m.ID = NewUUID()
	}
	return nil
}

type SModels struct {
	ID        sid.ID          `json:"id,omitzero" gorm:"primaryKey;comment:主键"`
	CreatedAt *time.Time      `json:"created_at,omitempty" gorm:"<-:create;comment:创建时间"`
	UpdatedAt *time.Time      `json:"updated_at,omitempty" gorm:"autoUpdateTime;comment:更新时间"`
	DeletedAt *gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index;comment:删除时间"`
}

func (m *SModels) BeforeCreate(tx *DB) error {
	if m.ID == 0 {
		m.ID = SnID.MustGenerate()
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
	P     int  `json:"p"`
	L     int  `json:"l"`
	Next  bool `json:"next"`
	Prev  bool `json:"prev"`
	Data  any  `json:"data"`
	Extra any  `json:"extra,omitempty"`
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
		tx = Conn()
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

func Conn(name ...string) *DB {
	key := "default"
	if len(name) > 0 {
		key = name[0]
	}
	if db, ok := conns[key]; ok {
		return db
	}
	Erro("Database connect failed: %s", key)
	return nil
}

func DBType(name ...string) string {
	key := "default"
	if len(name) > 0 {
		key = name[0]
	}
	if key, ok := dbsType[key]; ok {
		return key
	}
	return ""
}

var (
	conns   = make(map[string]*DB)
	dbsType = make(map[string]string)
	SnID    *sid.SnowflakeID
)

type DB = gorm.DB

func Expr(expr string, args ...any) clause.Expr {
	return gorm.Expr(expr, args...)
}

type Enum interface {
	~uint8
}

// EnumString
//
//	func (s TypeX) String() string {
//		return EnumString(s, TypeXMap)
//	}
func EnumString[T Enum](val T, mapping []string) string {
	return mapping[val]
}

// EnumFromString
//
//	func TypeXFromString(str string) TypeX {
//		return EnumFromString[TypeX](str, TypeXMap)
//	}
func EnumFromString[T Enum](str string, mapping []string) T {
	for i, s := range mapping {
		if s == str {
			return T(i)
		}
	}
	// 如果 str 是数字，尝试转换
	if num, err := strconv.ParseUint(str, 10, 8); err == nil {
		return T(num)
	}
	return T(0)
}

// EnumMarshalJSON
//
//	func (s TypeX)MarshalJSON() ([]byte, error) {
//		return EnumMarshalJSON(s, TypeX)
//	}
func EnumMarshalJSON[T Enum](val T, mapping []string) ([]byte, error) {
	return sonic.Marshal(mapping[val])
}

// EnumUnmarshalJSON
//
//	func (s *TypeX) UnmarshalJSON(data []byte) error {
//		val, err := EnumUnmarshalJSON[TypeX](data, TypeXMap)
//		if err != nil {
//			return err
//		}
//		*s = val
//		return nil
//	}
func EnumUnmarshalJSON[T Enum](data []byte, mapping []string) (T, error) {
	var strData string
	if err := sonic.Unmarshal(data, &strData); err == nil {
		return EnumFromString[T](strData, mapping), nil
	}

	var num float64
	if err := sonic.Unmarshal(data, &num); err != nil {
		return T(0), err
	}
	if !math.IsInf(num, 0) && num == math.Trunc(num) {
		val := T(uint8(num))
		if val >= 0 && val < T(len(mapping)) {
			return val, nil
		}
	}
	tType := reflect.TypeOf((*T)(nil)).Elem().Name()
	return T(0), fmt.Errorf("invalid %v value: %v ", tType, data)
}

// WithTransaction
//
//	func (s *TypeX) WithTransaction(fn func(tx *core.DB) error) error {
//		return WithTransaction(s.DB, fn)
//	}
func WithTransaction(tx *DB, fn func(tx *DB) error) error {
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			Erro("WithTransaction error: %v, %s", r, debug.Stack())
		}
	}()
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}
