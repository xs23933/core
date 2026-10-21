package core

import (
	"errors"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// configEncryptionKey 仅读取配置加密密钥，不修改业务 AESKey。
func configEncryptionKey() ([]byte, error) {
	path, configured := os.LookupEnv("CORE_CONFIG_KEY_FILE")
	if !configured {
		return AESKey, nil
	}
	if path == "" {
		return nil, errors.New("configuration key file is empty")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("configuration key file must be readable and regular")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("configuration key file cannot be opened")
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("configuration key file must be regular")
	}
	key, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil || len(key) != 32 {
		return nil, errors.New("configuration key file must contain exactly 32 bytes")
	}
	return key, nil
}

func encryptConfigBytes(plaintext []byte) ([]byte, error) {
	key, err := configEncryptionKey()
	if err != nil {
		return nil, err
	}
	return encryptAESBytesWithKey(plaintext, key)
}

func decryptConfigBytes(ciphertext []byte) ([]byte, error) {
	key, err := configEncryptionKey()
	if err != nil {
		return nil, err
	}
	return decryptAESBytesWithKey(ciphertext, key)
}

// configDatFileMode 为显式密钥配置创建更严格的配置文件，不影响旧流程。
func configDatFileMode() os.FileMode {
	if _, configured := os.LookupEnv("CORE_CONFIG_KEY_FILE"); configured {
		return 0600
	}
	return 0644
}

// loadKeyedEncryptedConfig 显式配置密钥后拒绝损坏配置及默认值回退。
func loadKeyedEncryptedConfig(datFile string) (Options, error) {
	key, err := configEncryptionKey()
	if err != nil {
		return nil, err
	}
	yamlFile := strings.TrimSuffix(datFile, ".dat") + ".default.yaml"
	plaintext, err := os.ReadFile(yamlFile)
	if err == nil {
		var conf Options
		if yaml.Unmarshal(plaintext, &conf) != nil || conf == nil {
			return nil, errors.New("configuration YAML is invalid")
		}
		encrypted, err := encryptAESBytesWithKey(plaintext, key)
		if err != nil {
			return nil, errors.New("configuration encryption failed")
		}
		if os.WriteFile(datFile, encrypted, configDatFileMode()) != nil {
			return nil, errors.New("encrypted configuration cannot be written")
		}
		return conf, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("configuration YAML cannot be read")
	}
	encrypted, err := os.ReadFile(datFile)
	if err != nil {
		return nil, errors.New("encrypted configuration cannot be read")
	}
	plaintext, err = decryptAESBytesWithKey(encrypted, key)
	if err != nil {
		return nil, errors.New("configuration decryption failed")
	}
	var conf Options
	if yaml.Unmarshal(plaintext, &conf) != nil || conf == nil {
		return nil, errors.New("configuration YAML is invalid")
	}
	return conf, nil
}
