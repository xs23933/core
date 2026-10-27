package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrConfigFileConflict 表示正式配置的密文版本已变化。
var ErrConfigFileConflict = errors.New("configuration file revision conflict")

// ErrConfigFileUnavailable 表示配置无法安全读取或持久化，不包含路径、密钥或明文。
var ErrConfigFileUnavailable = errors.New("configuration file unavailable")

const maxEncryptedConfigBytes = 8 << 20

var currentConfigFile atomic.Value

// CurrentConfigFile 返回最近一次成功加载配置时记录的绝对路径，尚未成功加载时返回空串。
func CurrentConfigFile() string {
	path, _ := currentConfigFile.Load().(string)
	return path
}

func recordCurrentConfigFile(path string) {
	absolute, err := filepath.Abs(path)
	if err == nil {
		currentConfigFile.Store(absolute)
	}
}

// EncryptedConfigFile 对部署侧指定的可信 .dat 文件提供跨进程的原子版本交换。
// 配置目录必须可写；预备文件和永久锁文件只保存密文或锁状态。
type EncryptedConfigFile struct{ path string }

// NewEncryptedConfigFile 校验配置、密钥与目录，拒绝链接和会在启动时覆盖配置的模板。
func NewEncryptedConfigFile(path string) (*EncryptedConfigFile, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || path == "" || !strings.HasSuffix(absolute, ".dat") {
		return nil, ErrConfigFileUnavailable
	}
	store := &EncryptedConfigFile{path: absolute}
	err = store.locked(func() error { _, _, e := store.snapshot(); return e })
	if err != nil {
		return nil, err
	}
	return store, nil
}

// Snapshot 读取未经环境变量展开、路径替换的原始配置及密文 SHA-256 版本。
func (s *EncryptedConfigFile) Snapshot() (Options, string, error) {
	var conf Options
	var revision string
	err := s.locked(func() error { var err error; conf, revision, err = s.snapshot(); return err })
	return conf, revision, err
}

// Prepare 校验期望版本后，将下一版仅以密文写入持久预备文件，不改变正式配置。
func (s *EncryptedConfigFile) Prepare(expectedRevision string, next Options) (string, error) {
	if !validConfigRevision(expectedRevision) || next == nil {
		return "", ErrConfigFileUnavailable
	}
	var revision string
	err := s.locked(func() error {
		_, current, err := s.snapshot()
		if err != nil {
			return err
		}
		if current != expectedRevision {
			return ErrConfigFileConflict
		}
		plaintext, err := marshalConfigOptions(next)
		if err != nil || len(plaintext) > maxEncryptedConfigBytes/2 {
			return ErrConfigFileUnavailable
		}
		if _, err = parseConfigOptions(plaintext); err != nil {
			return err
		}
		ciphertext, err := encryptConfigBytes(plaintext)
		if err != nil || len(ciphertext) > maxEncryptedConfigBytes {
			return ErrConfigFileUnavailable
		}
		revision = configRevision(ciphertext)
		prepared := s.preparedPath(revision)
		file, err := os.OpenFile(prepared, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if errors.Is(err, os.ErrExist) {
			existing, e := readConfigRegular(prepared)
			if e != nil || configRevision(existing) != revision {
				return ErrConfigFileUnavailable
			}
			return syncConfigDirectory(filepath.Dir(s.path))
		}
		if err != nil {
			return ErrConfigFileUnavailable
		}
		ok := false
		defer func() {
			if !ok {
				os.Remove(prepared)
			}
		}()
		if err = file.Chmod(0600); err == nil {
			_, err = file.Write(ciphertext)
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return ErrConfigFileUnavailable
		}
		if err = syncConfigDirectory(filepath.Dir(s.path)); err != nil {
			return err
		}
		ok = true
		return nil
	})
	if err != nil {
		return "", err
	}
	return revision, nil
}

// Commit 仅在版本匹配时原子替换正式配置，重复提交已生效版本返回成功。
func (s *EncryptedConfigFile) Commit(expectedRevision, preparedRevision string) error {
	if !validConfigRevision(expectedRevision) || !validConfigRevision(preparedRevision) {
		return ErrConfigFileUnavailable
	}
	return s.locked(func() error {
		_, current, err := s.snapshot()
		if err != nil {
			return err
		}
		if current == preparedRevision {
			return syncConfigDirectory(filepath.Dir(s.path))
		}
		if current != expectedRevision {
			return ErrConfigFileConflict
		}
		prepared := s.preparedPath(preparedRevision)
		ciphertext, err := readConfigRegular(prepared)
		if err != nil || configRevision(ciphertext) != preparedRevision {
			return ErrConfigFileUnavailable
		}
		if _, err = decodeConfigOptions(ciphertext); err != nil {
			return err
		}
		// 在替换前再次核对正式版本，避免接受不配合锁的外部旧值。
		_, current, err = s.snapshot()
		if err != nil {
			return err
		}
		if current != expectedRevision {
			return ErrConfigFileConflict
		}
		file, err := os.OpenFile(prepared, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return ErrConfigFileUnavailable
		}
		err = file.Chmod(0600)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return ErrConfigFileUnavailable
		}
		if err = os.Rename(prepared, s.path); err != nil {
			return ErrConfigFileUnavailable
		}
		return syncConfigDirectory(filepath.Dir(s.path))
	})
}

// Discard 删除尚未使用的预备密文；文件已不存在时返回成功。
func (s *EncryptedConfigFile) Discard(preparedRevision string) error {
	if !validConfigRevision(preparedRevision) {
		return ErrConfigFileUnavailable
	}
	return s.locked(func() error {
		path := s.preparedPath(preparedRevision)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		data, err := readConfigRegular(path)
		if err != nil || configRevision(data) != preparedRevision {
			return ErrConfigFileUnavailable
		}
		if err = os.Remove(path); err != nil {
			return ErrConfigFileUnavailable
		}
		return syncConfigDirectory(filepath.Dir(s.path))
	})
}

func (s *EncryptedConfigFile) preparedPath(revision string) string {
	return s.path + ".prepared-" + revision
}

func (s *EncryptedConfigFile) snapshot() (Options, string, error) {
	ciphertext, err := readConfigRegular(s.path)
	if err != nil {
		return nil, "", err
	}
	conf, err := decodeConfigOptions(ciphertext)
	if err != nil {
		return nil, "", err
	}
	return conf, configRevision(ciphertext), nil
}

func (s *EncryptedConfigFile) locked(action func() error) error {
	if s == nil || s.path == "" {
		return ErrConfigFileUnavailable
	}
	if err := checkConfigPath(s.path); err != nil {
		return err
	}
	lockPath := s.path + ".lock"
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return ErrConfigFileUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ErrConfigFileUnavailable
	}
	if err = file.Chmod(0600); err != nil {
		return ErrConfigFileUnavailable
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !(errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR)) || time.Now().After(deadline) {
			return ErrConfigFileUnavailable
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	current, err := os.Lstat(lockPath)
	if err != nil || !os.SameFile(info, current) {
		return ErrConfigFileUnavailable
	}
	if err = checkConfigPath(s.path); err != nil {
		return err
	}
	if err = checkConfigKeyFile(); err != nil {
		return err
	}
	return action()
}

func checkConfigPath(path string) error {
	for candidate := path; ; candidate = filepath.Dir(candidate) {
		info, err := os.Lstat(candidate)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrConfigFileUnavailable
		}
		if candidate == path && !info.Mode().IsRegular() {
			return ErrConfigFileUnavailable
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
	}
	template := strings.TrimSuffix(path, ".dat") + ".default.yaml"
	if _, err := os.Lstat(template); !errors.Is(err, os.ErrNotExist) {
		return ErrConfigFileUnavailable
	}
	return nil
}

func checkConfigKeyFile() error {
	path, configured := os.LookupEnv("CORE_CONFIG_KEY_FILE")
	if !configured {
		return nil
	}
	if path == "" {
		return ErrConfigFileUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ErrConfigFileUnavailable
	}
	return nil
}

func readConfigRegular(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrConfigFileUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxEncryptedConfigBytes {
		return nil, ErrConfigFileUnavailable
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return nil, ErrConfigFileUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, maxEncryptedConfigBytes+1))
	if err != nil || len(data) > maxEncryptedConfigBytes {
		return nil, ErrConfigFileUnavailable
	}
	return data, nil
}

func decodeConfigOptions(ciphertext []byte) (Options, error) {
	plaintext, err := decryptConfigBytes(ciphertext)
	if err != nil {
		return nil, ErrConfigFileUnavailable
	}
	return parseConfigOptions(plaintext)
}

func parseConfigOptions(plaintext []byte) (Options, error) {
	var conf Options
	decoder := yaml.NewDecoder(bytes.NewReader(plaintext))
	if err := decoder.Decode(&conf); err != nil || conf == nil {
		return nil, ErrConfigFileUnavailable
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrConfigFileUnavailable
	}
	return conf, nil
}

func marshalConfigOptions(conf Options) (data []byte, err error) {
	defer func() {
		if recover() != nil {
			data = nil
			err = ErrConfigFileUnavailable
		}
	}()
	return yaml.Marshal(conf)
}

func configRevision(ciphertext []byte) string {
	sum := sha256.Sum256(ciphertext)
	return hex.EncodeToString(sum[:])
}
func validConfigRevision(revision string) bool {
	if len(revision) != 64 {
		return false
	}
	for _, c := range revision {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func syncConfigDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return ErrConfigFileUnavailable
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil || closeErr != nil {
		return ErrConfigFileUnavailable
	}
	return nil
}
