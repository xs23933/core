package core

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xs23933/uid"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type Pages struct {
	P, L  int
	Total int64
	Data  interface{}
}

type Model struct {
	ID        uid.UID `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *gorm.DeletedAt `json:",omitempty"`
}

func (m *Model) BeforeCreate(tx *DB) error {
	if m.ID.IsEmpty() {
		m.ID = uid.New()
	}
	return nil
}

// FindPage Gorm find to page process whr
func FindPage(whr *Map, out interface{}, db ...*DB) (result Pages, err error) {
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

// Find find all data record max 10000
func Find(out interface{}, args ...interface{}) error {
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

func NewModel(conf Options, debug bool) (*DB, error) {
	tp := conf.GetString("type")
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
	case "sqlite":
		dial = sqlite.Open(dsn)
	}
	db, err := gorm.Open(dial)
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

// 字典类型

// Map map[string]interface{}
type Map map[string]interface{}

// Value 数据驱动接口
func (d Map) Value() (driver.Value, error) {
	bytes, err := json.Marshal(d)
	return string(bytes), err
}

func (d Map) GetString(k string) (value string) {
	if val, ok := d[k]; ok && val != nil {
		if value, ok = val.(string); ok {
			return
		}
	}
	return ""
}

func (d Map) ToString(k string, def ...string) string {
	if val, ok := (d)[k]; ok && val != nil {
		switch v := val.(type) {
		case string:
			return v
		case []byte:
			return string(v)
		case float64:
			return strconv.Itoa(int(v))
		case int:
			return strconv.Itoa(v)
		default:
			D("unknow %v", v)
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

func (d Map) GetBool(k string) (value bool) {
	if val, ok := d[k]; ok && val != nil {
		if value, ok = val.(bool); ok {
			return
		}
	}
	return false
}

// Scan 数据驱动接口
func (d *Map) Scan(src interface{}) error {
	switch val := src.(type) {
	case string:
		return json.Unmarshal([]byte(val), d)
	case []byte:
		if strings.EqualFold(string(val), "null") {
			*d = make(Map)
			return nil
		}
		if err := json.Unmarshal(val, d); err != nil {
			*d = make(Map)
		}
		return nil
	}
	return fmt.Errorf("not support %s", src)
}

// GormDataType schema.Field DataType
func (Map) GormDataType() string {
	return "text"
}

/** 数组部分 **/

// Array 数组类型
type Array []interface{}

func (d Array) FindHandle(handle, value string) Map {
	for _, r := range d {
		var v Map
		switch val := r.(type) {
		case map[string]interface{}:
			v = Map(val)
		case Map:
			v = val
		}
		if v.GetString(handle) == value {
			return v
		}
	}
	return Map{}
}

// Value 数据驱动接口
func (d Array) Value() (driver.Value, error) {
	bytes, err := json.Marshal(d)
	return string(bytes), err
}

// Scan 数据驱动接口
func (d *Array) Scan(src interface{}) error {
	*d = Array{}
	switch val := src.(type) {
	case string:
		return json.Unmarshal([]byte(val), d)
	case []byte:
		if strings.EqualFold(string(val), "null") {
			return nil
		}
		if err := json.Unmarshal(val, d); err != nil {
			*d = Array{}
		}
		return nil
	}
	return fmt.Errorf("not support %s", src)
}

// Strings 转换为 []string
func (d Array) String() []string {
	arr := make([]string, 0)
	for _, v := range d {
		arr = append(arr, fmt.Sprint(v))
	}
	return arr
}

// StringsJoin 链接为字符串
func (d Array) StringsJoin(sp string) string {
	arr := d.String()
	return strings.Join(arr, sp)
}

// GormDataType schema.Field DataType
func (Array) GormDataType() string {
	return "text"
}

// 空字符串 存入数据库 存 NULL ，这样会跳过数据库唯一索引的检查
type StringOrNil string

// implements driver.Valuer, will be invoked automatically when written to the db
func (s StringOrNil) Value() (driver.Value, error) {
	if s == "" {
		return nil, nil
	}
	return []byte(s), nil
}

// implements sql.Scanner, will be invoked automatically when read from the db
func (s *StringOrNil) Scan(src interface{}) error {
	switch v := src.(type) {
	case string:
		*s = StringOrNil(v)
	case []byte:
		*s = StringOrNil(v)
	case nil:
		*s = ""
	}
	return nil
}

func (s StringOrNil) String() string {
	return string(s)
}

// ToHandle 转换名称为handle
func ToHandle(src string) string {
	src = strings.ToLower(src)
	r := strings.NewReplacer("^", "", "`", "", "~", "", "!", "", "@", "", "#", "", "%", "", "|", "", "\\", "", "[&]", "", "]", "", "/", "", "&", "", "--", "-", " ", "-", "'", "")
	return r.Replace(src)
}

// Where build page query
//  whr *Map
//  db  *DB optional
//  return *DB, pos, lmt
func Where(whr *Map, db ...*DB) (*DB, int, int) {
	var tx *DB
	if len(db) > 0 {
		tx = db[0]
	} else {
		tx = conn
	}

	wher := map[string]interface{}(*whr)

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
			if strings.HasSuffix(k, " >") || strings.HasSuffix(k, " <") {
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

var (
	conn *DB
)

type DB = gorm.DB
