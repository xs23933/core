package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type paginationItem struct {
	ID   int
	Name string
}

func TestFindNextByTrimsProbeRow(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&paginationItem{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]paginationItem{
		{ID: 1, Name: "one"},
		{ID: 2, Name: "two"},
		{ID: 3, Name: "three"},
	}).Error; err != nil {
		t.Fatal(err)
	}

	var out []paginationItem
	page, err := FindNextBy[paginationItem](&Map{"p": 1, "l": 2, "asc": "id"}, &out, db)
	if err != nil {
		t.Fatal(err)
	}

	if !page.Next {
		t.Fatalf("Next = false, want true")
	}
	if len(page.Data) != 2 {
		t.Fatalf("data len = %d, want 2: %#v", len(page.Data), page.Data)
	}
	if page.Data[0].ID != 1 || page.Data[1].ID != 2 {
		t.Fatalf("data IDs = %#v, want first two rows", page.Data)
	}
}

func TestOpenDBUsesEnvironmentDSNBeforeConfigDSN(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "env.db")
	configPath := filepath.Join(t.TempDir(), "config.db")
	t.Setenv("CORE_DATABASE_DSN", envPath)

	db, err := openDB(Options{
		"type": "sqlite3",
		"dsn":  configPath,
	}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	if err := db.Exec("CREATE TABLE dsn_probe (id integer)").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf("env DSN database was not created: %v", err)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config DSN database was used, stat err = %v", err)
	}
}

func TestNewModelDoesNotApplyDefaultEnvironmentDSNToNamedDatabases(t *testing.T) {
	resetModelState(t)

	dir := t.TempDir()
	defaultEnvPath := filepath.Join(dir, "default-env.db")
	defaultConfigPath := filepath.Join(dir, "default-config.db")
	logConfigPath := filepath.Join(dir, "log-config.db")
	t.Setenv("CORE_DATABASE_DSN", defaultEnvPath)
	Conf = Options{
		"database": Options{
			"default": Options{"type": "sqlite3", "dsn": defaultConfigPath},
			"log":     Options{"type": "sqlite3", "dsn": logConfigPath},
		},
	}

	dbs, err := NewModel(Conf.GetMap("database"), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbs["default"].Exec("CREATE TABLE default_probe (id integer)").Error; err != nil {
		t.Fatal(err)
	}
	if err := dbs["log"].Exec("CREATE TABLE log_probe (id integer)").Error; err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(defaultEnvPath); err != nil {
		t.Fatalf("default env DSN database was not created: %v", err)
	}
	if _, err := os.Stat(defaultConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default config DSN database was used, stat err = %v", err)
	}
	if _, err := os.Stat(logConfigPath); err != nil {
		t.Fatalf("named database config DSN was not created: %v", err)
	}
}

func TestNewModelUsesNamedEnvironmentDSNForNamedDatabase(t *testing.T) {
	resetModelState(t)

	dir := t.TempDir()
	defaultConfigPath := filepath.Join(dir, "default-config.db")
	logEnvPath := filepath.Join(dir, "log-env.db")
	logConfigPath := filepath.Join(dir, "log-config.db")
	t.Setenv("CORE_DATABASE_LOG_DSN", logEnvPath)
	Conf = Options{
		"database": Options{
			"default": Options{"type": "sqlite3", "dsn": defaultConfigPath},
			"log":     Options{"type": "sqlite3", "dsn": logConfigPath},
		},
	}

	dbs, err := NewModel(Conf.GetMap("database"), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbs["default"].Exec("CREATE TABLE default_probe (id integer)").Error; err != nil {
		t.Fatal(err)
	}
	if err := dbs["log"].Exec("CREATE TABLE log_probe (id integer)").Error; err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(defaultConfigPath); err != nil {
		t.Fatalf("default config DSN database was not created: %v", err)
	}
	if _, err := os.Stat(logEnvPath); err != nil {
		t.Fatalf("named env DSN database was not created: %v", err)
	}
	if _, err := os.Stat(logConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("named config DSN database was used, stat err = %v", err)
	}
}

func resetModelState(t *testing.T) {
	t.Helper()

	prevConf := Conf
	prevConns := conns
	prevDBsType := dbsType
	conns = make(map[string]*DB)
	dbsType = make(map[string]string)

	t.Cleanup(func() {
		for _, db := range conns {
			if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
				_ = sqlDB.Close()
			}
		}
		Conf = prevConf
		conns = prevConns
		dbsType = prevDBsType
	})
}
