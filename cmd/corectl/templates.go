package main

const goModTemplate = `module {{.ModulePath}}

go {{.GoVersion}}

require github.com/xs23933/core/v3 {{.CoreVersion}}
`

const mainGoTemplate = `package main

import (
	"log"

	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/middleware/cors"
	"github.com/xs23933/core/v3/middleware/requestid"

	_ "{{.ModulePath}}/internal/handler"
)

func main() {
	app := core.New(core.LoadConfigFile("config.yaml"))

	app.Use(requestid.New())
	app.Use(cors.New(app))

	if err := app.Run(); err != nil {
		log.Fatalf("server startup failed: %v", err)
	}
}
`

const handlerGoTemplate = `package handler

import (
	"github.com/xs23933/core/v3"
	"{{.ModulePath}}/internal/service"
)

// BaseHandler 提供所有 Handler 的基础能力
// 可通过嵌入此 struct 快速创建新的 Handler
type BaseHandler struct {
	core.Handler
}

// UserHandler 用户相关 HTTP 接口
type UserHandler struct {
	core.Handler
	svc *service.UserService
}

func init() {
	core.RegHandle(&UserHandler{
		svc: service.NewUserService(),
	})
}

func (h *UserHandler) Init() {
	h.Prefix("/api/v1/users")
}

func (h *UserHandler) Preload(c core.Ctx) error {
	return c.Next()
}

// Post 获取用户列表
// POST /api/v1/users
func (h *UserHandler) Post(c core.Ctx) {
	whr := core.Map{}

	if err := c.BodyParser(&whr); err != nil {
		c.ToJSON(nil, err)
		return
	}

	c.ToJSON(h.svc.GetUserList(c.Ctx(), &whr))
}

// GetByID 根据 ID 获取用户详情
// GET /api/v1/users/:id
func (h *UserHandler) GetByID(c core.Ctx) {
	id := c.Params("id")
	user, err := h.svc.GetUserByID(c.Ctx(), id)
	c.ToJSON(user, err)
}
`

const configYamlTemplate = `debug: true
listen: ":8080"

database:
  default:
    type: "sqlite"
    dsn: "data.db"

restful:
  status: "status"
  data: "data"
  message: "msg"
`

const userModelTemplate = `package models

import "github.com/xs23933/core/v3"

// User 用户数据模型
type User struct {
	core.SModels

	Username string ` + "`json:\"username\" gorm:\"size:64;uniqueIndex;not null\"`" + `
	Email    string ` + "`json:\"email\" gorm:\"size:128;uniqueIndex\"`" + `
	Password string ` + "`json:\"-\" gorm:\"size:64;not null\"`" + `
	Phone    string ` + "`json:\"phone\" gorm:\"size:20\"`" + `
	Status   int    ` + "`json:\"status\" gorm:\"default:1;comment:'1:active 0:disabled'\"`" + `
}

func (User) TableName() string {
	return "users"
}
`

const userDaoTemplate = `package dao

import (
	"context"

	"{{.ModulePath}}/internal/models"

	"github.com/xs23933/core/v3"
)

// UserDAO 用户数据访问对象，统一管理 User 相关的数据库操作
type UserDAO struct {
	db *core.DB
}

// NewUserDAO 创建 UserDAO 实例
func NewUserDAO(db *core.DB) *UserDAO {
	db.AutoMigrate(&models.User{})
	return &UserDAO{db: db}
}

var UserDAOApp = &UserDAO{}

// Create 创建用户
func (dao *UserDAO) Create(ctx context.Context, user *models.User) error {
	return dao.db.Create(user).Error
}

// GetByID 根据 ID 查询用户
func (dao *UserDAO) GetByID(ctx context.Context, id string) (*models.User, error) {
	var user models.User
	err := dao.db.First(&user, "id = ?", id).Error
	return &user, err
}

// GetByUsername 根据用户名查询用户
func (dao *UserDAO) GetByUsername(ctx context.Context, username string) (*models.User, error) {
	var user models.User
	err := dao.db.First(&user, "username = ?", username).Error
	return &user, err
}

// List 获取用户列表（分页）
func (dao *UserDAO) List(ctx context.Context, whr *core.Map) (result core.NextPage[models.User], err error) {
	var users []models.User
	return core.FindNextBy(whr, &users, dao.db.WithContext(ctx))
}

// Update 更新用户信息
func (dao *UserDAO) Update(ctx context.Context, id string, updates map[string]any) error {
	return dao.db.Model(&models.User{}).Where("id = ?", id).Updates(updates).Error
}

// Delete 删除用户（软删除）
func (dao *UserDAO) Delete(ctx context.Context, id string) error {
	return dao.db.Delete(&models.User{}, "id = ?", id).Error
}
`

const userServiceTemplate = `package service

import (
	"context"
	"errors"

	"github.com/xs23933/core/v3"
	"gorm.io/gorm"

	"{{.ModulePath}}/internal/dao"
	"{{.ModulePath}}/internal/models"
)

// UserService 用户业务逻辑层，兼容 HTTP 和 gRPC 两种调用方式
type UserService struct {
	dao *dao.UserDAO
}

// NewUserService 创建 UserService 实例（依赖注入）
func NewUserService() *UserService {
	return &UserService{dao: dao.UserDAOApp}
}

// UserVO 用户视图对象（对外输出）
type UserVO struct {
	ID        string ` + "`json:\"id\"`" + `
	Username  string ` + "`json:\"username\"`" + `
	Email     string ` + "`json:\"email\"`" + `
	Phone     string ` + "`json:\"phone\"`" + `
	Status    int    ` + "`json:\"status\"`" + `
	CreatedAt string ` + "`json:\"created_at\"`" + `
	UpdatedAt string ` + "`json:\"updated_at\"`" + `
}

// toUserVO 将 Model 转换为 VO（禁止将 Model 直接暴露给前端）
func toUserVO(m *models.User) *UserVO {
	return &UserVO{
		ID:        m.ID.String(),
		Username:  m.Username,
		Email:     m.Email,
		Phone:     m.Phone,
		Status:    m.Status,
		CreatedAt: m.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt: m.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}

// GetUserList 获取用户列表
func (s *UserService) GetUserList(ctx context.Context, whr *core.Map) (*core.NextPage[UserVO], error) {
	resp, err := s.dao.List(ctx, whr)
	if err != nil {
		return nil, err
	}
	result := make([]UserVO, 0, len(resp.Data))
	for i, u := range resp.Data {
		result[i] = *toUserVO(&u)
	}
	return &core.NextPage[UserVO]{
		P:    resp.P,
		L:    resp.L,
		Next: resp.Next,
		Prev: resp.Prev,
		Data: result,
	}, nil
}

// GetUserByID 根据 ID 获取用户
func (s *UserService) GetUserByID(ctx context.Context, id string) (*UserVO, error) {
	user, err := s.dao.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, core.NewError(404, "user not found")
		}
		return nil, err
	}
	return toUserVO(user), nil
}
`

const middlewareAuthTemplate = `package middleware

import (
	"github.com/xs23933/core/v3"
)

// Auth 身份认证中间件
func Auth() func(core.Ctx) error {
	return func(c core.Ctx) error {
		token := c.GetHeader("Authorization")
		if token == "" {
			return c.ToJSON(nil, core.NewError(401, "unauthorized"))
		}
		// TODO: 实现实际的 Token 验证逻辑
		return c.Next()
	}
}
`
