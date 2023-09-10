package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/xs23933/uid"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func NewModel(conf Options, debug, colorful bool) (*DB, error) {
	var (
		db  *DB
		err error
	)
	tp := conf.GetString("type")
	dbType = tp
	dsn := conf.GetString("dsn")
	if dsn == "" {
		return nil, ErrNoConfig
	}
	var dial gorm.Dialector
	switch tp {
	case "mysql":
		dial = mysql.Open(dsn)
	case "pg":
		dial = postgres.Open(dsn)
	case "sqlite", "sqlite3":
		dial = sqlite.Open(dsn)
	}
	if !debug {
		db, err = gorm.Open(dial)
	} else {
		writer := new(Writers)
		db, err = gorm.Open(dial, &gorm.Config{
			Logger: logger.New(writer, logger.Config{
				SlowThreshold:             time.Second,
				LogLevel:                  logger.Info,
				IgnoreRecordNotFoundError: true,
				ParameterizedQueries:      false,
				Colorful:                  colorful,
			}),
		})
	}
	if err != nil {
		return nil, err
	}
	if debug {
		db = db.Debug()
	}
	D("%s Connected", tp)
	conn = db
	return db, err
}

type Model struct {
	ID        uid.UID         `gorm:"primaryKey" json:"id,omitempty"`
	CreatedAt time.Time       `json:"created_at" gorm:"<-:create"`
	UpdatedAt time.Time       `json:"updated_at"`
	DeletedAt *gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

func (m *Model) BeforeCreate(tx *DB) error {
	if m.ID.IsEmpty() {
		m.ID = uid.New()
	}
	return nil
}

type UUID struct {
	uuid.UUID
}

var UuidNil = UUID{uuid.Nil}

func NewUUID() UUID {
	uuid.EnableRandPool()
	return UUID{uuid.New()}
}

func (u UUID) IsEmpty() bool {
	return UUID{uuid.Nil} == u
}

func (u UUID) ToString() string {
	return strings.Replace(u.String(), "-", "", -1)
}

func UUIDFromString(s string) (UUID, error) {
	uu, err := uuid.Parse(s)
	if err != nil {
		return UuidNil, err
	}
	return UUID{uu}, nil
}

type Models struct {
	ID        UUID            `gorm:"primaryKey" json:"id,omitempty"`
	CreatedAt time.Time       `json:"created_at" gorm:"<-:create"`
	UpdatedAt time.Time       `json:"updated_at"`
	DeletedAt *gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

func (m *Models) BeforeCreate(tx *DB) error {
	if m.ID.IsEmpty() {
		m.ID = NewUUID()
	}
	m.ID.Value()
	return nil
}

type Pages struct {
	P     int   `json:"p"`
	L     int   `json:"l"`
	Total int64 `json:"total"`
	Data  any   `json:"data"`
	Extra any   `json:"extra,omitempty"`
}

// FindPage Gorm find to page process whr
func FindPage(whr *Map, out any, db ...*DB) (result Pages, err error) {
	var (
		total    int64
		tx       *DB
		pos, lmt int
	)
	if len(db) > 0 {
		tx, pos, lmt = Where(whr, db[0])
	} else {
		tx, pos, lmt = Where(whr)
	}
	err = tx.Find(out).Offset(-1).Limit(-1).Count(&total).Error
	result = Pages{
		P: pos, L: lmt,
		Total: total,
		Data:  out,
	}
	return
}

type NextPages struct {
	P    int  `json:"p"`
	L    int  `json:"l"`
	Next bool `json:"next"`
	Prev bool `json:"prev"`
	Data any  `json:"data"`
}

func FindNext(whr *Map, out any, db ...*DB) (result NextPages, err error) {
	var (
		lmt = 20
		pos = 1
		tx  *DB
	)
	if len(db) > 0 {
		tx, pos, lmt = Where(whr, db[0])
	} else {
		tx, pos, lmt = Where(whr)
	}
	act := tx.Limit(lmt + 1).Find(out)
	rows := act.RowsAffected
	err = act.Error
	result = NextPages{
		P: pos, L: lmt,
		Next: rows > int64(lmt),
		Prev: pos > 1,
		Data: out,
	}
	return
}

// Find find all data record max 10000
func Find(out any, args ...any) error {
	wher := make(Map)
	db := Conn()
	for _, arg := range args {
		switch a := arg.(type) {
		case *Map:
			wher = *a
		case *DB:
			db = a
		}
	}
	if _, ok := wher["l"]; !ok {
		wher["l"] = 10000
	}
	db, _, _ = Where(&wher, db)
	return db.Find(out).Error
}

// Where build page query
//
//	whr *Map
//	db  *DB optional
//	return *DB, pos, lmt
func Where(whr *Map, db ...*DB) (*DB, int, int) {
	var tx *DB
	if len(db) > 0 {
		tx = db[0]
	} else {
		tx = conn
	}

	wher := map[string]any(*whr)

	lmt := 20
	l, ok := wher["l"]
	if ok {
		delete(wher, "l") //删除lmt
		switch v := l.(type) {
		case int:
			lmt = v
		case float64:
			lmt = int(v)
		}
	}
	tx = tx.Limit(lmt)

	p, ok := wher["p"]
	pos := 1
	if ok {
		delete(wher, "p") // 删除 pos
		switch v := p.(type) {
		case int:
			pos = v
		case float64:
			pos = int(v)
		}
		if pos < 1 {
			pos = 1
		}
		tx = tx.Offset((pos - 1) * lmt)
	}

	asc, ok := wher["asc"].(string)
	if ok {
		delete(wher, "asc")
		tx = tx.Order(asc)
	}
	desc, ok := wher["desc"].(string)
	if ok {
		delete(wher, "desc")
		tx = tx.Order(fmt.Sprintf("%s desc", desc))
	}

	if name, ok := wher["name"]; ok {
		delete(wher, "name")
		if name != "" {
			tx = tx.Where("name like ?", fmt.Sprintf("%%%s%%", name))
		}
	}

	if omit, ok := wher["omitFields"]; ok { // 排除相应字段 多个,号隔开
		delete(wher, "omitFields")
		tx = tx.Omit(omit.(string))
	}

	// 过滤掉字符串等于空 的搜索
	if len(wher) > 0 {
		for k, v := range wher {
			if v == nil {
				delete(wher, k)
				continue
			}
			if x, ok := v.(string); ok && len(x) == 0 {
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, " NOTIN") {
				tx = tx.Where(fmt.Sprintf("%s NOT IN (?)", strings.TrimSuffix(k, " NOTIN")), v)
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, " IN") {
				tx = tx.Where(fmt.Sprintf("%s in (?)", strings.TrimSuffix(k, " IN")), v)
				delete(wher, k)
				continue
			}
			if strings.HasPrefix(k, "^") {
				tx = tx.Where(fmt.Sprintf("%s like ?", strings.TrimPrefix(k, "^")), fmt.Sprintf("%s%%", v))
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, "$") {
				tx = tx.Where(fmt.Sprintf("%s like ?", strings.TrimSuffix(k, "$")), fmt.Sprintf("%%%s", v))
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, "*") {
				tx = tx.Where(fmt.Sprintf("%s like ?", strings.TrimSuffix(k, "*")), fmt.Sprintf("%%%s%%", v))
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, " !=") {
				tx = tx.Where(fmt.Sprintf("`%s` <> ?", strings.TrimSuffix(k, " !=")), v)
				delete(wher, k)
				continue
			}
			if strings.HasSuffix(k, " >") || strings.HasSuffix(k, " <") ||
				strings.HasSuffix(k, " >=") || strings.HasSuffix(k, " <=") {
				ks := strings.Split(k, " ")
				ks[0] = fmt.Sprintf("`%s`", ks[0])
				ks = append(ks, "?")
				tx = tx.Where(strings.Join(ks, " "), v)
				delete(wher, k)
				continue
			}
		}
		tx = tx.Where(wher)
	}
	return tx, pos, lmt
}

func Conn() *DB {
	if conn != nil {
		return conn
	}
	Erro("database connect failed")
	return nil
}

func DBType() string {
	return dbType
}

var (
	conn   *DB
	dbType string
)

type DB = gorm.DB
