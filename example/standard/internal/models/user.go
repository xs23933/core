package models

import "github.com/xs23933/core/v3"

type User struct {
	core.Model
	Username string     `gorm:"size:64;uniqueIndex;not null"`
	Status   UserStatus `gorm:"not null;default:0"`
}
