package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xs23933/core/v3/sid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type paginationItem struct {
	ID   int
	Name string
}

type cursorPaginationItem struct {
	ID        int
	CreatedAt time.Time
	Name      string
}

type cursorSIDPaginationItem struct {
	ID        sid.ID
	CreatedAt time.Time
}

func TestFindNextByTrimsProbeRow(t *testing.T) {
	db := newPaginationDB(t)

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

func TestFindsDefaultsToPageMode(t *testing.T) {
	db := newPaginationDB(t)
	whr := &Map{"p": 1, "l": 2, "asc": "id"}

	page, err := Finds[paginationItem](FindsParams{
		Where: whr,
		DB:    db,
	})
	if err != nil {
		t.Fatal(err)
	}

	if page.Total == nil || *page.Total != 3 {
		t.Fatalf("Total = %v, want 3", page.Total)
	}
	if page.Next != nil || page.Prev != nil {
		t.Fatalf("next pagination flags = %v/%v, want nil", page.Next, page.Prev)
	}
	if len(page.Data) != 2 {
		t.Fatalf("data len = %d, want 2: %#v", len(page.Data), page.Data)
	}
	if !whr.Contains("p") || !whr.Contains("l") || !whr.Contains("asc") {
		t.Fatalf("Finds mutated original where map: %#v", *whr)
	}
}

func TestFindsNextModeTrimsProbeRow(t *testing.T) {
	db := newPaginationDB(t)

	page, err := Finds[paginationItem](FindsParams{
		Where: &Map{"p": 1, "l": 2, "asc": "id"},
		DB:    db,
		Mode:  FindsModeNext,
	})
	if err != nil {
		t.Fatal(err)
	}

	if page.Total != nil {
		t.Fatalf("Total = %v, want nil", page.Total)
	}
	if !page.HasNext() {
		t.Fatalf("HasNext() = false, want true")
	}
	if page.Prev == nil || *page.Prev {
		t.Fatalf("Prev = %v, want false", page.Prev)
	}
	if len(page.Data) != 2 {
		t.Fatalf("data len = %d, want 2: %#v", len(page.Data), page.Data)
	}
	if page.Data[0].ID != 1 || page.Data[1].ID != 2 {
		t.Fatalf("data IDs = %#v, want first two rows", page.Data)
	}
}

func TestFindsCursorMovesForwardAndBackwardWithTies(t *testing.T) {
	db := newCursorPaginationDB(t)
	spec := &CursorSpec{Fields: []string{"created_at", "id"}, Desc: true}

	first, err := Finds[cursorPaginationItem](FindsParams{
		Where:  &Map{"l": 2},
		DB:     db,
		Mode:   FindsModeCursor,
		Cursor: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCursorIDs(t, first.Data, 5, 4)
	assertCursorFlags(t, first, true, false)
	if first.NextCursor == "" || first.PrevCursor != "" {
		t.Fatalf("first cursors = next %q prev %q", first.NextCursor, first.PrevCursor)
	}

	secondSpec := *spec
	secondSpec.Token = first.NextCursor
	second, err := Finds[cursorPaginationItem](FindsParams{
		Where:  &Map{"l": 2},
		DB:     db,
		Mode:   FindsModeCursor,
		Cursor: &secondSpec,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCursorIDs(t, second.Data, 3, 2)
	assertCursorFlags(t, second, true, true)

	previousSpec := *spec
	previousSpec.Token = second.PrevCursor
	previousSpec.Direction = CursorDirectionPrev
	previous, err := Finds[cursorPaginationItem](FindsParams{
		Where:  &Map{"l": 2},
		DB:     db,
		Mode:   FindsModeCursor,
		Cursor: &previousSpec,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCursorIDs(t, previous.Data, 5, 4)
	assertCursorFlags(t, previous, true, false)
}

func TestFindsCursorIsStableWhenNewRowsAreInserted(t *testing.T) {
	db := newCursorPaginationDB(t)
	spec := &CursorSpec{Fields: []string{"created_at", "id"}, Desc: true}

	first, err := Finds[cursorPaginationItem](FindsParams{
		Where: &Map{"l": 2}, DB: db, Mode: FindsModeCursor, Cursor: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCursorIDs(t, first.Data, 5, 4)

	if err := db.Create(&cursorPaginationItem{
		ID: 6, CreatedAt: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC), Name: "new",
	}).Error; err != nil {
		t.Fatal(err)
	}

	nextSpec := *spec
	nextSpec.Token = first.NextCursor
	next, err := Finds[cursorPaginationItem](FindsParams{
		Where: &Map{"l": 2}, DB: db, Mode: FindsModeCursor, Cursor: &nextSpec,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCursorIDs(t, next.Data, 3, 2)
}

func TestFindsCursorClearsInheritedOffsetWithoutCount(t *testing.T) {
	var statements bytes.Buffer
	db := newCursorPaginationDB(t).Session(&gorm.Session{
		Logger: gormlogger.New(log.New(&statements, "", 0), gormlogger.Config{LogLevel: gormlogger.Info}),
	})
	result, err := Finds[cursorPaginationItem](FindsParams{
		Where: &Map{"l": 2}, DB: db.Offset(3), Mode: FindsModeCursor,
		Cursor: &CursorSpec{Fields: []string{"created_at", "id"}, Desc: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCursorIDs(t, result.Data, 5, 4)

	queries := strings.ToUpper(statements.String())
	if strings.Contains(queries, " OFFSET ") {
		t.Fatalf("cursor query contains OFFSET: %s", statements.String())
	}
	if strings.Contains(queries, "COUNT(") {
		t.Fatalf("cursor query contains COUNT: %s", statements.String())
	}
}

func TestFindsCursorSupportsQualifiedSingleIDAscending(t *testing.T) {
	db := newCursorPaginationDB(t)
	spec := &CursorSpec{
		Fields: []string{"cursor_pagination_items.id"},
		Desc:   false,
	}

	first, err := Finds[cursorPaginationItem](FindsParams{
		Where: &Map{"l": 1}, DB: db, Mode: FindsModeCursor, Cursor: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCursorIDs(t, first.Data, 1)

	nextSpec := *spec
	nextSpec.Token = first.NextCursor
	next, err := Finds[cursorPaginationItem](FindsParams{
		Where: &Map{"l": 1}, DB: db, Mode: FindsModeCursor, Cursor: &nextSpec,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCursorIDs(t, next.Data, 2)
}

func TestFindsCursorPreservesSIDValues(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cursorSIDPaginationItem{}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []cursorSIDPaginationItem{
		{ID: sid.ID(9007199254740993), CreatedAt: base},
		{ID: sid.ID(9007199254740994), CreatedAt: base},
		{ID: sid.ID(9007199254740995), CreatedAt: base.Add(time.Second)},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	spec := &CursorSpec{Fields: []string{"created_at", "id"}, Desc: true}
	first, err := Finds[cursorSIDPaginationItem](FindsParams{
		Where: &Map{"l": 2}, DB: db, Mode: FindsModeCursor, Cursor: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Data) != 2 || first.Data[0].ID != items[2].ID || first.Data[1].ID != items[1].ID {
		t.Fatalf("first SID page = %#v", first.Data)
	}

	nextSpec := *spec
	nextSpec.Token = first.NextCursor
	next, err := Finds[cursorSIDPaginationItem](FindsParams{
		Where: &Map{"l": 2}, DB: db, Mode: FindsModeCursor, Cursor: &nextSpec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Data) != 1 || next.Data[0].ID != items[0].ID {
		t.Fatalf("second SID page = %#v", next.Data)
	}
}

func TestCursorTokenRejectsMalformedOrMismatchedSpecifications(t *testing.T) {
	token, err := EncodeCursorToken([]string{"created_at", "id"}, true,
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), int64(9007199254740993))
	if err != nil {
		t.Fatal(err)
	}
	values, err := DecodeCursorToken(CursorSpec{
		Token: token, Fields: []string{"created_at", "id"}, Desc: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if number, ok := values[1].(json.Number); !ok || number.String() != "9007199254740993" {
		t.Fatalf("decoded ID = %#v, want exact json.Number", values[1])
	}

	tests := []CursorSpec{
		{Token: "not-base64", Fields: []string{"id"}},
		{Token: token, Fields: []string{"created_at", "other_id"}, Desc: true},
		{Token: token, Fields: []string{"created_at", "id"}, Desc: false},
		{Token: token, Fields: []string{"created_at desc", "id"}, Desc: true},
	}
	for _, spec := range tests {
		if _, err := DecodeCursorToken(spec); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("DecodeCursorToken(%#v) error = %v, want ErrInvalidCursor", spec, err)
		}
	}
}

func newPaginationDB(t *testing.T) *gorm.DB {
	t.Helper()

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
	return db
}

func newCursorPaginationDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cursorPaginationItem{}); err != nil {
		t.Fatal(err)
	}
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&[]cursorPaginationItem{
		{ID: 1, CreatedAt: t1, Name: "one"},
		{ID: 2, CreatedAt: t1, Name: "two"},
		{ID: 3, CreatedAt: t2, Name: "three"},
		{ID: 4, CreatedAt: t2, Name: "four"},
		{ID: 5, CreatedAt: t3, Name: "five"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func assertCursorIDs(t *testing.T, data []cursorPaginationItem, want ...int) {
	t.Helper()
	if len(data) != len(want) {
		t.Fatalf("data len = %d, want %d: %#v", len(data), len(want), data)
	}
	for i, id := range want {
		if data[i].ID != id {
			t.Fatalf("data[%d].ID = %d, want %d: %#v", i, data[i].ID, id, data)
		}
	}
}

func assertCursorFlags(t *testing.T, page FindsResult[cursorPaginationItem], next, prev bool) {
	t.Helper()
	if page.CursorHasNext == nil || *page.CursorHasNext != next {
		t.Fatalf("CursorHasNext = %v, want %v", page.CursorHasNext, next)
	}
	if page.CursorHasPrev == nil || *page.CursorHasPrev != prev {
		t.Fatalf("CursorHasPrev = %v, want %v", page.CursorHasPrev, prev)
	}
	if page.HasNext() != next || page.HasPrev() != prev {
		t.Fatalf("HasNext/HasPrev = %v/%v, want %v/%v", page.HasNext(), page.HasPrev(), next, prev)
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
