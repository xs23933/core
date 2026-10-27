package core

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func encryptedStoreFixture(t *testing.T) (string, Options) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORE_CONFIG_KEY_FILE", key)
	path := filepath.Join(dir, "config.dat")
	raw := []byte("custom:\n  token: '${ENV:SECRET:unchanged}'\n  list: [one, two]\nlisten: 8080\n")
	encrypted, err := encryptConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, encrypted, 0600); err != nil {
		t.Fatal(err)
	}
	return path, Options{"listen": 8080, "custom": Options{"token": "${ENV:SECRET:unchanged}", "list": []any{"one", "two"}}}
}

func TestEncryptedConfigFileRoundTripAndRecovery(t *testing.T) {
	path, want := encryptedStoreFixture(t)
	store, err := NewEncryptedConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, rev, err := store.Snapshot()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot %v %v", got, err)
	}
	got["listen"] = 9090
	prepared, err := store.Prepare(rev, got)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewEncryptedConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Commit(rev, prepared); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Commit(rev, prepared); err != nil {
		t.Fatal(err)
	}
	actual, newRev, err := store.Snapshot()
	if err != nil || newRev != prepared || !reflect.DeepEqual(actual, got) {
		t.Fatalf("result %v %v", actual, err)
	}
	if _, err = store.Prepare(rev, want); !errors.Is(err, ErrConfigFileConflict) {
		t.Fatalf("CAS %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", info, err)
	}
}

func TestEncryptedConfigFileRejectUnsafeInputs(t *testing.T) {
	for _, scenario := range []string{"cipher", "key", "missing-key", "template", "symlink", "parent-symlink", "directory", "invalid-yaml"} {
		t.Run(scenario, func(t *testing.T) {
			path, _ := encryptedStoreFixture(t)
			switch scenario {
			case "cipher":
				if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "key":
				t.Setenv("CORE_CONFIG_KEY_FILE", filepath.Join(t.TempDir(), "absent"))
			case "missing-key":
				t.Setenv("CORE_CONFIG_KEY_FILE", "")
			case "template":
				if err := os.WriteFile(strings.TrimSuffix(path, ".dat")+".default.yaml", []byte("x: y"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				alias := filepath.Join(t.TempDir(), "alias.dat")
				if err := os.Symlink(path, alias); err != nil {
					t.Fatal(err)
				}
				path = alias
			case "parent-symlink":
				alias := filepath.Join(t.TempDir(), "dir")
				if err := os.Symlink(filepath.Dir(path), alias); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(alias, "config.dat")
			case "directory":
				path = t.TempDir() + ".dat"
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "invalid-yaml":
				data, err := encryptConfigBytes([]byte("[bad"))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := NewEncryptedConfigFile(path); !errors.Is(err, ErrConfigFileUnavailable) {
				t.Fatalf("expected unavailable: %v", err)
			}
		})
	}
}

func TestEncryptedConfigFileConcurrentCommitAndDiscard(t *testing.T) {
	path, _ := encryptedStoreFixture(t)
	store, err := NewEncryptedConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conf, rev, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	conf["listen"] = 9001
	a, err := store.Prepare(rev, conf)
	if err != nil {
		t.Fatal(err)
	}
	conf["listen"] = 9002
	b, err := store.Prepare(rev, conf)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, p := range []string{a, b} {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			other, e := NewEncryptedConfigFile(path)
			if e == nil {
				e = other.Commit(rev, p)
			}
			results <- e
		}(p)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConfigFileConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	for _, p := range []string{a, b} {
		if err = store.Discard(p); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Commit(rev, "../unsafe"); !errors.Is(err, ErrConfigFileUnavailable) {
		t.Fatal(err)
	}
}

func TestCurrentConfigFileAbsoluteAndRaw(t *testing.T) {
	path, _ := encryptedStoreFixture(t)
	LoadConfigFile(path)
	if got := CurrentConfigFile(); got != path {
		t.Fatalf("current %q", got)
	}
}

func TestEncryptedConfigFileRejectTamperedPreparedAndBoundedInput(t *testing.T) {
	path, _ := encryptedStoreFixture(t)
	store, err := NewEncryptedConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conf, revision, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(revision, conf)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(store.preparedPath(prepared), []byte("not encrypted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = store.Commit(revision, prepared); !errors.Is(err, ErrConfigFileUnavailable) {
		t.Fatal(err)
	}
	if _, actual, err := store.Snapshot(); err != nil || actual != revision {
		t.Fatalf("original changed %s %v", actual, err)
	}
	if _, err = store.Prepare(strings.Repeat("A", 64), conf); !errors.Is(err, ErrConfigFileUnavailable) {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = file.Truncate(maxEncryptedConfigBytes + 1)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	if _, _, err = store.Snapshot(); !errors.Is(err, ErrConfigFileUnavailable) {
		t.Fatal(err)
	}
}

func TestEncryptedConfigFileRejectWrongKeyAndMultipleDocuments(t *testing.T) {
	for _, scenario := range []string{"wrong-key", "short-key", "key-symlink", "multiple-documents"} {
		t.Run(scenario, func(t *testing.T) {
			path, _ := encryptedStoreFixture(t)
			key := os.Getenv("CORE_CONFIG_KEY_FILE")
			switch scenario {
			case "wrong-key":
				if err := os.WriteFile(key, []byte(strings.Repeat("z", 32)), 0600); err != nil {
					t.Fatal(err)
				}
			case "short-key":
				if err := os.WriteFile(key, []byte("short"), 0600); err != nil {
					t.Fatal(err)
				}
			case "key-symlink":
				alias := filepath.Join(filepath.Dir(key), "key-link")
				if err := os.Symlink(key, alias); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CORE_CONFIG_KEY_FILE", alias)
			case "multiple-documents":
				ciphertext, err := encryptConfigBytes([]byte("first: 1\n---\nsecret: value\n"))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, ciphertext, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := NewEncryptedConfigFile(path); !errors.Is(err, ErrConfigFileUnavailable) {
				t.Fatal(err)
			}
		})
	}
}

func TestEncryptedConfigFileCrossProcessCommit(t *testing.T) {
	if os.Getenv("CORE_CONFIG_STORE_TEST_CHILD") == "1" {
		store, err := NewEncryptedConfigFile(os.Getenv("CORE_CONFIG_STORE_TEST_PATH"))
		if err == nil {
			err = store.Commit(os.Getenv("CORE_CONFIG_STORE_TEST_EXPECTED"), os.Getenv("CORE_CONFIG_STORE_TEST_PREPARED"))
		}
		if errors.Is(err, ErrConfigFileConflict) {
			os.Exit(23)
		}
		if err != nil {
			os.Exit(24)
		}
		os.Exit(0)
	}
	path, _ := encryptedStoreFixture(t)
	store, err := NewEncryptedConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conf, revision, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var commands []*exec.Cmd
	for i := 0; i < 2; i++ {
		conf["listen"] = 9900 + i
		prepared, err := store.Prepare(revision, conf)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(os.Args[0], "-test.run=^TestEncryptedConfigFileCrossProcessCommit$")
		command.Env = append(os.Environ(), "CORE_CONFIG_STORE_TEST_CHILD=1", "CORE_CONFIG_STORE_TEST_PATH="+path, "CORE_CONFIG_STORE_TEST_EXPECTED="+revision, "CORE_CONFIG_STORE_TEST_PREPARED="+prepared)
		if err = command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	success, conflict := 0, 0
	for _, command := range commands {
		err := command.Wait()
		if err == nil {
			success++
			continue
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 23 {
			conflict++
			continue
		}
		t.Fatalf("child failed: %v", err)
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}
