package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/gorilla/schema"
	"github.com/xs23933/core/v3/sid"
	"github.com/xs23933/core/v3/utils"
	"github.com/xs23933/core/v3/xid"
	"github.com/xs23933/uid"
)

type Ctx interface {
	Context() context.Context // Request().Context()
	Ctx() context.Context
	Host() string
	Response() ResponseWriter                                              // Response() return http.ResponseWriter
	Request() *http.Request                                                // Request() return *http.Request
	RedirectJS(to string, msg ...string)                                   // use js redirect
	Redirect(to string, stCode ...int) error                               // base redirect
	RemoteIP() net.IP                                                      // remote client ip
	SetCookie(name, value string, exp time.Time, path string, args ...any) // set cookie
	RemoveCookie(name, path string, dom ...string)                         // remove some cookie
	Cookie(cookie *http.Cookie)                                            // set cookie with cookie object
	Cookies(name string) (string, error)                                   // get some cookie
	ReadBody(out any, debug ...bool) error                                 // read put post any request body to struct or map
	Bind(out any, debug ...bool) error                                     // ReadBody alias  read put post form data to struct or map
	BodyParser(out any) error                                              // read put post form data to struct or map
	Validate(out any) error                                                // validate struct or map
	Next() error                                                           // next HandlerFunc
	Path() string                                                          // return http.Request.URI.path
	init(*Core, http.ResponseWriter, *http.Request)                        // Core call
	release()                                                              // Core called
	Send(buf []byte) error                                                 // send []byte data
	SendString(msg ...any) error                                           // send string to body
	SendStatus(code int, msg ...string) error                              // send status to client, options msg with display
	SetHeader(key string, value string)                                    // set response header
	GetHeader(key string, defaultValue ...string) string                   // get request header
	Method() string                                                        // return method e.g: GET,POST,PUT,DELETE,OPTION,HEAD...
	GetStatus() int                                                        // get response status
	Status(code int) Ctx                                                   // set response status
	Core() *Core                                                           // return app(*Core)
	Abort(args ...any) Ctx                                                 // Deprecated: As of v2.0.0, this function simply calls Ctx.Format.
	JSON(any, ...int) error                                                // send json
	JSONP(data any, callback ...string) error                              // send jsonp
	ToJSON(data any, msg ...any) error                                     // send json with status
	ToJSONCode(data any, msg ...any) error                                 // send have code to json
	StartAt(t ...time.Time) time.Time                                      // set ctx start time if t set, else get start at
	ParamsMaps() map[string]string
	Params(key string, defaultValue ...string) string                           // get Params data e.g c.Params("param")
	Param(key string, defaultValue ...string) string                            // get Param data e.g c.Param("param")
	ParamsUid(key string, defaultValue ...uid.UID) (uid.UID, error)             // get Param UID type, return uid.Nil if failed
	GetParamSid(key string, defaultValue ...sid.ID) (sid.ID, error)             // get Param ID type, return sid.Nil if failed
	ParamsSid(key string, defaultValue ...sid.ID) (sid.ID, error)               // get Param ID type, return sid.Nil if failed
	ParamsXid(key string, defaultValue ...xid.ID) (xid.ID, error)               // get Param XID type, return xid.Nil if failed
	ParamsUuid(key string, defaultValue ...UUID) (UUID, error)                  // get Param UID type, return uid.Nil if failed
	ParamUUID(key string, defaultValue ...UUID) UUID                            // get Param UUID type, return uid.Nil if failed
	ParamsInt(key string, defaultValue ...int) (int, error)                     // get Param int type, return -1 if failed
	GetParamUid(key string, defaultValue ...uid.UID) (uid.UID, error)           // get param uid.UID, return uid.Nil if failed
	GetParamInt(key string, defaultValue ...int) (int, error)                   // get param int, return -1 if failed
	File(filePath string)                                                       // send file
	FileAttachment(filepath, filename string)                                   // send file attachment
	FileFromFS(filePath string, fs http.FileSystem)                             // send file from FS
	Append(key string, values ...string) Ctx                                    // append response header
	Vary(fields ...string) Ctx                                                  // set response vary
	FormFile(key string) (*multipart.FileHeader, error)                         // get form file
	SaveFile(key, dst string, args ...any) (relpath, abspath string, err error) // upload some one file
	SaveFiles(key, dst string, args ...any) (rel Array, err error)              // upload multi-file
	Query(key string, def ...string) string                                     // get request query string like ?id=12345
	QueryInt(key string, def ...int) int                                        // parse form value to int
	Querys(key string, def ...[]string) []string                                // like query, but return []string values

	FromValueXid(key string, def ...xid.ID) xid.ID   // parse form value to xid
	FormValue(key string, def ...string) string      // like Query support old version
	FromValueInt(key string, def ...int) int         // parse form value to int
	FromValueUid(key string, def ...uid.UID) uid.UID // parse form value to uid
	FromValueUUID(key string, def ...UUID) UUID      // parse form value to uuid
	FormValues(key string, def ...[]string) []string // like Querys
	Flush(data any, statusCode ...int) error         // flush
	Accepts(offers ...string) string                 // Accepts checks if the specified extensions or content types are acceptable.
	AcceptsCharsets(offers ...string) string         // AcceptsCharsets checks if the specified charset is acceptable.
	AcceptsEncodings(offers ...string) string        // AcceptsEncodings checks if the specified encoding is acceptable.
	AcceptsLanguages(offers ...string) string        // AcceptsLanguages checks if the specified language is acceptable.
	Format(body any) error                           // Format performs content-negotiation on the Accept HTTP header. It uses Accepts to select a proper format. If the header is not specified or there is no proper format, text/plain is used.
	Type(extension string, charset ...string) Ctx    // 发送 response content-type
	XML(data any) error                              // output xml
	Set(key string, val any)
	Get(key string) (val any, ok bool)
	Locals(key string, val ...any) any // set get local ver
	GetString(key string, def ...string) (value string)
	GetBool(key string) (value bool)
	GetInt(key string, def ...int) (i int)
	GetInt64(key string, def ...int64) (i int64)
	GetUint(key string, def ...uint) (i uint)
	GetUint64(key string, def ...uint64) (i uint64)
	GetUUID(key string, def ...UUID) (v UUID)
	GetXid(key string, def ...xid.ID) (v xid.ID)
	GetSid(key string, def ...sid.ID) (v sid.ID)
	GetFloat64(key string, def ...float64) (value float64)
	GetTime(key string) (t time.Time)
	GetDuration(key string) (d time.Duration)
	GetStrings(key string, def ...[]string) (value []string)
	GetMap(key string, def ...map[string]any) (value map[string]any)
	GetMapString(key string, def ...map[string]string) (value map[string]string)
	GetMapStringSlice(key string, def ...map[string][]string) (value map[string][]string)
	GetAs(key string, v any) error
	Vars() Map
	Stream(step func(w io.Writer) bool) bool
	ViewReload() // set view reload
	// SSE 相关
	SSEWrite(event, data string, id ...string) error    // 写入单条 SSE 事件
	SSESend(event string, data any, id ...string) error // 发送结构化 SSE 事件（自动序列化 data）
	SSEComment(comment string) error                    // 发送 SSE 注释（心跳）
	// SSE 相关结束
	Render(f string, bind ...any) error
	TextBytes(out io.Writer, f string, bind ...any) error
	TextRender(f string, bind ...any) error
	SetParams(key string, val string)
}

type BaseCtx struct {
	wm           resp
	app          *Core        // Reference to *App
	handlers     HandlerFuncs // Reference to *Route
	indexRoute   int          // Index of the current route
	indexHandler int          // Index of the current handler
	method       string       // HTTP method
	methodInt    MethodType
	baseURI      string
	path         string // HTTP path with the modifications by the configuration -> string copy from pathBuffer
	pathOriginal string // Original HTTP path
	matched      bool   // Whether the request matched an explicit route
	theme        string
	W            ResponseWriter
	R            *http.Request
	ctx          context.Context
	vars         Map
	querys       url.Values
	startAt      time.Time
	respJsonKeys *RestfulDefine
	mu           sync.RWMutex
	params       map[string]string
}

// IsRouteFallback reports whether the current handler chain is running because
// an OPTIONS request did not match an explicit route.
func IsRouteFallback(c Ctx) bool {
	ctx, ok := c.(*BaseCtx)
	return ok && ctx.Method() == MethodOptions && !ctx.matched
}

func (c *BaseCtx) Ctx() context.Context {
	return c.Request().Context()
}

func (c *BaseCtx) Context() context.Context {
	return c.Ctx()
}

func (c *BaseCtx) Host() string {
	return c.R.Host
}

func (c *BaseCtx) SetParams(key, val string) {
	if c.params == nil {
		c.params = make(map[string]string)
	}
	c.params[key] = val
}

// decoderPool helps to improve ReadBody's and QueryParser's performance
var decoderPool = utils.NewPool(func() *schema.Decoder {
	var decoder = schema.NewDecoder()
	decoder.IgnoreUnknownKeys(true)
	decoder.ZeroEmpty(true)
	decoder.RegisterConverter(time.Time{}, func(s string) reflect.Value {
		if s == "" {
			return reflect.Zero(reflect.TypeFor[time.Time]())
		}

		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return reflect.Zero(reflect.TypeFor[time.Time]())
		}
		return reflect.ValueOf(t)
	})
	return decoder
})

// ViewTheme 使用模版风格
func (c *BaseCtx) ViewTheme(theme string) {
	c.theme = theme
}

func (c *BaseCtx) ViewReload() {
	c.app.Views.Reload()
}

func (c *BaseCtx) Render(f string, bind ...any) error {
	var err error
	var binding any
	if len(bind) > 0 {
		binding = bind[0]
	} else {
		c.mu.RLock()
		binding = c.vars
		c.mu.RUnlock()
	}

	if c.app.Views == nil {
		err = fmt.Errorf("Render: Not Initial Views")
		Erro(err.Error())
		return err
	}
	if c.theme != "" {
		c.app.Views.SetTheme(c.theme)
	}

	err = c.app.Views.Execute(c.W, f, binding)
	if err != nil {
		c.SendStatus(StatusInternalServerError, err.Error())
	}
	return err
}

func (c *BaseCtx) TextRender(f string, bind ...any) error {
	var err error
	var binding any
	if len(bind) > 0 {
		binding = bind[0]
	} else {
		c.mu.RLock()
		binding = c.vars
		c.mu.RUnlock()
	}

	if c.app.TextEngine == nil {
		err = fmt.Errorf("Render: Not Initial TextEngine")
		Erro(err.Error())
		return err
	}
	if c.theme != "" {
		c.app.TextEngine.SetTheme(c.theme)
	}
	err = c.app.TextEngine.Execute(c.W, f, binding)
	if err != nil {
		c.SendStatus(StatusInternalServerError, err.Error())
	}
	return err
}

func (c *BaseCtx) TextBytes(out io.Writer, f string, bind ...any) error {
	var err error
	var binding any
	if len(bind) > 0 {
		binding = bind[0]
	} else {
		c.mu.RLock()
		binding = c.vars
		c.mu.RUnlock()
	}

	if c.app.TextEngine == nil {
		err = fmt.Errorf("Render: Not Initial TextEngine")
		Erro(err.Error())
		return err
	}
	if c.theme != "" {
		c.app.TextEngine.SetTheme(c.theme)
	}
	return c.app.TextEngine.Execute(out, f, binding)
}

// Stream sends a streaming response and returns a boolean
// indicates "Is client disconnected in middle of stream"
func (c *BaseCtx) Stream(step func(w io.Writer) bool) bool {
	w := c.W
	ctx := c.ctx
	for {
		select {
		case <-ctx.Done():
			return true
		default:
			keepOpen := step(w)
			w.Flush()
			if !keepOpen {
				return false
			}
		}
	}
}

// sseBufPool 复用 SSE 事件写入缓冲，避免每次 SSEWrite 分配 256B
var sseBufPool = utils.NewBytePool(256)

// SSEWrite writes a single Server-Sent Event to the response.
// Handles proper formatting of id, event, and data fields.
// Multi-line data is automatically prefixed with "data: " on each line.
func (c *BaseCtx) SSEWrite(event, data string, id ...string) error {
	w := c.W
	bufp := sseBufPool.Get()
	buf := *bufp
	defer sseBufPool.Put(bufp)

	if len(id) > 0 && id[0] != "" {
		buf = append(buf, "id: "...)
		buf = append(buf, id[0]...)
		buf = append(buf, '\n')
	}
	if event != "" {
		buf = append(buf, "event: "...)
		buf = append(buf, event...)
		buf = append(buf, '\n')
	}
	if data != "" {
		// support multi-line data: 直接扫描换行符，避免 splitLines 产生 []string 分配
		start := 0
		for i := 0; i < len(data); i++ {
			if data[i] == '\n' {
				buf = append(buf, "data: "...)
				buf = append(buf, data[start:i]...)
				buf = append(buf, '\n')
				start = i + 1
			}
		}
		if start < len(data) {
			buf = append(buf, "data: "...)
			buf = append(buf, data[start:]...)
			buf = append(buf, '\n')
		} else if start == len(data) && len(data) > 0 && data[len(data)-1] == '\n' {
			// trailing newline: 最后一个空行补一个空 data
			buf = append(buf, "data: \n"...)
		}
	} else {
		buf = append(buf, "data: \n"...)
	}
	buf = append(buf, '\n')

	_, err := w.Write(buf)
	w.Flush()
	return err
}

// SSESend sends a structured SSE event, auto-serializing data to JSON.
func (c *BaseCtx) SSESend(event string, data any, id ...string) error {
	var dataStr string
	switch v := data.(type) {
	case string:
		dataStr = v
	case []byte:
		dataStr = string(v)
	case nil:
		dataStr = ""
	default:
		b, err := sonic.MarshalString(v)
		if err != nil {
			return err
		}
		dataStr = b
	}
	return c.SSEWrite(event, dataStr, id...)
}

// SSEComment sends an SSE comment line (used for heartbeat/keepalive).
// The SSE spec says lines starting with ":" are comments and ignored by clients.
func (c *BaseCtx) SSEComment(comment string) error {
	w := c.W
	if comment == "" {
		comment = "ping"
	}
	_, err := w.WriteString(": " + comment + "\n\n")
	w.Flush()
	return err
}

// splitLines splits a string by \n, preserving empty lines.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	} else if len(lines) > 0 && start == len(s) {
		// trailing newline: add empty line
		lines = append(lines, "")
	}
	return lines
}

func (c *BaseCtx) Bind(out any, debug ...bool) error {
	return c.ReadBody(out, debug...)
}

// ReadBody binds the request body to a struct.
// It supports decoding the following content types based on the Content-Type header:
// application/json, application/xml, application/x-www-form-urlencoded, multipart/form-data
// If none of the content types above are matched, it will return a ErrUnprocessableEntity error
//
//	out any MIMEApplicationForm MIMEMultipartForm MIMETextXML must struct
//
// 单文件
// type Upload struct {
//     File *multipart.FileHeader `form:"file"`
// }
// f, _ := req.File.Open()
// defer f.Close()

// 多文件
//
//	type Upload struct {
//	    Files []*multipart.FileHeader `form:"files"`
//	}
func (c *BaseCtx) ReadBody(out any, debug ...bool) error {
	// Get decoder from pool
	schemaDecoder := decoderPool.Get()
	defer decoderPool.Put(schemaDecoder)

	schemaDecoder.ZeroEmpty(true)

	// Get content-type
	ctype := strings.ToLower(c.R.Header.Get(HeaderContentType))
	switch {
	case strings.HasPrefix(ctype, MIMEApplicationJSON):
		schemaDecoder.SetAliasTag("json")
		body, err := io.ReadAll(c.R.Body)
		if err != nil {
			if c.app.Debug || len(debug) > 0 && debug[0] {
				Warn("header")
				for v := range c.R.Header {
					Warn("  %s:\t\t%s", v, c.GetHeader(v))
				}
				Erro("body: %s\nerr: %s", body, err.Error())
			}
			return err
		}

		c.R.Body = io.NopCloser(bytes.NewBuffer(body))
		if err = sonic.Unmarshal(body, out); err != nil {
			if c.app.Debug || len(debug) > 0 && debug[0] {
				Warn("header")
				for v := range c.R.Header {
					Warn("  %s:\t\t%s", v, c.GetHeader(v))
				}

				Erro("body: \n%s\nerr: %s", body, err.Error())
			}
		}
		return err
	case strings.HasPrefix(ctype, MIMEApplicationForm):
		schemaDecoder.SetAliasTag("form")
		if err := c.R.ParseForm(); err != nil {
			Erro("ParseForm err: %s", err.Error())
			return err
		}
		return schemaDecoder.Decode(out, c.R.PostForm)
	case strings.HasPrefix(ctype, MIMEMultipartForm):
		schemaDecoder.SetAliasTag("form")
		if err := c.R.ParseMultipartForm(c.app.MaxMultipartMemory); err != nil {
			return err
		}
		// 解析普通字段
		if err := schemaDecoder.Decode(out, c.R.MultipartForm.Value); err != nil {
			return err
		}

		return c.bindMultipartFiles(out)

	case strings.HasPrefix(ctype, MIMETextXML), strings.HasPrefix(ctype, MIMEApplicationXML):
		schemaDecoder.SetAliasTag("xml")
		body, err := io.ReadAll(c.R.Body)
		if err != nil {
			return err
		}
		c.R.Body = io.NopCloser(bytes.NewBuffer(body))
		return xml.Unmarshal(body, out)
	}
	// No suitable content type found
	return ErrUnprocessableEntity
}

func (c *BaseCtx) bindMultipartFiles(out any) error {
	// 自动绑定 FileHeader
	files := c.R.MultipartForm.File
	if len(files) == 0 {
		return nil
	}

	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer {
		return nil
	}

	rv = rv.Elem()
	rt := rv.Type()

	fileHeaderType := reflect.TypeFor[*multipart.FileHeader]()
	// fileType := reflect.TypeFor[multipart.File]()

	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		tag := field.Tag.Get("form")
		if tag == "" {
			continue
		}

		fileHeaders, ok := files[tag]
		if !ok || len(fileHeaders) == 0 {
			continue
		}

		fv := rv.Field(i)

		if !fv.CanSet() {
			continue
		}

		// *multipart.FileHeader
		if fv.Type() == fileHeaderType {
			fh := fileHeaders[0]

			if err := c.processFileTags(field, fh); err != nil {
				return err
			}
			fv.Set(reflect.ValueOf(fh))
			continue
		}

		// []*multipart.FileHeader
		if fv.Type() == reflect.SliceOf(fileHeaderType) {
			for _, fh := range fileHeaders {
				if err := c.processFileTags(field, fh); err != nil {
					return err
				}
			}
			fv.Set(reflect.ValueOf(fileHeaders))
		}
	}

	return nil
}

func (c *BaseCtx) processFileTags(field reflect.StructField, fh *multipart.FileHeader) error {

	// max size
	if maxTag := field.Tag.Get("max"); maxTag != "" {

		maxBytes, err := parseSize(maxTag)
		if err != nil {
			return err
		}

		if fh.Size > maxBytes {
			return fmt.Errorf("file too large: %s", fh.Filename)
		}
	}

	// mime
	if mimeTag := field.Tag.Get("mime"); mimeTag != "" {

		mimes := strings.Split(mimeTag, ",")

		file, err := fh.Open()
		if err != nil {
			return err
		}
		defer file.Close()

		buff := make([]byte, 512)

		n, _ := file.Read(buff)

		mime := http.DetectContentType(buff[:n])

		ok := false

		for _, m := range mimes {
			if strings.Contains(mime, m) {
				ok = true
				break
			}
		}

		if !ok {
			return fmt.Errorf("invalid mime: %s", mime)
		}
	}

	// save
	if saveTag := field.Tag.Get("save"); saveTag != "" {

		if err := saveUploadedFile(fh, saveTag); err != nil {
			return err
		}
	}

	return nil
}

func parseSize(s string) (int64, error) {

	s = strings.ToUpper(s)

	if strings.HasSuffix(s, "MB") {
		v, _ := strconv.Atoi(strings.TrimSuffix(s, "MB"))
		return int64(v) * 1024 * 1024, nil
	}

	if strings.HasSuffix(s, "KB") {
		v, _ := strconv.Atoi(strings.TrimSuffix(s, "KB"))
		return int64(v) * 1024, nil
	}

	return strconv.ParseInt(s, 10, 64)
}

func saveUploadedFile(fh *multipart.FileHeader, dst string) error {

	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	name := uuid.New().String() + filepath.Ext(fh.Filename)

	path := filepath.Join(dst, name)

	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, src)

	return err
}

// BodyParser parses the request body into the provided 'out' parameter.
// It delegates the actual parsing to the ReadBody method.
func (c *BaseCtx) BodyParser(out any) error {
	return c.ReadBody(out)
}

// 全局验证器实例
// 自定义错误类型
var (
	ErrInvalidValidationType = NewError(400, "验证类型必须是结构体指针")
	validate                 = validator.New()
)

// Validate 验证传入的结构体
func (c *BaseCtx) Validate(out any) error {
	// 检查是否是结构体指针
	val := reflect.ValueOf(out)
	if val.Kind() != reflect.Pointer || val.Elem().Kind() != reflect.Struct {
		return ErrInvalidValidationType
	}

	// 执行验证
	if err := validate.Struct(out); err != nil {
		return err
	}

	return nil
}

// ReadBodyAndValidate 读取请求体并验证，返回统一的错误响应格式。
// 先调用 c.ReadBody(out) 解析，再调用 c.Validate(out) 验证。
// 解析失败返回 400，验证失败返回 422。
// 使用方式:
//
//	var req CreateUserReq
//	if err := c.ReadBodyAndValidate(&req); err != nil {
//	    return err
//	}
func (c *BaseCtx) ReadBodyAndValidate(out any) error {
	if err := c.ReadBody(out); err != nil {
		return err
	}
	if err := c.Validate(out); err != nil {
		// validator.ValidationErrors 转换为可读消息
		if errs, ok := err.(validator.ValidationErrors); ok {
			msg := make([]string, 0, len(errs))
			for _, fe := range errs {
				msg = append(msg, fmt.Sprintf("%s: %s", fe.Field(), fe.Tag()))
			}
			return NewError(422, strings.Join(msg, "; "))
		}
		return NewError(422, err.Error())
	}
	return nil
}

// Cookie

// SetCookie adds a Set-Cookie header to the ResponseWriter's headers.
// The provided cookie must have a valid Name. Invalid cookies may be
// silently dropped.
func (c *BaseCtx) SetCookie(name, value string, exp time.Time, path string, args ...any) {
	if path == "" {
		path = "/"
	}
	cookie := &http.Cookie{
		Name:     name,
		Value:    url.QueryEscape(value),
		Expires:  exp,
		Path:     path,
		SameSite: http.SameSiteLaxMode,
	}

	for _, arg := range args {
		switch a := arg.(type) {
		case string:
			if strings.EqualFold(a, "httponly") {
				cookie.HttpOnly = true
				continue
			}
			cookie.Domain = a
		case bool:
			cookie.Secure = a
		}
	}

	if cookie.Domain == "" { // read config domain
		cookie.Domain = c.Core().Conf.GetString("domain")
	}

	http.SetCookie(c.W, cookie)
}

func (c *BaseCtx) RemoveCookie(name, path string, dom ...string) {
	exp := time.Now().Add(-time.Hour)
	cookie := &http.Cookie{
		Name:    name,
		Value:   "",
		Expires: exp,
		Path:    path,
	}
	if len(dom) > 0 {
		cookie.Domain = dom[0]
	}
	if cookie.Domain == "" { // read config domain
		cookie.Domain = c.Core().Conf.GetString("domain")
	}
	http.SetCookie(c.W, cookie)
}

// Cookie sets a cookie by passing a cookie struct.
func (c *BaseCtx) Cookie(cookie *http.Cookie) {
	http.SetCookie(c.W, cookie)
}

// Cookie returns the named cookie provided in the request or
// ErrNoCookie if not found. And return the named cookie is unescaped.
// If multiple cookies match the given name, only one cookie will
// be returned.
func (c *BaseCtx) Cookies(name string) (string, error) {
	cookie, err := c.R.Cookie(name)
	if err != nil {
		return "", err
	}
	val, _ := url.QueryUnescape(cookie.Value)
	return val, nil
}

// RemoteIP parses the IP from Request.RemoteAddr, normalizes and returns the IP (without the port).
// It also checks if the remoteIP is a trusted proxy or not.
// In order to perform this validation, it will see if the IP is contained within at least one of the CIDR blocks
func (c *BaseCtx) RemoteIP() net.IP {
	return RemoteIP(c.R.Header, c.R.RemoteAddr)
	// 按照优先级检查各个HTTP头
	// for _, header := range ipHeaders {
	// 	ip := strings.TrimSpace(c.GetHeader(header))
	// 	if ip == "" {
	// 		continue
	// 	}
	// 	// 多个 IP 时取第一个（用户真实 IP）
	// 	if header == "X-Forwarded-For" {
	// 		parts := strings.Split(ip, ",")
	// 		ip = strings.TrimSpace(parts[0])
	// 	}
	// 	if realIP := net.ParseIP(ip); realIP != nil {
	// 		return realIP
	// 	}
	// }

	// // 最后 RemoteAddr
	// if host, _, err := net.SplitHostPort(strings.TrimSpace(c.R.RemoteAddr)); err == nil {
	// 	if realIP := net.ParseIP(host); realIP != nil {
	// 		return realIP
	// 	}
	// }

	// return nil
}

// set locals var
func (c *BaseCtx) Set(key string, val any) {
	c.mu.Lock()
	if c.vars == nil {
		c.vars = make(Map)
	}
	c.vars[key] = val
	c.mu.Unlock()
}

func (c *BaseCtx) Locals(key string, val ...any) any {
	if len(val) > 0 {
		c.Set(key, val[0])
		return nil
	}
	value, _ := c.Get(key)
	return value
}

// Get returns the value for the given key, ie: (value, true).
// If the value does not exists it returns (nil, false)
func (c *BaseCtx) Get(key string) (val any, ok bool) {
	c.mu.RLock()
	val, ok = c.vars[key]
	c.mu.RUnlock()
	return
}

func (c *BaseCtx) Vars() Map {
	c.mu.RLock()
	vars := c.vars
	c.mu.RUnlock()
	return vars
}

func localValue[T any](c *BaseCtx, key string) (T, bool) {
	var zero T
	val, ok := c.Get(key)
	if !ok || val == nil {
		return zero, false
	}
	value, ok := val.(T)
	return value, ok
}

func defaultValue[T any](fallback T, def []T) T {
	if len(def) > 0 {
		return def[0]
	}
	return fallback
}

// GetString returns the value associated with the key as a string.
func (c *BaseCtx) GetString(key string, def ...string) (value string) {
	if value, ok := localValue[string](c, key); ok {
		return value
	}
	return defaultValue("", def)
}

// GetBool returns the value associated with the key as a boolean.
func (c *BaseCtx) GetBool(key string) (value bool) {
	if val, ok := c.Get(key); ok && val != nil {

		switch v := val.(type) {
		case string:
			return v == "true"
		case bool:
			return v
		}
		return val.(bool)
	}
	return false
}

// GetInt returns the value associated with the key as an integer.
func (c *BaseCtx) GetInt(key string, def ...int) (i int) {
	if i, ok := localValue[int](c, key); ok {
		return i
	}
	return defaultValue(-1, def)
}

// GetInt64 returns the value associated with the key as an integer.
func (c *BaseCtx) GetInt64(key string, def ...int64) (i int64) {
	if i, ok := localValue[int64](c, key); ok {
		return i
	}
	return defaultValue(int64(-1), def)
}

// GetUint returns the value associated with the key as an integer.
func (c *BaseCtx) GetUint(key string, def ...uint) (i uint) {
	if i, ok := localValue[uint](c, key); ok {
		return i
	}
	return defaultValue(uint(0), def)
}

// GetUint64 returns the value associated with the key as an integer.
func (c *BaseCtx) GetUint64(key string, def ...uint64) (i uint64) {
	if i, ok := localValue[uint64](c, key); ok {
		return i
	}
	return defaultValue(uint64(0), def)
}

func (c *BaseCtx) GetUUID(key string, def ...UUID) (v UUID) {
	if val, ok := c.Get(key); ok && val != nil {
		if value, ok := val.(string); ok {
			if v, err := UUIDFromString(value); err == nil {
				return v
			}
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return NewUUID()
}

func (c *BaseCtx) GetXid(key string, def ...xid.ID) (v xid.ID) {
	if val, ok := c.Get(key); ok && val != nil {
		if value, ok := val.(string); ok {
			if v, err := xid.ParseString(value); err == nil {
				return v
			}
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return xid.ID(0)
}

func (c *BaseCtx) GetSid(key string, def ...sid.ID) (v sid.ID) {
	if val, ok := c.Get(key); ok && val != nil {
		switch value := val.(type) {
		case string:
			if v, err := sid.ParseString(value); err == nil {
				return v
			}
		case int64:
			return sid.ID(value)
		case sid.ID:
			return value
		default:
			Erro("invalid sid type: %T", value)
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return sid.ID(0)
}

// GetFloat64 returns the value associated with the key as a float64.
func (c *BaseCtx) GetFloat64(key string, def ...float64) (value float64) {
	if val, ok := c.Get(key); ok && val != nil {
		value, _ = val.(float64)
	}
	if len(def) > 0 {
		return def[0]
	}
	return
}

// GetTime returns the value associated with the key as time.
func (c *BaseCtx) GetTime(key string) (t time.Time) {
	if val, ok := c.Get(key); ok && val != nil {
		t, _ = val.(time.Time)
	}
	return
}

// GetDuration returns the value associated with the key as a duration.
func (c *BaseCtx) GetDuration(key string) (d time.Duration) {
	if val, ok := c.Get(key); ok && val != nil {
		d, _ = val.(time.Duration)
	}
	return
}

// GetStrings String Slice returns the value associated with the key as a slice of strings.
func (c *BaseCtx) GetStrings(key string, def ...[]string) (value []string) {
	if value, ok := localValue[[]string](c, key); ok {
		return value
	}
	return defaultValue(value, def)
}

// GetMap returns the value associated with the key as a map of interfaces.
//
//	> return map[string]any
func (c *BaseCtx) GetMap(key string, def ...map[string]any) (value map[string]any) {
	if value, ok := localValue[map[string]any](c, key); ok {
		return value
	}
	return defaultValue(value, def)
}

// GetMapString returns the value associated with the key as a map of strings.
//
//	> return map[string]string
func (c *BaseCtx) GetMapString(key string, def ...map[string]string) (value map[string]string) {
	if value, ok := localValue[map[string]string](c, key); ok {
		return value
	}
	return defaultValue(value, def)
}

// GetStringMapStringSlice returns the value associated with the key as a map to a slice of strings.
//
//	> return map[string][]string
func (c *BaseCtx) GetMapStringSlice(key string, def ...map[string][]string) (value map[string][]string) {
	if value, ok := localValue[map[string][]string](c, key); ok {
		return value
	}
	return defaultValue(value, def)
}

// GetAs retrieve struct like c.Get("user").(User)
//
//	> Experimental function, problem unknown
func (c *BaseCtx) GetAs(key string, v any) error {
	if val, ok := c.Get(key); ok && val != nil {
		rv := reflect.ValueOf(v)
		if rv.Kind() != reflect.Ptr || rv.IsNil() {
			return &InvalidUnmarshalError{reflect.TypeOf(v)}
		}
		rv = rv.Elem()
		rv.Set(reflect.ValueOf(val).Convert(rv.Type()))
		return nil
	}
	return ErrDataTypeNotSupport
}

func (c *BaseCtx) Redirect(to string, stCode ...int) error {
	code := StatusTemporaryRedirect
	if len(stCode) > 0 {
		code = stCode[0]
	}
	http.Redirect(c.W, c.R, to, code)
	return nil
}

func (c *BaseCtx) RedirectJS(to string, msg ...string) {
	c.SetHeader(HeaderContentType, MIMETextHTMLCharsetUTF8)
	if len(msg) > 0 {
		c.SendString("<script>alert('" + msg[0] + "');location.href='" + to + "';</script>")
	}
	c.SendString("<script>location.href='" + to + "'</script>")
}

// GetStatus get response statusCode
func (c *BaseCtx) GetStatus() int {
	return c.Response().Status()
}

// Flush response dat and break
func (c *BaseCtx) Flush(data any, statusCode ...int) error {
	c.Abort()
	if len(statusCode) > 0 {
		c.SendStatus(statusCode[0])
	}
	switch v := data.(type) {
	case string:
		return c.SendString(v)
	case []byte:
		return c.Send(v)
	}
	return c.JSON(data)
}

func (c *BaseCtx) JSONP(data any, callback ...string) error {
	raw, err := sonic.Marshal(data)
	if err != nil {
		return err
	}

	cb := "callback"
	if len(callback) > 0 {
		cb = callback[0]
	}

	result := fmt.Sprintf("%s(%s);", cb, string(raw))
	c.W.Header().Set(HeaderContentType, MIMEApplicationJavaScriptCharsetUTF8)
	return c.SendString(result)
}

// Send send []byte to client
func (c *BaseCtx) Send(buf []byte) error {
	_, err := c.W.Write(buf)
	return err
}

// FormValue Get query
//
//	key string
//	def string default val optional
//
// >  GET /?name=Jack&id=
//
//	`
//	  name := c.FormValue("name")  // name = Jack
//	  id := c.FormValue("id", "1") // id = 1 Because the default value is used
//	`
func (c *BaseCtx) Query(key string, def ...string) string {
	if val := c.Request().FormValue(key); val != "" {
		return val
	}
	return defaultString("", def)
}

func (c *BaseCtx) QueryInt(key string, def ...int) int {
	val := c.Query(key)
	if val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			return v
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return -1
}

// FormValue support old version
func (c *BaseCtx) FormValue(key string, def ...string) string {
	return c.Query(key, def...)
}

func (c *BaseCtx) FromValueInt(key string, def ...int) int {
	val := c.Query(key)
	if val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			return v
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return -1
}

func (c *BaseCtx) FromValueUid(key string, def ...uid.UID) uid.UID {
	val := c.Query(key)
	if val != "" {
		if v, err := uid.FromString(val); err == nil && !v.IsEmpty() {
			return v
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return uid.Nil
}
func (c *BaseCtx) FromValueXid(key string, def ...xid.ID) xid.ID {
	val := c.Query(key)
	if val != "" {
		if v, err := xid.ParseString(val); err == nil && !v.IsZero() {
			return v
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return xid.ID(0)
}

func (c *BaseCtx) FromValueUUID(key string, def ...UUID) UUID {
	val := c.Query(key)
	if val != "" {
		if v, err := UUIDFromString(val); err == nil {
			return v
		}
	}

	if len(def) > 0 {
		return def[0]
	}
	return UuidNil
}

// FormValues returns a slice of strings for a given query key.
func (c *BaseCtx) FormValues(key string, def ...[]string) []string {
	return c.Querys(key, def...)
}
func (c *BaseCtx) Querys(key string, def ...[]string) []string {
	if c.querys == nil {
		c.querys = c.R.URL.Query()
	}
	if val, ok := c.querys[key]; ok {
		return val
	}
	if len(def) > 0 {
		return def[0]
	}
	return make([]string, 0)
}

// Vary add the given header field to the vary response header
//
// c.Vary("Accept-Encoding", "Accept", "X-Requested-With")
//
// Response Header:
//
//	Vary: Accept-Encoding, Accept, X-Requested-With
func (c *BaseCtx) Vary(fields ...string) Ctx {
	c.Append(HeaderVary, fields...)
	return c
}

// FileAttachment writes the specified file into the body stream in an efficient way
// On the client side, the file will typically be downloaded with the given filename
func (c *BaseCtx) FileAttachment(filepath, filename string) {
	c.SetHeader(HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeFile(c.W, c.R, filepath)
}

// File implements Ctx.
func (c *BaseCtx) File(filePath string) {
	http.ServeFile(c.Response(), c.Request(), filePath)
}

// FileFromFS writes the specified file from http.FileSystem into the body stream in an efficient way.
func (c *BaseCtx) FileFromFS(filepath string, fs http.FileSystem) {
	defer func(old string) {
		c.R.URL.Path = old
	}(c.R.URL.Path)

	c.R.URL.Path = filepath

	http.FileServer(fs).ServeHTTP(c.W, c.R)
}

// FormFile returns the first file for the provided form key.
// FormFile calls ParseMultipartForm and ParseForm if necessary.
func (c *BaseCtx) FormFile(key string) (*multipart.FileHeader, error) {
	if c.Request().MultipartForm == nil {
		if err := c.Request().ParseMultipartForm(c.app.MaxMultipartMemory); err != nil {
			return nil, err
		}
	}
	f, fh, err := c.Request().FormFile(key)
	if err != nil {
		return nil, err
	}
	f.Close()
	return fh, err
}

// SaveFile upload file save to a folder
//
//	path = {root}/{dst}/{id}
//	@param
//	name string filename
//	dst string dst path
//	root string root path optional
//	id   path optional type uid.UID, int, uint, int64,uint64
//	rename bool optional
//	return relPath, absPath
//
//	   c.SaveFile("file", "/images")
//	   (string) relpath "/images/10/favicon.png"
//	   (string) abspath "/images/10/favicon.png"
//
//	   c.SaveFile("file", "/images", "./static")
//	   (string) relpath "/images/10/5hsbkthaadld/favicon.png"
//	   (string) abspath "/static/images/10/5hsbkthaadld/favicon.png"
//
//	   c.SaveFile("file", "/images", "./static", uid.New())
//	   (string) relpath "/images/10/5hsbkthaadld/5hsbkthaadld.png"
//	   (string) abspath "/static/images/10/5hsbkthaadld/5hsbkthaadld.png"
//	              👇file    👇dst      👇root     👇id      👇rename
//	   c.SaveFile("file", "/images", "./static", uid.New(), true)
//	   (string) relpath "/images/10/5hsbkthaadld/5hsbkthaadld.png"
//	   (string) abspath "/static/images/10/5hsbkthaadld/5hsbkthaadld.png"
func (c *BaseCtx) SaveFile(key, dst string, args ...any) (relpath, abspath string, err error) {
	file, err := c.FormFile(key)
	if err != nil {
		return "", "", err
	}
	src, err := file.Open()
	if err != nil {
		return "", "", err
	}
	defer src.Close()
	relpath, abspath, err = MakePath(file.Filename, dst, args...)
	if err != nil {
		return "", "", err
	}
	out, err := os.Create(abspath)
	if err != nil {
		return relpath, abspath, err
	}
	defer out.Close()
	_, err = io.Copy(out, src)
	return relpath, abspath, err
}

// SaveFiles like SaveFile
//
// @params
// key string MultipartForm key name : files
// dst string static to ./static
// more args see SaveFile
//
//	rel, err := c.SaveFiles("files", "static", true)
//
// return relative url path
// rel is
//
//	return []string {
//		"/static/09/wjejwifx.jpg",
//		"/static/09/wjejwifx.jpg",
//		"/static/09/wjejwifx.jpg",
//	}
func (c *BaseCtx) SaveFiles(key, dst string, args ...any) (rel Array, err error) {
	err = c.Request().ParseMultipartForm(c.app.MaxMultipartMemory)
	if err != nil {
		return
	}
	rel = make(Array, 0)
	files := c.Request().MultipartForm.File[key]
	for _, v := range files {
		src, err := v.Open()
		if err != nil {
			return rel, err
		}
		defer src.Close()
		relpath, abspath, err := MakePath(v.Filename, dst, args...)
		if err != nil {
			return rel, err
		}
		out, err := os.Create(abspath)
		if err != nil {
			return rel, err
		}
		defer out.Close()
		if _, err = io.Copy(out, src); err != nil {
			return rel, err
		}
		rel = append(rel, "/"+relpath)
	}
	return
}

type RestfulDefine struct {
	Data    string
	Status  string
	Message string
	Code    any
}

// Core implements Ctx.
func (c *BaseCtx) Core() *Core {
	return c.app
}

// Append values to the same key, separated by commas
//
// c.Append("Vary", "Accept-Encoding", "Accept", "X-Requested-With")
//
// Response Header:
//
//	Vary: Accept-Encoding, Accept, X-Requested-With
func (c *BaseCtx) Append(key string, values ...string) Ctx {
	if len(values) == 0 {
		return c
	}
	h := c.Response().Header().Get(key)
	vals := make([]string, 0)
	if len(h) > 0 {
		vals = append(vals, h)
	}
	vals = append(vals, values...)
	value := strings.Join(vals, ",")
	if h != value {
		c.Response().Header().Add(key, value)
	}
	return c
}

func (c *BaseCtx) Abort(args ...any) Ctx {
	for _, arg := range args {
		switch a := arg.(type) {
		case string:
			c.SendString(a)
		case int:
			c.SendStatus(a)
		default:
			c.JSON(a)
		}
	}
	return c
}

func (c *BaseCtx) StartAt(t ...time.Time) time.Time {
	if len(t) > 0 {
		c.startAt = t[0]
	}
	return c.startAt
}
func (c *BaseCtx) JSON(data any, code ...int) error {

	raw, err := sonic.Marshal(data)
	if err != nil {
		return err
	}
	if len(code) > 0 {
		c.W.WriteHeader(code[0])
	}
	c.W.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
	_, err = c.W.Write(raw)
	return err
}

func (c *BaseCtx) ToJSONCode(data any, msg ...any) error {
	dat := Map{}
	dat[c.respJsonKeys.Data] = data
	dat["code"] = c.respJsonKeys.Code
	for _, v := range msg {
		switch d := v.(type) {
		case int, int32, int16, int8:
			dat["code"] = d
		case string:
			dat[c.respJsonKeys.Message] = d
		case Errors:
			eCode, eMsg := d.Errors()
			dat["code"] = eCode
			dat[c.respJsonKeys.Message] = eMsg
		case error:
			dat[c.respJsonKeys.Message] = d.Error()
		}
	}
	return c.JSON(dat)
}
func (c *BaseCtx) ToJSON(data any, msg ...any) error {
	dat := Map{}
	dat[c.respJsonKeys.Data] = data
	dat[c.respJsonKeys.Message] = "ok"
	dat[c.respJsonKeys.Status] = true
	for _, v := range msg {
		switch d := v.(type) {
		case int, int32, int16, int8:
			dat[c.respJsonKeys.Status] = d
		case string:
			dat[c.respJsonKeys.Message] = d
		case error:
			dat[c.respJsonKeys.Status] = false
			dat[c.respJsonKeys.Message] = d.Error()
		case Error:
			dat[c.respJsonKeys.Status] = int(d.status.Code())
			dat[c.respJsonKeys.Message] = d.status.Message()
		}
	}
	return c.JSON(dat)
}

func NormalizeHeaders(h http.Header) {
	for k, v := range h {
		ck := http.CanonicalHeaderKey(k)

		if ck != k {
			h.Del(k)
			h[ck] = v
		}
	}
}

func (c *BaseCtx) init(app *Core, w http.ResponseWriter, r *http.Request) {
	NormalizeHeaders(r.Header)
	c.R = r
	c.wm.init(w)
	c.W = &c.wm
	c.path = r.URL.Path
	c.ctx = r.Context()
	c.app = app
	c.indexRoute = -1
	c.indexHandler = 0
	c.matched = false
	c.baseURI = ""
	c.method = c.R.Method
	c.pathOriginal = r.URL.RawPath
	c.methodInt = MethodType(methodPos(c.method))
	c.respJsonKeys = &app.defaultRestful
	c.querys = nil
	c.vars = nil
}
func (c *BaseCtx) release() {
	const maxRetainedHandlerCapacity = 64
	if cap(c.handlers) > maxRetainedHandlerCapacity {
		c.handlers = nil
	} else {
		clear(c.handlers[:cap(c.handlers)])
		c.handlers = c.handlers[:0]
	}
	c.R = nil
	c.W = nil
	c.wm.ResponseWriter = nil
	c.app = nil
	c.ctx = nil
	c.querys = nil
	c.vars = nil
	c.path = ""
	c.pathOriginal = ""
	c.method = ""
	c.baseURI = ""
	c.theme = ""
	c.respJsonKeys = nil
	utils.ClearMap(c.params)
}

func (c *BaseCtx) Method() string {
	return c.method
}

func (c *BaseCtx) Path() string {
	return c.path
}
func (c *BaseCtx) ParamsMaps() map[string]string {
	return c.params
}
func (c *BaseCtx) Params(key string, defaultValue ...string) string {
	if val, ok := c.params[key]; ok {
		return val
	}
	return defaultString("", defaultValue)
}
func (c *BaseCtx) Param(key string, defaultValue ...string) string {
	return c.Params(key, defaultValue...)
}

// ParamsUid get uid.UID param, return uid.Nil if failed
func (c *BaseCtx) ParamsUid(key string, defaultValue ...uid.UID) (uid.UID, error) {
	value, err := uid.FromString(c.Params(key))
	if err != nil {
		if len(defaultValue) > 0 {
			return defaultValue[0], nil
		}
		return uid.Nil, fmt.Errorf("failed to convert: %w", err)
	}
	return value, nil
}

// ParamsUid get uid.UID param, return uid.Nil if failed
func (c *BaseCtx) ParamsUuid(key string, defaultValue ...UUID) (UUID, error) {
	value, err := UUIDFromString(c.Params(key))
	if err != nil {
		if len(defaultValue) > 0 {
			return defaultValue[0], nil
		}
		return UuidNil, fmt.Errorf("failed to convert: %w", err)
	}
	return value, nil
}

func (c *BaseCtx) ParamUUID(key string, defaultValue ...UUID) UUID {
	ret, _ := c.ParamsUuid(key, defaultValue...)
	return ret
}

func (c *BaseCtx) GetParamSid(key string, defaultValue ...sid.ID) (sid.ID, error) {
	return c.ParamsSid(key, defaultValue...)
}

func (c *BaseCtx) ParamsSid(key string, defaultValue ...sid.ID) (sid.ID, error) {
	value, err := sid.ParseString(c.Params(key))
	if err != nil {
		if len(defaultValue) > 0 {
			return defaultValue[0], nil
		}
		return 0, fmt.Errorf("failed to convert: %w", err)
	}
	return value, nil
}

func (c *BaseCtx) ParamsXid(key string, defaultValue ...xid.ID) (xid.ID, error) {
	value, err := xid.ParseString(c.Params(key))
	if err != nil {
		if len(defaultValue) > 0 {
			return defaultValue[0], nil
		}
		return 0, fmt.Errorf("failed to convert: %w", err)
	}
	return value, nil
}

// ParamsInt get int param, return -1 if failed
func (c *BaseCtx) ParamsInt(key string, defaultValue ...int) (int, error) {
	value, err := strconv.Atoi(c.Params(key))
	if err != nil {
		if len(defaultValue) > 0 {
			return defaultValue[0], nil
		}
		return -1, fmt.Errorf("failed to convert: %w", err)
	}
	return value, nil
}

// GetParamInt get int param, return -1 if failed
func (c *BaseCtx) GetParamInt(key string, defaultValue ...int) (int, error) {
	return c.ParamsInt(key, defaultValue...)
}

// GetParamUid get uid.UID param, return uid.Nil if failed
func (c *BaseCtx) GetParamUid(key string, defaultValue ...uid.UID) (uid.UID, error) {
	return c.ParamsUid(key, defaultValue...)
}

func (c *BaseCtx) SetHeader(key string, value string) {
	if value == "" {
		c.W.Header().Del(key)
		return
	}
	c.W.Header().Set(key, value)
}

// GetHeader get Request header
func (c *BaseCtx) GetHeader(key string, defaultValue ...string) string {
	return defaultString(c.R.Header.Get(key), defaultValue)
}

func (c *BaseCtx) SendStatus(code int, msg ...string) error {
	c.Status(code)
	if len(msg) > 0 {
		return c.SendString(msg[0])
	}
	return nil
}

func (c *BaseCtx) SendString(str ...any) error {
	// c.SetHeader(HeaderContentType, MIMETextPlainCharsetUTF8)
	buf := ""
	if len(str) == 1 {
		buf = fmt.Sprint(str...)
	} else if len(str) > 1 {
		if strings.Contains(str[0].(string), "%") {
			buf = fmt.Sprintf(str[0].(string), str[1:]...)
		} else {
			buf = fmt.Sprint(str...)
		}
	}
	_, err := c.W.WriteString(buf)
	return err
}

func (c *BaseCtx) Status(code int) Ctx {
	c.W.WriteHeader(code)
	return c
}

// Request implements Ctx.
func (c *BaseCtx) Request() *http.Request {
	return c.R
}

// ResponseWriter implements Ctx.
func (c *BaseCtx) Response() ResponseWriter {
	return c.W
}

func (app *Core) AcquireCtx(w http.ResponseWriter, r *http.Request) *BaseCtx {
	ctx := app.pool.Get()
	ctx.init(app, w, r)
	return ctx
}

func (app *Core) ReleaseCtx(c Ctx) {
	c.release()
	app.pool.Put(c.(*BaseCtx))
}

func (c *BaseCtx) Next() error {
	// Increment handler index
	c.indexHandler++
	// Did we executed all route handlers?
	if c.indexHandler < len(c.handlers) {
		return c.handlers[c.indexHandler](c)
	}
	return nil
}

// Accepts checks if the specified extensions or content types are acceptable.
func (c *BaseCtx) Accepts(offers ...string) string {
	if len(offers) == 0 {
		return ""
	}
	header := c.GetHeader(HeaderAccept)
	if header == "" {
		return offers[0]
	}

	spec, commaPos := "", 0
	for len(header) > 0 && commaPos != -1 {
		commaPos = strings.IndexByte(header, ',')
		if commaPos != -1 {
			spec = strings.TrimLeft(header[:commaPos], " ")
		} else {
			spec = strings.TrimLeft(header, " ")
		}
		if factorSign := strings.IndexByte(spec, ';'); factorSign != -1 {
			spec = spec[:factorSign]
		}

		var mimetype string
		for _, offer := range offers {
			if len(offer) == 0 {
				continue
				// Accept: */*
			} else if spec == "*/*" {
				return offer
			}

			if strings.IndexByte(offer, '/') != -1 {
				mimetype = offer // MIME type
			} else {
				mimetype = MIME(offer) // extension
			}

			if spec == mimetype {
				// Accept: <MIME_type>/<MIME_subtype>
				return offer
			}

			s := strings.IndexByte(mimetype, '/')
			// Accept: <MIME_type>/*
			if strings.HasPrefix(spec, mimetype[:s]) && (spec[s:] == "/*" || mimetype[s:] == "/*") {
				return offer
			}
		}
		if commaPos != -1 {
			header = header[commaPos+1:]
		}
	}

	return ""
}

// AcceptsCharsets checks if the specified charset is acceptable.
func (c *BaseCtx) AcceptsCharsets(offers ...string) string {
	return getOffer(c.GetHeader(HeaderAcceptCharset), offers...)
}

// AcceptsEncodings checks if the specified encoding is acceptable.
func (c *BaseCtx) AcceptsEncodings(offers ...string) string {
	return getOffer(c.GetHeader(HeaderAcceptEncoding), offers...)
}

// AcceptsLanguages checks if the specified language is acceptable.
func (c *BaseCtx) AcceptsLanguages(offers ...string) string {
	return getOffer(c.GetHeader(HeaderAcceptLanguage), offers...)
}

// Type sets the Content-Type HTTP header to the MIME type specified by the file extension.
func (c *BaseCtx) Type(extension string, charset ...string) Ctx {
	if len(charset) > 0 {
		c.SetHeader(HeaderContentType, MIME(extension)+"; charset="+charset[0])
	} else {

		c.SetHeader(HeaderContentType, MIME(extension))
	}
	return c
}

// Format performs content-negotiation on the Accept HTTP header.
// It uses Accepts to select a proper format.
// If the header is not specified or there is no proper format, text/plain is used.
func (c *BaseCtx) Format(body any) error {
	// Get accepted content type
	accept := c.Accepts("html", "json", "txt", "xml")
	// Set accepted content type
	c.Type(accept, CharsetUTF8)
	// Type convert provided body
	var b string
	switch val := body.(type) {
	case string:
		b = val
	case []byte:
		b = string(val)
	default:
		b = fmt.Sprintf("%v", val)
	}

	// Format based on the accept content type
	switch accept {
	case "html":
		return c.SendString("<p>" + b + "</p>")
	case "json":
		return c.JSON(body)
	case "txt":
		return c.SendString(b)
	case "xml":
		return c.XML(body)
	}
	return c.SendString(b)
}

// XML converts any interface or string to XML.
// This method also sets the content header to application/xml.
func (c *BaseCtx) XML(data any) error {
	raw, err := xml.Marshal(data)
	if err != nil {
		return err
	}
	c.SetHeader(HeaderContentType, MIMEApplicationXML)
	c.Send(raw)
	return nil
}

// ResponseWriter ...
type ResponseWriter interface {
	http.ResponseWriter
	http.Hijacker
	http.Flusher

	// Returns the HTTP response status code of the current request.
	Status() int

	// Returns the number of bytes already written into the response http body.
	// See Written()
	Size() int

	// Writes the string into the response body.
	WriteString(string) (int, error)

	// Returns true if the response body was already written.
	Written() bool

	// Forces to write the http header (status code + headers).
	DoWriteHeader()

	// get the http.Pusher for server push
	Pusher() http.Pusher
}

type resp struct {
	http.ResponseWriter
	size   int
	status int
}

func (w *resp) init(writer http.ResponseWriter) {
	w.ResponseWriter = writer
	w.size = -1
	w.status = StatusOK
}

func (w *resp) DoWriteHeader() {
	if !w.Written() {
		w.size = 0
		w.ResponseWriter.WriteHeader(w.status)
	}
}

func (w *resp) Write(data []byte) (n int, err error) {
	w.DoWriteHeader()
	n, err = w.ResponseWriter.Write(data)
	w.size += n
	return
}

func (w *resp) WriteString(s string) (n int, err error) {
	w.DoWriteHeader()
	n, err = io.WriteString(w.ResponseWriter, s)
	w.size += n
	return
}

func (w *resp) Status() int {
	return w.status
}

func (w *resp) Size() int {
	return w.size
}

func (w *resp) Written() bool {
	return w.size != -1
}

// Hijack implements the http.Hijacker interface.
func (w *resp) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.size < 0 {
		w.size = 0
	}
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

// Flush implements the http.Flush interface.
func (w *resp) Flush() {
	w.DoWriteHeader()
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w *resp) Pusher() (pusher http.Pusher) {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher
	}
	return nil
}

func (w *resp) WriteHeader(code int) {
	if code > 0 && w.status != code {
		if w.Written() {
			// Warn("headers were already written. Wanted to override status code %d with %d", w.status, code)
			return
		}
		w.status = code
	}
}
