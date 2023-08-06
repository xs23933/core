package models

import (
	"github.com/xs23933/core/v2"
	"github.com/xs23933/uid"
)

type User struct {
	core.Model
	User     string `json:"user" gorm:"size:32"`
	Password string `json:"password" gorm:"size:96"`
}

func (m *User) Save() error {
	return DB.Save(m).Error
}

func UserById(id uid.UID) (user User, err error) {
	err = DB.First(&user, "id = ?", id).Error
	return
}

func UserPage() (any, error) {
	result := make([]User, 0)
	err := core.Find(&result)
	return result, err
}

func InitDB() {
	DB = core.Conn()
	DB.AutoMigrate(&User{})
}

var (
	DB *core.DB
)
