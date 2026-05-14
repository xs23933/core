package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"

	"golang.org/x/crypto/bcrypt"
)

// ============================================================
// AES 加密解密
// ============================================================

var (
	// AESKey AES-256 密钥（32字节）
	AESKey = []byte("this-is-a-32-byte-key-for-aes256")
)

// EncryptAES AES-256-GCM 加密
func EncryptAES(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	result, err := EncryptAESBytes([]byte(plaintext))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(result), nil
}

// EncryptAESBytes AES-256-GCM 加密
func EncryptAESBytes(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}

	block, err := aes.NewCipher(AESKey)
	if err != nil {
		return nil, err
	}

	// 使用 GCM 模式（AEAD）
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// 生成随机 nonce（GCM 推荐 12 字节）
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	// Seal 加密并认证
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	return ciphertext, nil
}

// DecryptAES AES-256-GCM 解密
func DecryptAES(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}

	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}

	result, err := DecryptAESBytes(data)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

// DecryptAESBytes AES-256-GCM 解密
func DecryptAESBytes(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, nil
	}

	block, err := aes.NewCipher(AESKey)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertextBytes := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// ============================================================
// 密码哈希
// ============================================================

// HashPassword 使用 bcrypt 哈希密码（使用默认 cost）
func HashPassword(password string) (string, error) {
	return HashPasswordWithCost(password, bcrypt.DefaultCost)
}

// HashPasswordWithCost 使用 bcrypt 哈希密码，可指定 cost
func HashPasswordWithCost(password string, cost int) (string, error) {
	if cost <= 0 {
		cost = bcrypt.DefaultCost
	}
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// CheckPassword 验证密码
func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// ============================================================
// SHA-256 哈希（用于手机号/邮箱索引）
// ============================================================

// SHA256Hash 计算 SHA-256 哈希值（string 版本）
func SHA256Hash(data string) string {
	return SHA256HashBytes([]byte(data))
}

// SHA256HashBytes 计算 SHA-256 哈希值（[]byte 版本）
func SHA256HashBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
