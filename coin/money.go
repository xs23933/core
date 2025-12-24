package coin

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// ==========================================
// 常量与基数
// ==========================================
const CoinScale = 8

var (
	coinBase = new(big.Int).Exp(big.NewInt(10), big.NewInt(CoinScale), nil) // 10^8
)

// ==========================================
// CoinMoney 结构体
// 内部使用 big.Int 存储最小单位（固定 8 位小数）
// ==========================================
type CoinMoney struct {
	v *big.Int // 最小单位
}

// ==========================================
// 构造函数
// ==========================================
func Zero() CoinMoney {
	return CoinMoney{v: big.NewInt(0)}
}

func (m CoinMoney) norm() *big.Int {
	if m.v == nil {
		return big.NewInt(0)
	}
	return m.v
}

func FromInt(v *big.Int) CoinMoney {
	if v == nil {
		return Zero()
	}
	return CoinMoney{v: new(big.Int).Set(v)}
}

func FromInt64(v int64) CoinMoney {
	return CoinMoney{v: big.NewInt(v)}
}

// ParseCoinMoney 从字符串解析，例如 "123.45678901"
func ParseCoinMoney(s string) (CoinMoney, error) {
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

	if len(fracPart) > CoinScale {
		return Zero(), errors.New("too many decimal places")
	}
	fracPart += strings.Repeat("0", CoinScale-len(fracPart))
	raw := intPart + fracPart

	n := new(big.Int)
	if _, ok := n.SetString(raw, 10); !ok {
		return Zero(), errors.New("invalid money string")
	}

	if neg {
		n.Neg(n)
	}
	return CoinMoney{v: n}, nil
}

// ==========================================
// 输出
// ==========================================
func (m CoinMoney) String() string {
	if m.v == nil {
		return "0.00000000"
	}
	sign := ""
	val := new(big.Int).Set(m.v)
	if val.Sign() < 0 {
		sign = "-"
		val.Abs(val)
	}

	s := val.Text(10)
	if len(s) <= CoinScale {
		s = strings.Repeat("0", CoinScale-len(s)+1) + s
	}
	intPart := s[:len(s)-CoinScale]
	fracPart := s[len(s)-CoinScale:]
	return sign + intPart + "." + fracPart
}

func (m CoinMoney) Int() *big.Int {
	if m.v == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(m.v)
}

// ==========================================
// JSON
// ==========================================
func (m CoinMoney) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.String())
}

func (m *CoinMoney) UnmarshalJSON(data []byte) error {
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
		cm, err := ParseCoinMoney(s)
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

	cm, err := ParseCoinMoney(num.String())
	if err != nil {
		return err
	}
	*m = cm
	return nil
}

// ==========================================
// GORM / SQL
// ==========================================
func (m CoinMoney) GormDBDataType(db *gorm.DB, field *schema.Field) string {
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

func (m CoinMoney) Value() (driver.Value, error) {
	if m.v == nil {
		return "0", nil
	}
	return m.String(), nil
}

func (m *CoinMoney) Scan(value any) error {
	if value == nil {
		*m = Zero()
		return nil
	}

	switch v := value.(type) {
	case string:
		cm, err := ParseCoinMoney(v)
		if err != nil {
			return err
		}
		*m = cm
	case []byte:
		cm, err := ParseCoinMoney(string(v))
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
func (m CoinMoney) Add(x CoinMoney) CoinMoney {
	return CoinMoney{v: new(big.Int).Add(m.norm(), x.v)}
}

func (m CoinMoney) Sub(x CoinMoney) CoinMoney {
	return CoinMoney{v: new(big.Int).Sub(m.norm(), x.v)}
}

func (m CoinMoney) MulInt(n int64) CoinMoney {
	return CoinMoney{v: new(big.Int).Mul(m.norm(), big.NewInt(n))}
}

func (m CoinMoney) DivInt(n int64) CoinMoney {
	return CoinMoney{v: new(big.Int).Div(m.norm(), big.NewInt(n))}
}

// 精确除法（必须整除）
func (m CoinMoney) DivIntExact(n int64) (CoinMoney, error) {
	div := big.NewInt(n)
	mod := new(big.Int).Mod(m.norm(), div)
	if mod.Sign() != 0 {
		return Zero(), fmt.Errorf("division not exact")
	}
	return CoinMoney{v: new(big.Int).Div(m.norm(), div)}, nil
}

func (m CoinMoney) Abs() CoinMoney {
	return CoinMoney{v: new(big.Int).Abs(m.norm())}
}

func (m CoinMoney) Cmp(x CoinMoney) int {
	return m.v.Cmp(x.norm())
}

func (m CoinMoney) IsZero() bool {
	return m.norm().Sign() == 0
}

func (m CoinMoney) IsNegative() bool {
	return m.norm().Sign() < 0
}

// ==========================================
// ERC20 Token 转换
// ==========================================

// TokenAmount → CoinMoney
// decimals: ERC20 decimals
func FromToken(token *big.Int, decimals uint8) CoinMoney {
	if token == nil {
		return Zero()
	}
	v := new(big.Int).Set(token)
	diff := int(decimals) - CoinScale
	if diff > 0 {
		v.Div(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(diff)), nil))
	} else if diff < 0 {
		v.Mul(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-diff)), nil))
	}
	return CoinMoney{v: v}
}

// CoinMoney → TokenAmount
func (m CoinMoney) ToToken(decimals uint8) (*big.Int, error) {
	if m.v == nil {
		return big.NewInt(0), nil
	}
	v := new(big.Int).Set(m.v)
	diff := int(decimals) - CoinScale
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
