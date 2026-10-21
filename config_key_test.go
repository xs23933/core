package core

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestConfigKeyFileGenerationAndLoad(t *testing.T) {
	previous := confFile
	t.Cleanup(func() { confFile = previous })
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	key := bytes.Repeat([]byte{0x81}, 32)
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORE_CONFIG_KEY_FILE", keyPath)
	yamlPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(yamlPath, []byte("answer: 42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "--generate", "-f", yamlPath)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate: %s %v", out, err)
	}
	ciphertext, err := os.ReadFile(filepath.Join(dir, "config.dat"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "config.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("new keyed configuration must use mode 0600")
	}
	// 业务 AES 使用独立密钥；配置密文不能再由业务全局密钥读取。
	if _, err := DecryptAESBytes(ciphertext); err == nil {
		t.Fatal("configuration still encrypted with global AESKey")
	}
	before := append([]byte(nil), AESKey...)
	conf := LoadConfigFile(filepath.Join(dir, "config.dat"))
	if got := conf.GetInt("answer"); got != 42 {
		t.Fatalf("answer=%d", got)
	}
	if !bytes.Equal(before, AESKey) {
		t.Fatal("configuration changed business AESKey")
	}
	if err := SaveConfigFile(map[string]any{"answer": 43}); err != nil {
		t.Fatal(err)
	}
	conf = LoadConfigFile(filepath.Join(dir, "config.dat"))
	if conf.GetInt("answer") != 43 {
		t.Fatal("SaveConfigFile did not reuse config key")
	}
}

func TestConfigKeyFileFailuresNeverReplaceEncryptedFile(t *testing.T) {
	for _, kind := range []string{"missing", "empty-env", "short", "long", "directory", "wrong", "corrupt", "invalid-yaml"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			keyPath := filepath.Join(dir, "key")
			dat := filepath.Join(dir, "config.dat")
			key := bytes.Repeat([]byte{0x83}, 32)
			if err := os.WriteFile(keyPath, key, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CORE_CONFIG_KEY_FILE", keyPath)
			plaintext := []byte("answer: 42\n")
			if kind == "invalid-yaml" {
				plaintext = []byte("invalid: [")
			}
			ciphertext, err := encryptAESBytesWithKey(plaintext, key)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "corrupt" {
				ciphertext = []byte("broken-ciphertext")
			}
			if err = os.WriteFile(dat, ciphertext, 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				os.Remove(keyPath)
			case "empty-env":
				t.Setenv("CORE_CONFIG_KEY_FILE", "")
			case "short":
				os.WriteFile(keyPath, []byte("short"), 0600)
			case "long":
				os.WriteFile(keyPath, bytes.Repeat([]byte{0x83}, 33), 0600)
			case "directory":
				t.Setenv("CORE_CONFIG_KEY_FILE", dir)
			case "wrong":
				os.WriteFile(keyPath, bytes.Repeat([]byte{0x84}, 32), 0600)
			}
			var recovered any
			func() { defer func() { recovered = recover() }(); LoadConfigFile(dat) }()
			if recovered == nil {
				t.Fatal("invalid configured encryption did not fail closed")
			}
			after, err := os.ReadFile(dat)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(ciphertext, after) {
				t.Fatal("failed load replaced encrypted configuration")
			}
			if _, err = os.Stat(filepath.Join(dir, "config.default.yaml")); !os.IsNotExist(err) {
				t.Fatal("failed load created plaintext fallback")
			}
		})
	}
}

func TestConfigKeyFileLegacyCompatibility(t *testing.T) {
	t.Setenv("CORE_CONFIG_KEY_FILE", "")
	os.Unsetenv("CORE_CONFIG_KEY_FILE")
	data, err := encryptConfigBytes([]byte("legacy"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := DecryptAESBytes(data)
	if err != nil || string(plain) != "legacy" {
		t.Fatal("legacy config encryption changed")
	}
}

func TestConfigKeyDefaultYAMLAndDatGeneration(t *testing.T) {
	previous := confFile
	t.Cleanup(func() { confFile = previous })
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	dat := filepath.Join(dir, "config.dat")
	key := bytes.Repeat([]byte{0x92}, 32)
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORE_CONFIG_KEY_FILE", keyPath)
	yamlPath := filepath.Join(dir, "config.default.yaml")
	if err := os.WriteFile(yamlPath, []byte("answer: 51\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(executable, "--generate", "-f", dat).CombinedOutput(); err != nil {
		t.Fatalf("generate dat: %v %s", err, out)
	}
	encrypted, err := os.ReadFile(dat)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := decryptAESBytesWithKey(encrypted, key); err != nil || string(plain) != "answer: 51\n" {
		t.Fatal("dat generator did not use key file")
	}
	if err := os.WriteFile(yamlPath, []byte("answer: 52\n"), 0600); err != nil {
		t.Fatal(err)
	}
	conf := LoadConfigFile(dat)
	if conf.GetInt("answer") != 52 {
		t.Fatal("default YAML update failed")
	}
	if err := os.Remove(yamlPath); err != nil {
		t.Fatal(err)
	}
	conf = LoadConfigFile(dat)
	if conf.GetInt("answer") != 52 {
		t.Fatal("updated dat did not use same key")
	}
	t.Setenv("CORE_CONFIG_KEY_FILE", filepath.Join(dir, "missing-key"))
	before, err := os.ReadFile(dat)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(executable, "--generate", "-f", dat).CombinedOutput(); err == nil {
		t.Fatalf("generation accepted missing key: %s", out)
	}
	after, err := os.ReadFile(dat)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed generation overwrote dat")
	}
}
