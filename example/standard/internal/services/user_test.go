package services

import (
	"context"
	"errors"
	"testing"

	"github.com/xs23933/core/v3/example/standard/internal/dao"
	domain "github.com/xs23933/core/v3/example/standard/internal/domain/user"
	"github.com/xs23933/core/v3/example/standard/internal/models"
	"github.com/xs23933/uid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestUserQueries(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	row := models.User{Username: "alice", Status: models.USER_STATUS_ACTIVE}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewUserService(dao.NewUserDAO(db))
	page, err := svc.List(context.Background(), 1, 20)
	if err != nil || page.Total != 1 || len(page.Data) != 1 || page.Data[0].Status != "active" {
		t.Fatalf("list = %+v, %v", page, err)
	}
	result, err := svc.Get(context.Background(), row.ID.String())
	if err != nil || result.Username != "alice" {
		t.Fatalf("get = %+v, %v", result, err)
	}
	if _, err := svc.Get(context.Background(), uid.New().String()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := svc.Get(context.Background(), "invalid"); !errors.Is(err, domain.ErrInvalidQuery) {
		t.Fatalf("invalid id: %v", err)
	}
	if _, err := svc.List(context.Background(), 0, 101); !errors.Is(err, domain.ErrInvalidQuery) {
		t.Fatalf("invalid page: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.List(ctx, 1, 20); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list: %v", err)
	}
	if _, err := svc.Get(ctx, row.ID.String()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled get: %v", err)
	}
}
