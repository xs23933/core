package coins

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const (
	MoneyScale = 8
	MoneyBase  = int64(100000000)
)

// ==========================================
// 全局配置
// ==========================================
var (
	// 默认 JSON 输出小数位数（3 表示输出所有非零小数位）
	defaultJSONDecimals = 3
	configMutex         sync.RWMutex
)

// SetDefaultJSONDecimals 设置默认 JSON 输出小数位数
// decimals: 0 表示输出所有非零小数位，1-8 表示保留指定位数
func SetDefaultJSONDecimals(decimals int) {
	configMutex.Lock()
	defer configMutex.Unlock()
	if decimals < 0 || decimals > 8 {
		decimals = 0
	}
	defaultJSONDecimals = decimals
}

// GetDefaultJSONDecimals 获取默认 JSON 输出小数位数
func GetDefaultJSONDecimals() int {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return defaultJSONDecimals
}

type Money struct {
	v int64
}

type integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

type moneyInput interface {
	~string | integer | ~float32 | ~float64
}

var ZeroMoney = Money{}

// ==============================
// 构造
// ==============================

func Zero() Money {
	return ZeroMoney
}

func FromInt64(v int64) Money {
	return Money{v}
}

// New constructs Money from whole or decimal coin units.
// Use FromInt64 when the value is already scaled by MoneyBase.
func New[T moneyInput](v T) Money {
	m, err := Parse(v)
	if err != nil {
		panic(err)
	}
	return m
}

func MustParse[T moneyInput](v T) Money {
	m, err := Parse(v)
	if err != nil {
		panic(err)
	}
	return m
}

func Parse[T moneyInput](v T) (Money, error) {
	s := formatMoneyInput(v)

	s = strings.TrimSpace(s)

	if s == "" {
		return Zero(), nil
	}

	neg := false

	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}

	parts := strings.SplitN(s, ".", 2)

	intPart := parts[0]
	fracPart := ""

	if len(parts) == 2 {
		fracPart = parts[1]
	}

	if len(fracPart) > MoneyScale {
		fracPart = fracPart[:MoneyScale]
	}

	fracPart += strings.Repeat("0", MoneyScale-len(fracPart))

	raw := intPart + fracPart

	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return Zero(), err
	}

	if neg {
		n = -n
	}

	return Money{n}, nil
}

func formatMoneyInput[T moneyInput](v T) string {
	rv := reflect.ValueOf(v)

	switch rv.Kind() {
	case reflect.String:
		return rv.String()
	case reflect.Float32:
		return strconv.FormatFloat(rv.Float(), 'f', -1, 32)
	case reflect.Float64:
		return strconv.FormatFloat(rv.Float(), 'f', -1, 64)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(rv.Uint(), 10)
	default:
		return strconv.FormatInt(rv.Int(), 10)
	}
}

// ==============================
// 输出
// ==============================

func (m Money) Int64() int64 {
	return m.v
}

func (m Money) Float64() float64 {
	return float64(m.v) / float64(MoneyBase)
}

func (m Money) String() string {
	return m.FormatDecimals(0) // 0 表示输出所有非零小数位
}

func (m Money) StringFixed(decimals int) string {
	if decimals < 0 || decimals > 8 {
		decimals = 8
	}
	return m.FormatDecimals(decimals)
}

// FormatDecimals 按指定小数位格式化
// decimals: 0 表示输出所有非零小数位，1-8 表示保留指定位数
func (m Money) FormatDecimals(decimals int) string {
	val := m.v

	sign := ""
	if val < 0 {
		sign = "-"
		val = -val
	}

	intPart := val / MoneyBase
	fracPart := val % MoneyBase

	if decimals == 0 {
		// 输出所有非零小数位
		frac := fmt.Sprintf("%08d", fracPart)
		frac = strings.TrimRight(frac, "0")
		if frac == "" {
			return fmt.Sprintf("%s%d", sign, intPart)
		}
		return fmt.Sprintf("%s%d.%s", sign, intPart, frac)
	}

	// 保留指定位数
	if decimals <= 8 {
		// 截取指定位数的小数
		div := int64(1)
		for i := 0; i < 8-decimals; i++ {
			div *= 10
		}
		fracPart = (fracPart / div) * div

		frac := fmt.Sprintf("%08d", fracPart)
		frac = frac[:decimals]

		// 去掉末尾的 0
		frac = strings.TrimRight(frac, "0")
		if frac == "" {
			return fmt.Sprintf("%s%d", sign, intPart)
		}
		return fmt.Sprintf("%s%d.%s", sign, intPart, frac)
	}

	// decimals > 8，补零
	frac := fmt.Sprintf("%08d", fracPart)
	frac += strings.Repeat("0", decimals-8)
	return fmt.Sprintf("%s%d.%s", sign, intPart, frac)
}

// ==============================
// JSON
// ==============================

func (m Money) MarshalJSON() ([]byte, error) {
	decimals := GetDefaultJSONDecimals()
	return m.MarshalJSONWithDecimals(decimals)
}

func (m Money) MarshalJSONWithDecimals(decimals int) ([]byte, error) {
	return json.Marshal(m.FormatDecimals(decimals))
}

func (m *Money) UnmarshalJSON(data []byte) error {

	data = bytes.TrimSpace(data)

	if bytes.Equal(data, []byte("null")) {
		*m = Zero()
		return nil
	}

	if data[0] == '"' {

		var s string

		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}

		cm, err := Parse(s)
		if err != nil {
			return err
		}

		*m = cm
		return nil
	}

	var num json.Number

	if err := json.Unmarshal(data, &num); err != nil {
		return err
	}

	cm, err := Parse(num.String())
	if err != nil {
		return err
	}

	*m = cm
	return nil
}

// MoneyJSON 用于临时指定小数位的包装器
type MoneyJSON struct {
	Money
	Decimals int
}

func (mj MoneyJSON) MarshalJSON() ([]byte, error) {
	return mj.Money.MarshalJSONWithDecimals(mj.Decimals)
}

// ==============================
// SQL / GORM
// ==============================

func (Money) GormDataType() string {
	return "bigint"
}

func (Money) GormDBDataType(db *gorm.DB, field *schema.Field) string {

	switch db.Dialector.Name() {

	case "mysql":
		return "BIGINT"

	case "postgres":
		return "BIGINT"

	case "clickhouse":
		return "Int64"

	case "sqlite":
		return "INTEGER"

	default:
		return "BIGINT"
	}
}

func (m Money) Value() (driver.Value, error) {
	return m.v, nil
}

func (m *Money) Scan(value any) error {

	switch v := value.(type) {

	case int64:
		m.v = v

	case uint64:
		m.v = int64(v)

	case float64:
		m.v = int64(v * float64(MoneyBase))

	case string:
		cm, err := Parse(v)
		if err != nil {
			return err
		}
		*m = cm

	case []byte:
		cm, err := Parse(string(v))
		if err != nil {
			return err
		}
		*m = cm

	default:
		return fmt.Errorf("unsupported Scan type: %T", value)
	}

	return nil
}

// ==============================
// 运算
// ==============================

func (m Money) Add(x Money) Money {
	return Money{m.v + x.v}
}

func (m Money) Sub(x Money) Money {
	return Money{m.v - x.v}
}

func (m Money) MulInt(n int64) Money {
	return Money{m.v * n}
}

func (m Money) DivInt(n int64) Money {
	return Money{m.v / n}
}

// ==============================
// 高精度比例计算
// ==============================

// floor(m * numerator / denominator)
func (m Money) MulRatio(numerator, denominator int64) Money {

	if denominator == 0 {
		return Zero()
	}

	return Money{(m.v * numerator) / denominator}
}

// rate = 1e8 表示小数
// 例如 0.002 -> 200000
func (m Money) MulRate(rate int64) Money {

	return Money{
		(m.v * rate) / MoneyBase,
	}
}

// ==============================
// 比较
// ==============================

func (m Money) Cmp(x Money) int {

	if m.v < x.v {
		return -1
	}

	if m.v > x.v {
		return 1
	}

	return 0
}

// Less 小于
func (m Money) Less(x Money) bool {
	return m.v < x.v
}

// Greater 大于
func (m Money) Greater(x Money) bool {
	return m.v > x.v
}

// Ge 基于 Cmp 实现的 >=
func (m Money) Ge(x Money) bool { // >=
	return m.Cmp(x) >= 0
}

// Le 居于 Cmp 实现的 <=
func (m Money) Le(x Money) bool { // <=
	return m.Cmp(x) <= 0
}

func (m Money) IsZero() bool {
	return m.v == 0
}

func (m Money) IsNegative() bool {
	return m.v < 0
}

func (m Money) Abs() Money {

	if m.v < 0 {
		return Money{-m.v}
	}

	return m
}

// ==============================
// 安全运算（防 overflow）
// ==============================

func (m Money) SafeAdd(x Money) (Money, error) {

	r := m.v + x.v

	if (x.v > 0 && r < m.v) || (x.v < 0 && r > m.v) {
		return Zero(), errors.New("money overflow")
	}

	return Money{r}, nil
}
