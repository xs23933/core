package core

import (
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
