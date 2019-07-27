package core

import (
	"fmt"

	"github.com/casbin/casbin"
	gormadapter "github.com/casbin/gorm-adapter"
	"github.com/pelletier/go-toml"
)

var (
	cas *casbin.Enforcer
)

func load() (err error) {
	defer func() {
		if e := recover(); e != nil {
			err = fmt.Errorf("")
		}
	}()
	err = cas.LoadPolicy()
	return
}

// NewPolicy 创建权限检查
func NewPolicy() *casbin.Enforcer {
	if err := load(); err == nil {
		return cas
	}

	dbTree := Config.Get("db").(*toml.Tree)
	adapter := new(gormadapter.Adapter)
	driver := dbTree.Get("driver").(string)

	if driver == "mysql" {
		adapter = gormadapter.NewAdapter(driver, fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4",
			dbTree.Get("user").(string),
			dbTree.Get("password").(string),
			dbTree.Get("host").(string),
			dbTree.Get("port").(string),
			dbTree.Get("db").(string),
		), true)
	} else {
		adapter = gormadapter.NewAdapter(driver, dbTree.Get("db").(string), true)
	}
	m := casbin.NewModel()
	m.AddDef("r", "r", "sub, dom, obj, act")
	m.AddDef("p", "p", "sub, dom, obj, act")
	m.AddDef("g", "g", "_, _, _")
	m.AddDef("e", "e", "some(where (p.eft == allow))")
	m.AddDef("m", "m", `g(r.sub, p.sub, r.dom) && keyMatch(r.dom, p.dom) && keyMatch(r.obj, p.obj) && (r.act == p.act || p.act == "*") || r.sub == "admin"`)
	rules := casbin.NewEnforcer(m, adapter)
	rules.LoadPolicy()
	cas = rules
	return rules
}
