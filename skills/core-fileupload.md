---
name: core-fileupload
description: Core 框架文件上传处理，包括单文件/多文件上传、自动绑定、校验（大小/MIME）、保存
tags: [go, core-framework, upload, multipart, file]
---

# Core 文件上传

## 概述

Core 提供三种文件上传方式：
1. **SaveFile / SaveFiles** — 快捷保存，一行代码完成上传+存储
2. **FormFile** — 获取原始 `*multipart.FileHeader`，手动处理
3. **ReadBody 自动绑定** — struct tag 声明式上传，支持校验+自动保存

## 配置

```yaml
# Multipart 表单内存缓冲上限（超过则写临时文件），默认 32MB
max_multipart_memory: 33554432
```

通过 `app.MaxMultipartMemory` 访问，`core.New()` 自动读取。

## 方式一：SaveFile / SaveFiles

### SaveFile — 单文件上传

```go
app.POST("/upload", func(c core.Ctx) error {
    // 最简：key="file"，保存到 ./images 目录
    relpath, abspath, err := c.SaveFile("file", "/images")

    // 指定根目录（relpath 不含 root，abspath 包含）
    relpath, abspath, err := c.SaveFile("file", "/images", "./static")

    // 指定 ID 子目录（uid/int/int64/uint64/uuid）
    relpath, abspath, err := c.SaveFile("file", "/images", "./static", userID)

    // 重命名文件（用 UUID 替换原文件名）
    relpath, abspath, err := c.SaveFile("file", "/images", "./static", userID, true)

    return c.ToJSON(Map{"path": relpath, "abs": abspath}, nil)
})
```

**路径规则**：`{dst}/{month}/{id?}/{filename}`

| 参数 | 说明 |
|------|------|
| `key` | Multipart 表单字段名 |
| `dst` | 相对路径前缀（如 `/images`） |
| `root` | （可选）物理根目录（如 `./static`） |
| `id` | （可选）子目录 ID，支持 `uid.UID`/`int`/`int64`/`uint`/`uint64`/`uuid.UUID` |
| `rename` | （可选）`bool`，true 时用 UUID 重命名文件，保留扩展名 |

**返回值**：
- `relpath` — 相对 URL 路径（不含 root），如 `/images/05/5hsbkthaadld/avatar.png`
- `abspath` — 物理绝对路径，如 `./static/images/05/5hsbkthaadld/avatar.png`

### SaveFiles — 多文件上传

```go
app.POST("/upload/batch", func(c core.Ctx) error {
    // 返回所有文件的相对路径数组
    rels, err := c.SaveFiles("files", "/images", "./static", true)

    // rels = ["/images/05/uuid1.jpg", "/images/05/uuid2.jpg", ...]
    return c.ToJSON(Map{"files": rels}, nil)
})
```

参数与 `SaveFile` 相同，返回 `[]string`（相对路径数组）。

## 方式二：FormFile

```go
app.POST("/upload/raw", func(c core.Ctx) error {
    fh, err := c.FormFile("file")
    if err != nil {
        return err
    }

    // fh.Filename  — 原始文件名
    // fh.Size      — 文件大小（字节）
    // fh.Open()    — 打开文件流

    src, err := fh.Open()
    if err != nil {
        return err
    }
    defer src.Close()

    // 自行处理文件内容...
    data, _ := io.ReadAll(src)

    return c.Send(data)
})
```

## 方式三：ReadBody 自动绑定（推荐）

通过 struct tag 声明文件字段，自动校验大小、MIME 类型、保存路径。

### 基本绑定

```go
type UploadReq struct {
    File  *multipart.FileHeader   `form:"file"`
    Files []*multipart.FileHeader `form:"files"`
}

app.POST("/upload/bind", func(c core.Ctx) error {
    var req UploadReq
    if err := c.ReadBody(&req); err != nil {
        return err
    }

    // req.File  — 第一个文件
    // req.Files — 所有文件
    fmt.Println(req.File.Filename, len(req.Files))
    return c.ToJSON(Map{"name": req.File.Filename}, nil)
})
```

### 校验 Tag

```go
type UploadReq struct {
    // max: 限制文件大小（支持 KB/MB 后缀）
    // mime: 限制 MIME 类型（逗号分隔，前缀匹配）
    // save: 自动保存到指定目录
    Avatar *multipart.FileHeader `form:"avatar" max:"5MB" mime:"image/jpeg,image/png" save:"./uploads/avatars"`
    Doc    *multipart.FileHeader `form:"doc" max:"10MB" mime:"application/pdf" save:"./uploads/docs"`
    Photos []*multipart.FileHeader `form:"photos" max:"20MB" mime:"image/" save:"./uploads/photos"`
}
```

### Tag 说明

| Tag | 格式 | 说明 |
|-----|------|------|
| `form:"key"` | 字符串 | Multipart 表单字段名 |
| `max:"5MB"` | `数字KB`/`数字MB`/纯数字(字节) | 文件大小上限，超出返回 `file too large` |
| `mime:"image/jpeg,image/png"` | 逗号分隔 | MIME 类型白名单（前缀匹配，`image/` 匹配所有图片） |
| `save:"./dir"` | 目录路径 | 自动保存文件到该目录（UUID 重命名，保留扩展名） |

### MIME 匹配规则

使用 `http.DetectContentType` 检测文件前 512 字节的真实类型（不依赖扩展名），与 tag 中的白名单**前缀匹配**：

- `"image/jpeg"` → 精确匹配 JPEG
- `"image/"` → 匹配所有 `image/*`
- `"image/jpeg,image/png"` → 匹配 JPEG 或 PNG

## MakePath — 路径生成工具

`SaveFile` 内部调用 `MakePath` 生成存储路径，也可独立使用：

```go
// 生成存储路径
relPath, absPath, err := core.MakePath("photo.jpg", "/images", "./static", userID, true)
// relPath = /images/05/00001234/uuid.jpg
// absPath = /full/path/static/images/05/00001234/uuid.jpg
// 自动创建中间目录
```

## 完整示例：头像上传

```go
type AvatarUpload struct {
    core.Handler
}

func (h *AvatarUpload) Prefix() string { return "/api/avatar" }

func (h *AvatarUpload) PostUpload(c core.Ctx) error {
    var req struct {
        Avatar *multipart.FileHeader `form:"avatar" max:"2MB" mime:"image/" save:"./static/avatars"`
    }
    if err := c.ReadBody(&req); err != nil {
        return c.ToJSON(nil, err)
    }

    // save tag 已自动保存文件
    // 如需自定义路径，用 SaveFile
    // relpath, _, _ := c.SaveFile("avatar", "/avatars", "./static", userID, true)

    return c.ToJSON(Map{"filename": req.Avatar.Filename}, nil)
}

func main() {
    cfg := core.LoadConfigFile("config.dat")
    app := core.New(cfg)
    core.RegHandle(&AvatarUpload{})
    app.Run()
}
```

## 注意事项

1. **MIME 检测基于内容**，不依赖扩展名，安全可靠
2. **max tag** 检查 `FileHeader.Size`（客户端声明的大小），不含 `save` 时不会读取文件内容
3. **save tag** 自动创建目录（`os.MkdirAll`），UUID 重命名避免冲突
4. **SaveFile** 的 `rename` 参数为 true 时用 UUID 替换文件名，false 保留原始文件名
5. 多文件上传时 `SaveFiles` 返回路径数组，`ReadBody` 绑定到 `[]*multipart.FileHeader`
6. 默认内存缓冲 32MB，大文件自动写入临时目录，通过 `max_multipart_memory` 配置调整
