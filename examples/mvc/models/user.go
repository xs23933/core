package models

import "github.com/xs23933/core"

type User struct {
	core.Model
	User     string `gorm:"size:128"`
	Password string `gorm:"size:96"`
}

func InitDB() {
	core.Conn().AutoMigrate(new(User))
}
