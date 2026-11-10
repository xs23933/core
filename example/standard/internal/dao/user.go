package dao

import (
	"context"

	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/example/standard/internal/models"
)

type UserDAO struct{ db *core.DB }

func NewUserDAO(db *core.DB) *UserDAO { return &UserDAO{db: db} }

func (d *UserDAO) GetByID(ctx context.Context, id string) (*models.User, error) {
	var row models.User
	if err := d.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (d *UserDAO) List(ctx context.Context, page, size int) (core.Page[models.User], error) {
	query := core.Map{"p": page, "l": size, "asc": "id"}
	var rows []models.User
	return core.FindPageBy(&query, &rows, d.db.WithContext(ctx))
}
