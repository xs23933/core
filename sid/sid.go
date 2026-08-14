package sid

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// 常量定义
const (
	Epoch           int64 = 1774972800000
	DefaultNodeBits uint8 = 10
	DefaultStepBits uint8 = 12
	EncodeAlphabet        = "ABCDEFGHJKLMNPQRSTUVWXYZ123456789"
	Base62                = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz123456789"
)

var (
	// 预计算错误
	ErrInvalidNode      = errors.New("invalid node ID")
	ErrBitsAllocation   = errors.New("nodeBits + stepBits cannot exceed 22")
	ErrClockBackwards   = errors.New("clock moved backwards")
	ErrInvalidCharacter = errors.New("invalid character for encoding")

	// 解码表
	decodeTable [256]int8
	base        = int64(len(EncodeAlphabet))
)

func init() {
	// 初始化解码表
	for i := range decodeTable {
		decodeTable[i] = -1
	}
	for idx, ch := range EncodeAlphabet {
		decodeTable[ch] = int8(idx)
		// 同时映射小写字母
		if ch >= 'A' && ch <= 'Z' {
			decodeTable[ch+'a'-'A'] = int8(idx)
		}
	}

}

// SnowflakeID 雪花ID生成器
type SnowflakeID struct {
	mu       sync.Mutex
	epoch    time.Time
	lastTime int64
	nodeID   int64
	step     int64

	// 预计算的位运算常量
	nodeMax   int64
	nodeMask  int64
	stepMask  int64
	timeShift uint8
	nodeShift uint8
}

// Config 配置选项
type Config struct {
	NodeBits uint8
	StepBits uint8
}

// NewSnowflakeID 创建新的雪花ID生成器
func New(nodeID int64, config ...Config) (*SnowflakeID, error) {
	nodeBits := DefaultNodeBits
	stepBits := DefaultStepBits

	if len(config) > 0 {
		nodeBits = config[0].NodeBits
		stepBits = config[0].StepBits
	}

	// 验证位数配置
	if nodeBits+stepBits > 22 {
		return nil, ErrBitsAllocation
	}

	nodeMax := int64(-1) ^ (int64(-1) << nodeBits)
	if nodeID < 0 || nodeID > nodeMax {
		return nil, fmt.Errorf("%w: must be between 0 and %d", ErrInvalidNode, nodeMax)
	}

	// 计算位运算常量
	timeShift := nodeBits + stepBits
	nodeShift := stepBits
	nodeMask := nodeMax << stepBits
	stepMask := int64(-1) ^ (int64(-1) << stepBits)

	// 计算起始时间
	epochTime := time.Unix(Epoch/1000, (Epoch%1000)*int64(time.Millisecond))

	return &SnowflakeID{
		nodeID:    nodeID,
		epoch:     epochTime,
		nodeMax:   nodeMax,
		nodeMask:  nodeMask,
		stepMask:  stepMask,
		timeShift: timeShift,
		nodeShift: nodeShift,
	}, nil
}

// Generate 生成新的雪花ID
func (s *SnowflakeID) Generate() (ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	currentTime := time.Since(s.epoch).Milliseconds()

	// 处理时钟回拨
	if currentTime < s.lastTime {
		return 0, ErrClockBackwards
	}

	if currentTime == s.lastTime {
		s.step = (s.step + 1) & s.stepMask
		if s.step == 0 {
			// 当前毫秒的序列号用完，等待下一毫秒
			for currentTime <= s.lastTime {
				currentTime = time.Since(s.epoch).Milliseconds()
			}
		}
	} else {
		s.step = 0
	}

	s.lastTime = currentTime

	id := ID((currentTime << s.timeShift) |
		(s.nodeID << s.nodeShift) |
		s.step)

	return id, nil
}

// MustGenerate 生成ID，如果出错会panic（适用于确定不会出错的场景）
func (s *SnowflakeID) MustGenerate() ID {
	id, err := s.Generate()
	if err != nil {
		panic(err)
	}
	return id
}

// ID 雪花ID类型
type ID int64

// Int64 返回int64形式的ID
func (id ID) Int64() int64 {
	return int64(id)
}

// String 返回十进制字符串表示
func (id ID) String() string {
	return strconv.FormatInt(int64(id), 10)
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

func isFloatDigits(s string) bool {
	dot := false
	digit := false
	for i, c := range s {
		if c == '.' {
			if dot || i == 0 || i == len(s)-1 {
				return false
			}
			dot = true
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
		digit = true
	}
	return dot && digit
}

func idFromFloat64(v float64) (ID, error) {
	id := ID(v)
	if float64(id) != v {
		return 0, fmt.Errorf("invalid non-integer sid: %v", v)
	}
	return id, nil
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
	if isFloatDigits(s) {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, err
		}
		return idFromFloat64(v)
	}

	return Decode(s)
}

func MustParseString(s string) ID {
	id, err := ParseString(s)
	if err != nil {
		panic(err)
	}
	return id
}

// Encode 使用自定义base32编码ID为短字符串
func (id ID) Encode() string {
	if id == 0 {
		return string(EncodeAlphabet[0])
	}

	var b [13]byte // 最大长度预估
	i := len(b)

	for id > 0 {
		i--
		b[i] = EncodeAlphabet[id%ID(base)]
		id /= ID(base)
	}

	return string(b[i:])
}

// Decode 从编码字符串解析ID
func Decode(s string) (ID, error) {
	if s == "" {
		return 0, ErrInvalidCharacter
	}

	var id ID
	for _, ch := range s {
		if int(ch) >= len(decodeTable) {
			return 0, ErrInvalidCharacter
		}
		idx := decodeTable[ch]
		if idx < 0 {
			return 0, ErrInvalidCharacter
		}
		id = id*ID(base) + ID(idx)
	}
	return id, nil
}

func (id ID) Time(epoch time.Time) time.Time {
	sf := int64(id) >> 22 // 假设timeShift是22
	return epoch.Add(time.Duration(sf) * time.Millisecond)
}

// NodeID 提取ID中的节点ID部分
func (id ID) NodeID(nodeShift, nodeMask uint8) int64 {
	return (int64(id) >> nodeShift) & int64(nodeMask)
}

// Step 提取ID中的序列号部分
func (id ID) Step(stepMask int64) int64 {
	return int64(id) & stepMask
}

// JSON序列化相关
type JSONSyntaxError struct {
	Original []byte
}

func (e JSONSyntaxError) Error() string {
	return fmt.Sprintf("invalid snowflake ID: %q", string(e.Original))
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
		return JSONSyntaxError{Original: data}
	}

	parsedID, err := ParseString(raw)
	if err != nil {
		return err
	}

	*id = parsedID
	return nil
}

func (id *ID) IsZero() bool {
	return *id == 0
}

func (id *ID) IsValid() bool {
	return *id != 0
}

func (id *ID) IsNil() bool {
	return *id == 0
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

// 实现 GORM 的序列化接口
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
		*id = ID(v)
	case int32:
		*id = ID(v)
	case int:
		*id = ID(v)
	case uint64:
		if v > uint64(^uint64(0)>>1) {
			return fmt.Errorf("sid uint64 out of int64 range: %d", v)
		}
		*id = ID(v)
	case uint32:
		*id = ID(v)
	case uint:
		if strconv.IntSize == 64 && uint64(v) > uint64(^uint64(0)>>1) {
			return fmt.Errorf("sid uint out of int64 range: %d", v)
		}
		*id = ID(v)
	case float64:
		parsed, err := idFromFloat64(v)
		if err != nil {
			return err
		}
		*id = parsed
	case float32:
		parsed, err := idFromFloat64(float64(v))
		if err != nil {
			return err
		}
		*id = parsed
	case string:
		parsed, err := ParseString(v)
		if err != nil {
			return err
		}
		*id = parsed
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

// Expression 实现 GORM 的 Expression 接口，让 ID 在查询中自动转换为 int64
func (id ID) Expression() clause.Expression {
	return clause.Expr{
		SQL:  "?",
		Vars: []any{id.Int64()}, // 自动转换为 int64
	}
}
func (id ID) Build(builder clause.Builder) {
	builder.WriteString(fmt.Sprintf("%d", id.Int64()))
}

// GormValue 添加 GORM 特定的方法
func (id ID) GormValue(ctx context.Context, db *gorm.DB) clause.Expr {
	return clause.Expr{
		SQL:  "?",
		Vars: []any{id.Int64()},
	}
}
