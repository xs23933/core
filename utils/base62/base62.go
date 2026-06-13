package base62

import (
	"errors"
	"math/big"
	"strings"
)

const (
	// Base62 字符集：0-9 (0-9), A-Z (10-35), a-z (36-61)
	charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	base    = 62
)

// Encode 将 uint64 数字编码为 Base62 字符串
func Encode(num uint64) string {
	if num == 0 {
		return string(charset[0])
	}

	var result strings.Builder
	n := num

	for n > 0 {
		remainder := n % base
		result.WriteByte(charset[remainder])
		n = n / base
	}

	// 反转字符串（因为计算是从低位到高位）
	return reverseString(result.String())
}

// EncodeInt 将 int64 数字编码为 Base62 字符串
func EncodeInt(num int64) (string, error) {
	if num < 0 {
		return "", errors.New("negative number not supported")
	}
	return Encode(uint64(num)), nil
}

// Decode 将 Base62 字符串解码为 uint64 数字
func Decode(s string) (uint64, error) {
	var result uint64 = 0

	for i := 0; i < len(s); i++ {
		char := s[i]
		var value uint64

		// 查找字符在字符集中的位置
		switch {
		case char >= '0' && char <= '9':
			value = uint64(char - '0')
		case char >= 'A' && char <= 'Z':
			value = uint64(char-'A') + 10
		case char >= 'a' && char <= 'z':
			value = uint64(char-'a') + 36
		default:
			return 0, errors.New("invalid character in base62 string")
		}

		// 检查溢出
		if result > (^uint64(0)-value)/base {
			return 0, errors.New("overflow during decoding")
		}

		result = result*base + value
	}

	return result, nil
}

// MustDecode 解码 Base62 字符串，如果出错则 panic
func MustDecode(s string) uint64 {
	num, err := Decode(s)
	if err != nil {
		panic(err)
	}
	return num
}

// reverseString 反转字符串
func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// EncodeString 将字符串编码为 Base62 字符串
func EncodeString(s string) string {
	// 将字符串转为 big.Int（把字符串当作字节数组）
	bytes := []byte(s)
	num := new(big.Int).SetBytes(bytes)

	if num.Cmp(big.NewInt(0)) == 0 {
		return string(charset[0])
	}

	zero := big.NewInt(0)
	baseBig := big.NewInt(base)

	var result strings.Builder

	// 临时变量
	temp := new(big.Int).Set(num)

	for temp.Cmp(zero) > 0 {
		remainder := new(big.Int)
		temp.DivMod(temp, baseBig, remainder)
		result.WriteByte(charset[remainder.Int64()])
	}

	return reverseString(result.String())
}

// DecodeString 将 Base62 字符串解码为原始字符串
func DecodeString(s string) (string, error) {
	// 将 Base62 转为 big.Int
	num := big.NewInt(0)

	for i := 0; i < len(s); i++ {
		char := s[i]
		var value int64

		switch {
		case char >= '0' && char <= '9':
			value = int64(char - '0')
		case char >= 'A' && char <= 'Z':
			value = int64(char-'A') + 10
		case char >= 'a' && char <= 'z':
			value = int64(char-'a') + 36
		default:
			return "", errors.New("invalid character in base62 string")
		}

		num.Mul(num, big.NewInt(base))
		num.Add(num, big.NewInt(value))
	}

	// 将 big.Int 转回字符串
	bytes := num.Bytes()
	return string(bytes), nil
}
