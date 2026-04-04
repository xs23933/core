package shortid

import (
	"errors"
	"sync"
	"time"
)

const (
	alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

var (
	decodeTable [256]int8
	Default     *Generator
	base        = int64(len(alphabet))
)

func init() {

	for i := range decodeTable {
		decodeTable[i] = -1
	}
	for i := 0; i < len(alphabet); i++ {
		decodeTable[alphabet[i]] = int8(i)
	}

	Default = NewGenerator(&Config{
		NodeID:      1,
		CounterBits: 16,
		EpochMS:     1774972800000,
		Salt:        0x5F,
	})
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

// Config 配置
type Config struct {
	NodeID      int64
	CounterBits uint8
	EpochMS     int64
	Salt        int64
}

// 新建生成器
func NewGenerator(cfg *Config) *Generator {
	counterBits := cfg.CounterBits
	if counterBits == 0 {
		counterBits = 8
	}
	return &Generator{
		nodeID:     cfg.NodeID & 0xFF,
		counterMax: (1 << counterBits) - 1,
		epoch:      cfg.EpochMS,
		salt:       cfg.Salt,
	}
}

// 生成原始 int64 ID
func (g *Generator) NextID() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	ms := (time.Now().UnixMilli() - g.epoch) & 0xFFFFF // 只取低20位
	if ms < g.lastMS {
		ms = g.lastMS
	}

	if ms == g.lastMS {
		g.counter++
		if g.counter > g.counterMax {
			for ms <= g.lastMS {
				ms = (time.Now().UnixMilli() - g.epoch) & 0xFFFFF
			}
			g.counter = 0
		}
	} else {
		g.counter = 0
	}
	g.lastMS = ms

	id := (ms << 16) | ((g.nodeID & 0xFF) << 8) | (g.counter & 0xFF)
	return id
}

// XOR 混淆，不膨胀
func (g *Generator) obfuscate(id int64) int64 {
	return id ^ g.salt
}

func (g *Generator) deobfuscate(id int64) int64 {
	return id ^ g.salt
}

// Next 返回 8位 Base62
func (g *Generator) Next() string {
	id := g.NextID()
	obf := g.obfuscate(id)
	return Encode(obf)
}

// Decode 将 Base62 转回原始 int64
func (g *Generator) Decode(s string) (int64, error) {
	val, err := Decode(s)
	if err != nil {
		return 0, err
	}
	return g.deobfuscate(int64(val)), nil
}

// Base62 Encode
func Encode(n int64) string {
	if n == 0 {
		return string(alphabet[0])
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{alphabet[n%base]}, buf...)
		n /= base
	}
	return string(buf)
}

// Basebase Decode
func Decode(s string) (int64, error) {
	var n int64
	for i := 0; i < len(s); i++ {
		idx := decodeTable[s[i]]
		if idx < 0 {
			return 0, errors.New("invalid basebase character")
		}
		n = n*base + int64(idx)
	}
	return n, nil
}
