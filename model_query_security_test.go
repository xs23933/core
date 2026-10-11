package core

import (
	"bytes"
	"errors"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestListQueriesRejectUnsafeInputBeforeSQL(t *testing.T) {
	db := newPaginationDB(t)
	inputs := []struct {
		name  string
		where Map
	}{
		{"like", Map{"name LIKE '%' OR name*": "absent"}},
		{"prefix", Map{"^name LIKE '%' OR name": "absent"}},
		{"suffix", Map{"name LIKE '%' OR name$": "absent"}},
		{"in", Map{"id IN (1,2,3) OR id IN": []int{999}}},
		{"notin", Map{"id IN (1,2,3) OR id NOTIN": []int{1, 2, 3}}},
		{"comparison", Map{"id` = 1 OR `id >": 999}},
		{"not-equal", Map{"id` = 1 OR `id !=": 2}},
		{"plain", Map{"id OR 1=1 --": 1}},
		{"quoted-field", Map{"`id`": 1}},
		{"field-newline", Map{"id\nOR 1=1*": "x"}},
		{"field-wildcard", Map{"pagination_items.*": 1}},
		{"field-multiple-qualifiers", Map{"schema.pagination_items.id": 1}},
		{"empty-unsafe", Map{"id; SELECT 1*": ""}},
		{"nil-unsafe", Map{"id; SELECT 1": nil}},
		{"unsupported-operator", Map{"id BETWEEN": []int{1, 3}}},
		{"asc-expression", Map{"asc": "(CASE WHEN name = 'two' THEN 0 ELSE 1 END), id"}},
		{"desc-expression", Map{"desc": "id; SELECT 1 --"}},
		{"sort-comment", Map{"asc": "id /* comment */"}},
		{"sort-direction", Map{"asc": "id DESC"}},
		{"sort-empty-item", Map{"asc": "id,"}},
		{"asc-type", Map{"asc": []string{"id"}}},
		{"desc-type", Map{"desc": nil}},
		{"omit-type", Map{"omitFields": []string{"name"}}},
		{"omit-expression", Map{"omitFields": "name; SELECT 1"}},
	}
	apis := []struct {
		name  string
		query func(*Map, *DB) error
	}{
		{"Finds-page", func(w *Map, db *DB) error { _, err := Finds[paginationItem](FindsParams{Where: w, DB: db}); return err }},
		{"Finds-next", func(w *Map, db *DB) error {
			_, err := Finds[paginationItem](FindsParams{Where: w, DB: db, Mode: FindsModeNext})
			return err
		}},
		{"Finds-cursor", func(w *Map, db *DB) error {
			_, err := Finds[paginationItem](FindsParams{Where: w, DB: db, Mode: FindsModeCursor, Cursor: &CursorSpec{Fields: []string{"id"}}})
			return err
		}},
		{"FindPage", func(w *Map, db *DB) error { var out []paginationItem; _, err := FindPage(w, &out, db); return err }},
		{"FindPageBy", func(w *Map, db *DB) error { var out []paginationItem; _, err := FindPageBy(w, &out, db); return err }},
		{"FindNext", func(w *Map, db *DB) error { var out []paginationItem; _, err := FindNext(w, &out, db); return err }},
		{"FindNextBy", func(w *Map, db *DB) error { var out []paginationItem; _, err := FindNextBy(w, &out, db); return err }},
		{"Find", func(w *Map, db *DB) error { var out []paginationItem; return Find(&out, w, db) }},
		{"Where", func(w *Map, db *DB) error {
			var out []paginationItem
			tx, _, _ := Where(w, db)
			return tx.Find(&out).Error
		}},
	}
	for _, api := range apis {
		for _, input := range inputs {
			t.Run(api.name+"/"+input.name, func(t *testing.T) {
				var sql bytes.Buffer
				queryDB := db.Session(&gorm.Session{Logger: gormlogger.New(log.New(&sql, "", 0), gormlogger.Config{LogLevel: gormlogger.Info, SlowThreshold: time.Second})})
				where := cloneFindsWhere(&input.where)
				err := api.query(&where, queryDB)
				if !errors.Is(err, ErrInvalidQuery) {
					t.Fatalf("error = %v, want ErrInvalidQuery", err)
				}
				if sql.Len() != 0 {
					t.Fatalf("rejected input executed SQL: %s", sql.String())
				}
				if db.Error != nil {
					t.Fatalf("rejection contaminated shared DB: %v", db.Error)
				}
			})
		}
	}
}

func TestWherePreservesSupportedFiltersAndOrdering(t *testing.T) {
	db := newPaginationDB(t)
	cases := []struct {
		where Map
		ids   []int
	}{
		{Map{"id": 2}, []int{2}},
		{Map{"id": []int{1, 3}}, []int{1, 3}},
		{Map{"name": "o"}, []int{1, 2}},
		{Map{"name*": "o"}, []int{1, 2}},
		{Map{"^name": "t"}, []int{2, 3}},
		{Map{"name$": "e"}, []int{1, 3}},
		{Map{"id IN": []int{1, 3}}, []int{1, 3}},
		{Map{"id NOTIN": []int{2}}, []int{1, 3}},
		{Map{"id !=": 2}, []int{1, 3}},
		{Map{"id >": 1}, []int{2, 3}},
		{Map{"id <": 3}, []int{1, 2}},
		{Map{"id >=": 2}, []int{2, 3}},
		{Map{"id <=": 2}, []int{1, 2}},
		{Map{"pagination_items.id >": 1}, []int{2, 3}},
		{Map{"pagination_items.id !=": 2}, []int{1, 3}},
		{Map{"pagination_items.name*": "o"}, []int{1, 2}},
		{Map{"pagination_items.id IN": []int{1, 3}}, []int{1, 3}},
		{Map{"asc": " name, pagination_items.id "}, []int{1, 3, 2}},
		{Map{"desc": "name, id"}, []int{2, 3, 1}},
		{Map{"asc": "", "desc": "", "omitFields": ""}, []int{1, 2, 3}},
		{Map{"id": nil, "name*": ""}, []int{1, 2, 3}},
	}
	for i, c := range cases {
		w := cloneFindsWhere(&c.where)
		if !w.Contains("asc") && !w.Contains("desc") {
			w["asc"] = "id"
		}
		result, err := Finds[paginationItem](FindsParams{Where: &w, DB: db})
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		ids := make([]int, len(result.Data))
		for i, row := range result.Data {
			ids[i] = row.ID
		}
		if !reflect.DeepEqual(ids, c.ids) {
			t.Fatalf("case %d: IDs=%v, want %v", i, ids, c.ids)
		}
	}
	w := Map{"omitFields": "name", "asc": "id", "p": 2, "l": 1}
	result, err := Finds[paginationItem](FindsParams{Where: &w, DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 || result.Data[0].ID != 2 || result.Data[0].Name != "" || result.Total == nil || *result.Total != 3 {
		t.Fatalf("pagination/omission changed: %+v", result)
	}
}

func TestWhereKeepsAttackStringsAsBoundValues(t *testing.T) {
	db := newPaginationDB(t)
	attack := "x' OR 1=1 --"
	if err := db.Create(&paginationItem{ID: 4, Name: attack}).Error; err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "name*", "^name", "name$", "name IN"} {
		var value any = attack
		if key == "name IN" {
			value = []string{attack}
		}
		w := Map{key: value}
		result, err := Finds[paginationItem](FindsParams{Where: &w, DB: db})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Data) != 1 || result.Data[0].ID != 4 {
			t.Fatalf("%s broadened literal search: %+v", key, result.Data)
		}
	}
}

func TestWhereRejectionDoesNotPoisonScopedDB(t *testing.T) {
	db := newPaginationDB(t).Where("id = ?", 2)
	w := Map{"asc": "id; SELECT 1", "p": 2, "l": 1}
	tx, _, _ := Where(&w, db)
	if !errors.Is(tx.Error, ErrInvalidQuery) || db.Error != nil {
		t.Fatalf("error isolation: rejected=%v shared=%v", tx.Error, db.Error)
	}
	if len(w) != 3 {
		t.Fatalf("rejection consumed query controls: %v", w)
	}
	result, err := Finds[paginationItem](FindsParams{DB: db})
	if err != nil || len(result.Data) != 1 || result.Data[0].ID != 2 {
		t.Fatalf("shared scope changed: %+v %v", result, err)
	}
}

func TestWhereQuotesIdentifiersAcrossDialects(t *testing.T) {
	base := newPaginationDB(t)
	for _, c := range []struct {
		name      string
		dialector gorm.Dialector
		quote     string
	}{
		{"mysql", mysql.New(mysql.Config{Conn: base.Statement.ConnPool, SkipInitializeWithVersion: true}), "`"},
		{"postgres", postgres.New(postgres.Config{Conn: base.Statement.ConnPool}), `"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, err := gorm.Open(c.dialector, &gorm.Config{DryRun: true, DisableAutomaticPing: true})
			if err != nil {
				t.Fatal(err)
			}
			w := Map{"pagination_items.id >": 1, "pagination_items.name*": "' OR 1=1 --", "desc": "pagination_items.id"}
			tx, _, _ := Where(&w, db)
			var out []paginationItem
			tx = tx.Find(&out)
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			sql := tx.Statement.SQL.String()
			column := c.quote + "pagination_items" + c.quote + "." + c.quote + "id" + c.quote
			if !strings.Contains(sql, column+" > ") || !strings.Contains(sql, "ORDER BY "+column+" DESC") {
				t.Fatalf("unquoted identifier: %s", sql)
			}
			if strings.Contains(sql, "OR 1=1") || len(tx.Statement.Vars) != 3 {
				t.Fatalf("unbound value: %s vars=%v", sql, tx.Statement.Vars)
			}
		})
	}
}

func TestFindsCursorIgnoresValidatedLegacySort(t *testing.T) {
	db := newPaginationDB(t)
	w := Map{"asc": "name, id", "desc": "name", "p": 5, "l": 2}
	result, err := Finds[paginationItem](FindsParams{DB: db, Where: &w, Mode: FindsModeCursor, Cursor: &CursorSpec{Fields: []string{"id"}, Desc: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 2 || result.Data[0].ID != 3 || result.Data[1].ID != 2 || result.P != 1 {
		t.Fatalf("cursor order/offset changed: %+v", result)
	}
	if !w.Contains("asc") || !w.Contains("desc") || !w.Contains("p") {
		t.Fatalf("caller map mutated: %v", w)
	}
}

func TestWherePreservesTrustedScopesAndExpressions(t *testing.T) {
	db := newPaginationDB(t).Where("id > ?", 1).Order("CASE WHEN id = 3 THEN 0 ELSE 1 END")
	w := Map{"id <=": Expr("? + ?", 1, 2)}
	result, err := Finds[paginationItem](FindsParams{DB: db, Where: &w})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 2 || result.Data[0].ID != 3 || result.Data[1].ID != 2 {
		t.Fatalf("trusted scope/expression changed: %+v", result.Data)
	}
}
