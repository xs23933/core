package xid

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

const (
	alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

var (
	decodeTable         [256]int8
	base                = int64(len(alphabet))
	ErrInvalidCharacter = errors.New("invalid character for encoding")
	salt                int64
)

func init() {

	for i := range decodeTable {
		decodeTable[i] = -1
	}
	for i := range len(alphabet) {
		decodeTable[alphabet[i]] = int8(i)
	}
	rand.Seed(time.Now().UnixNano())

}

// Config 配置
type Config struct {
	NodeID      int64
	CounterBits uint8
	Salt        int64
}

type Generator struct {
	mu         sync.Mutex
	nodeID     int64
	lastMS     int64
	counter    int64
	counterMax int64
	epoch      int64
	salt       int64
}

// 新建生成器
func New(cfg *Config) *Generator {
	counterBits := cfg.CounterBits
	if counterBits == 0 {
		counterBits = 12 // 默认12位序列号，1ms内4096个ID
	}
	salt = cfg.Salt
	return &Generator{
		nodeID:     cfg.NodeID & 0xFFFF,
		counterMax: (1 << counterBits) - 1,
	}
}

func (g *Generator) MustGenerate() ID {
	return g.NextID()
}

type ID int64

func (id ID) Int64() int64 {
	return int64(id)
}

func (id ID) String() string {
	return strconv.FormatInt(int64(id), 10)
}
func (id ID) Encode() string {
	obf := id.obfuscate(id)
	return Encode(obf)
}

func (id ID) MarshalJSON() ([]byte, error) {
	return []byte(`"` + id.String() + `"`), nil
}

func (id ID) MarshalBinary() ([]byte, error) {
	return []byte(id.Encode()), nil
}

func (id *ID) UnmarshalBinary(data []byte) error {
	str := string(data)
	val, err := ParseString(str)
	if err != nil {
		return err
	}
	*id = val
	return nil
}

// JSON序列化相关
type JSONSyntaxError struct {
	Original []byte
}

func (e JSONSyntaxError) Error() string {
	return fmt.Sprintf("invalid xID: %q", string(e.Original))
}

func (id *ID) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || string(data) == "\"\"" {
		*id = 0
		return nil
	}

	if len(data) < 3 || data[0] != '"' || data[len(data)-1] != '"' {
		return JSONSyntaxError{Original: data}
	}

	val, err := ParseString(string(data[1 : len(data)-1]))
	if err != nil {
		return err
	}
	*id = val
	return nil
}

func (id *ID) IsZero() bool {
	return *id == 0
}

func (id *ID) IsValid() bool {
	return *id != 0
}

// GormDBDataType gorm 方言映射
func (ID) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	switch db.Dialector.Name() {
	case "sqlite":
		return "INTEGER" // SQLite特殊处理
	case "clickhouse":
		return "Int64" // ClickHouse推荐写法
	default:
		return "BIGINT" // 其他数据库都用BIGINT
	}
}

func (id *ID) Scan(value any) error {
	if value == nil {
		*id = 0
		return nil
	}

	switch v := value.(type) {
	case int64:
		*id = ID(v)
	case int32:
		*id = ID(v)
	case int:
		*id = ID(v)
	case string:
		parsed, err := ParseString(v)
		if err != nil {
			return err
		}
		*id = ID(parsed)
	case []byte:
		parsed, err := ParseString(string(v))
		if err != nil {
			return err
		}
		*id = ID(parsed)
	default:
		return fmt.Errorf("不支持的扫描类型: %T", value)
	}
	return nil
}

// 实现 GORM 的序列化接口
func (id ID) Value() (driver.Value, error) {
	return id.Int64(), nil
}

// GormValue 添加 GORM 特定的方法
func (id ID) GormValue(ctx context.Context, db *gorm.DB) clause.Expr {
	return clause.Expr{
		SQL:  "?",
		Vars: []any{id.Int64()},
	}
}

// ParseString 从字符串解析ID
func ParseString(s string) (ID, error) {
	if s == "" {
		return 0, ErrInvalidCharacter
	}

	// 优先尝试十进制解析
	if isDigits(s) {
		i, err := strconv.ParseInt(s, 10, 64)
		return ID(i), err
	}

	return Decode(s)
}

// 判断字符串是否为纯数字
func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Next 生成 8 位 Base62 ID

// Next 生成 8 位 Base62 ID
func (g *Generator) NextID() ID {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 时间戳 32 位，低 32 位毫秒差
	now := uint64(time.Now().UnixMilli() & 0xFFFFFFFF)

	if int64(now) == g.lastMS {
		g.counter++
		if g.counter > 0xFFF { // 每毫秒 4096 个 ID
			for int64(now) <= g.lastMS {
				now = uint64(time.Now().UnixMilli() & 0xFFFFFFFF)
			}
			g.counter = 0
		}
	} else {
		g.counter = 0
	}
	g.lastMS = int64(now)

	// 组合 48 位：
	// 32 位时间 + 6 位节点 + 10 位序列号 = 48 位
	raw := (now << 16) | (uint64(g.nodeID) << 10) | uint64(g.counter&0x3FF)

	// XOR 混淆
	obf := raw ^ uint64(g.salt)

	return ID(obf)
}

// ----------------------------
// 混淆
// ----------------------------
func (g *ID) obfuscate(id ID) ID {
	return ID(int64(id) ^ salt) // XOR 混淆，保证不膨胀
}

func (g *ID) deobfuscate(val ID) ID {
	return ID(int64(val) ^ salt)
}

// ----------------------------
// 生成可给用户使用的 8 位 Base62 ID
// ----------------------------
func (g *Generator) Next() string {
	raw := g.NextID()
	obf := raw.obfuscate(raw)

	// 最大值 48 位 → Base62 编码 8位足够
	return Encode(obf)
}

// Base62 Encode
func Encode(n ID) string {
	if n == 0 {
		return string(alphabet[0])
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{alphabet[n.Int64()%base]}, buf...)
		n /= ID(base)
	}
	return string(buf)
}

// Basebase Decode
func Decode(s string) (ID, error) {
	var n ID
	for i := 0; i < len(s); i++ {
		idx := decodeTable[s[i]]
		if idx < 0 {
			return 0, errors.New("invalid basebase character")
		}
		n = n*ID(base) + ID(idx)
	}
	return n.deobfuscate(n), nil
}
