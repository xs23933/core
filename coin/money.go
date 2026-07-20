package coin

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// ==========================================
// 全局配置
// ==========================================
var (
	// 默认输出小数位数（0 表示输出所有小数位）
	defaultOutputDecimals = 8
	configMutex           sync.RWMutex
)

// SetDefaultOutputDecimals 设置默认输出小数位数
// decimals: 0 表示输出所有小数位，1-8 表示保留指定位数
func SetDefaultOutputDecimals(decimals int) {
	configMutex.Lock()
	defer configMutex.Unlock()
	if decimals < 0 || decimals > 8 {
		decimals = 8
	}
	defaultOutputDecimals = decimals
}

// GetDefaultOutputDecimals 获取默认输出小数位数
func GetDefaultOutputDecimals() int {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return defaultOutputDecimals
}

// ==========================================
// 常量与基数
// ==========================================
const MoneyScale = 8

var (
	MoneyBase = new(big.Int).Exp(big.NewInt(10), big.NewInt(MoneyScale), nil) // 10^8
	oneMoney  = Money{v: new(big.Int).Set(MoneyBase)}
)

func One() Money {
	return oneMoney
}

// ==========================================
// Money 结构体
// 内部使用 big.Int 存储最小单位（固定 8 位小数）
// ==========================================
type Money struct {
	v *big.Int // 最小单位
}

// ==========================================
// 构造函数
// ==========================================
func Zero() Money {
	return Money{v: big.NewInt(0)}
}

func (m Money) norm() *big.Int {
	if m.v == nil {
		return big.NewInt(0)
	}
	return m.v
}

func FromInt(v *big.Int) Money {
	if v == nil {
		return Zero()
	}
	return Money{v: new(big.Int).Set(v)}
}

func FromInt64(v int64) Money {
	return Money{v: big.NewInt(v)}
}

func MustParse(s string) Money {
	m, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return m
}

func Parse(s string) (Money, error) {
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
		// 改四舍五入
		fracPart = fracPart[:MoneyScale]
		// ⚠️ 直接拒绝，不四舍五入
		// return Zero(), errors.New("money precision overflow")
	}

	fracPart += strings.Repeat("0", MoneyScale-len(fracPart))
	raw := intPart + fracPart

	n := new(big.Int)
	if _, ok := n.SetString(raw, 10); !ok {
		return Zero(), errors.New("invalid money string")
	}

	if neg {
		n.Neg(n)
	}
	return Money{v: n}, nil
}

// ParseMoney 从字符串解析，例如 "123.45678901"
func ParseMoney(s string) (Money, error) {
	return Parse(s)
}

// FormatDecimals 按指定小数位格式化
// decimals: 0 表示输出所有小数位，1-8 表示保留指定位数
func (m Money) FormatDecimals(decimals int) string {
	if m.v == nil {
		return "0." + strings.Repeat("0", decimals)
	}

	sign := ""
	val := new(big.Int).Set(m.v)
	if val.Sign() < 0 {
		sign = "-"
		val.Abs(val)
	}

	s := val.Text(10)
	if len(s) <= MoneyScale {
		s = strings.Repeat("0", MoneyScale-len(s)+1) + s
	}

	intPart := s[:len(s)-MoneyScale]
	fullFracPart := s[len(s)-MoneyScale:]

	// 按指定小数位截取
	if decimals >= 0 && decimals <= 8 {
		if decimals == 0 {
			// 去掉末尾的 0
			fullFracPart = strings.TrimRight(fullFracPart, "0")
			if fullFracPart == "" {
				return sign + intPart
			}
			return sign + intPart + "." + fullFracPart
		}
		// 保留指定位数
		if decimals <= len(fullFracPart) {
			fracPart := fullFracPart[:decimals]
			// 去掉末尾的 0
			fracPart = strings.TrimRight(fracPart, "0")
			if fracPart == "" {
				return sign + intPart
			}
			return sign + intPart + "." + fracPart
		}
		// 补零
		fracPart := fullFracPart + strings.Repeat("0", decimals-len(fullFracPart))
		return sign + intPart + "." + fracPart
	}

	// 默认返回完整 8 位
	return sign + intPart + "." + fullFracPart
}

// ==========================================
// 输出（支持小数位配置）
// ==========================================
func (m Money) String() string {
	return m.FormatDecimals(GetDefaultOutputDecimals())
}

// StringFull 输出完整 8 位小数
func (m Money) StringFull() string {
	return m.FormatDecimals(8)
}

func (m Money) Int() *big.Int {
	if m.v == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(m.norm())
}

// ==========================================
// JSON
// ==========================================
func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.String())
}

func (m *Money) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)

	// null
	if bytes.Equal(data, []byte("null")) {
		*m = Zero()
		return nil
	}

	// string: "123.45"
	if len(data) > 0 && data[0] == '"' {
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

	// number: 123.45 / 0 / 1
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

// MarshalJSONWithDecimals 按指定小数位序列化
func (m Money) MarshalJSONWithDecimals(decimals int) ([]byte, error) {
	return json.Marshal(m.FormatDecimals(decimals))
}

// MoneyJSON 用于临时指定小数位的包装器
type MoneyJSON struct {
	Money
	Decimals int
}

func (mj MoneyJSON) MarshalJSON() ([]byte, error) {
	return mj.Money.MarshalJSONWithDecimals(mj.Decimals)
}

// ==========================================
// GORM / SQL
// ==========================================
func (m Money) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	if field.TagSettings != nil {
		if t, ok := field.TagSettings["TYPE"]; ok && t != "" {
			return t
		}
	}
	switch db.Dialector.Name() {
	case "clickhouse", "mysql", "postgres", "sqlite":
		return "DECIMAL(28,8)"
	default:
		return "DECIMAL(28,8)"
	}
}

func (m Money) Value() (driver.Value, error) {
	return m.String(), nil
}

func (m *Money) Scan(value any) error {
	if value == nil {
		*m = Zero()
		return nil
	}

	switch v := value.(type) {
	case string:
		cm, err := ParseMoney(v)
		if err != nil {
			return err
		}
		*m = cm
	case []byte:
		cm, err := ParseMoney(string(v))
		if err != nil {
			return err
		}
		*m = cm
	default:
		return fmt.Errorf("unsupported Scan type: %T", value)
	}
	return nil
}

// ==========================================
// 基础运算
// ==========================================
func (m Money) Add(x Money) Money {
	if m.v == nil {
		m.v = new(big.Int)
	}
	if x.v == nil {
		x.v = new(big.Int)
	}

	return Money{v: new(big.Int).Add(m.norm(), x.v)}
}

func (m Money) Sub(x Money) Money {
	return Money{v: new(big.Int).Sub(m.norm(), x.v)}
}

// Mul 乘法
func (m Money) Mul(x Money) Money {
	return Money{v: new(big.Int).Mul(m.norm(), x.norm())}
}
func (m Money) Div(x Money) Money {
	return Money{v: new(big.Int).Div(m.norm(), x.norm())}
}

// MulInt 整数乘法
func (m Money) MulInt(n int64) Money {
	return Money{v: new(big.Int).Mul(m.norm(), big.NewInt(n))}
}

func (m Money) DivInt(n int64) Money {
	if n == 0 {
		return Zero()
	}
	return Money{v: new(big.Int).Div(m.norm(), big.NewInt(n))}
}

// MulRatio 向下截断的比例乘法
// 等价于：m * numerator / denominator
func (m Money) MulRatio(numerator, denominator *big.Int) Money {
	if denominator == nil || denominator.Sign() == 0 {
		return Zero()
	}
	tmp := new(big.Int).Mul(m.norm(), numerator)
	tmp.Div(tmp, denominator) // 向下截断
	return Money{v: tmp}
}

// MulRate 基于 MoneyBase 的比例乘法
// rate 是 *1e8 表示的小数（如 0.002 = 200000）
func (m Money) MulRate(rate *big.Int) Money {
	return m.MulRatio(rate, MoneyBase)
}

// MulRatioFloor
// 返回 floor(m * numerator / denominator)
func (m Money) MulRatioFloor(
	numerator,
	denominator *big.Int,
) Money {

	// 安全兜底
	if m.v == nil || m.v.Sign() == 0 {
		return Zero()
	}
	if numerator == nil || numerator.Sign() == 0 {
		return Zero()
	}
	if denominator == nil || denominator.Sign() <= 0 {
		panic("MulRatioFloor: invalid denominator")
	}

	// m.v * numerator
	num := new(big.Int).Mul(m.v, numerator)

	// floor(num / denominator)
	num.Div(num, denominator)

	return Money{v: num}
}

// DivIntExact 精确除法（必须整除）
//
// 如果除数不是 n 的倍数，则返回错误。
func (m Money) DivIntExact(n int64) (Money, error) {
	div := big.NewInt(n)
	mod := new(big.Int).Mod(m.norm(), div)
	if mod.Sign() != 0 {
		return Zero(), fmt.Errorf("division not exact")
	}
	return Money{v: new(big.Int).Div(m.norm(), div)}, nil
}

// Abs 绝对值
//
// Abs returns the absolute value of x.
func (m Money) Abs() Money {
	return Money{v: new(big.Int).Abs(m.norm())}
}

// Cmp 比较
// Cmp compares x and y and returns:
//
// -1 if x < y;
//
// 0 if x == y;
//
// +1 if x > y.
func (m Money) Cmp(x Money) int {
	return m.norm().Cmp(x.norm())
}

// IsZero 是否为0
func (m Money) IsZero() bool {
	return m.norm().Sign() == 0
}

// IsNegative 是否为负数
func (m Money) IsNegative() bool {
	return m.norm().Sign() < 0
}

// Less 小于
func (m Money) Less(x Money) bool {
	return m.Cmp(x) < 0
}

// Greater 大于
func (m Money) Greater(x Money) bool {
	return m.Cmp(x) > 0
}

// LessThanOrEqual 小于等于
func (m Money) LessThanOrEqual(o Money) bool {
	return m.norm().Cmp(o.norm()) <= 0
}

// GreaterThanOrEqual 大于等于
func (m Money) GreaterThanOrEqual(o Money) bool {
	return m.Cmp(o) >= 0
}

// ==========================================
// ERC20 Token 转换
// ==========================================

// TokenAmount → Money
// decimals: ERC20 decimals
func FromToken(token *big.Int, decimals uint8) Money {
	if token == nil {
		return Zero()
	}
	v := new(big.Int).Set(token)
	diff := int(decimals) - MoneyScale
	if diff > 0 {
		v.Div(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(diff)), nil))
	} else if diff < 0 {
		v.Mul(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-diff)), nil))
	}
	return Money{v: v}
}

// Money → TokenAmount
func (m Money) ToToken(decimals uint8) (*big.Int, error) {
	if m.v == nil {
		return big.NewInt(0), nil
	}
	v := new(big.Int).Set(m.v)
	diff := int(decimals) - MoneyScale
	if diff > 0 {
		v.Mul(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(diff)), nil))
	} else if diff < 0 {
		div := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-diff)), nil)
		mod := new(big.Int).Mod(v, div)
		if mod.Sign() != 0 {
			return nil, fmt.Errorf("amount precision overflow: %s cannot convert to %d decimals", m.String(), decimals)
		}
		v.Div(v, div)
	}
	return v, nil
}
