package core

import (
	"bufio"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gorilla/schema"
	"github.com/xs23933/uid"
)

type Ctx interface {
	Response() ResponseWriter                                                   // Response() return http.ResponseWriter
	Request() *http.Request                                                     // Request() return *http.Request
	RedirectJS(to string, msg ...string)                                        // use js redirect
	Redirect(to string, stCode ...int)                                          // base redirect
	RemoteIP() net.IP                                                           // remote client ip
	SetCookie(name, value string, exp time.Time, path string, args ...any)      // set cookie
	RemoveCookie(name, path string, dom ...string)                              // remove some cookie
	Cookie(cookie *http.Cookie)                                                 // set cookie with cookie object
	Cookies(name string) (string, error)                                        // get some cookie
	ReadBody(out any) error                                                     // read put post any request body to struct or map
	Next() error                                                                // next HandlerFunc
	Path() string                                                               // return http.Request.URI.path
	init(*Core, http.ResponseWriter, *http.Request)                             // Core call
	release()                                                                   // Core called
	Send(buf []byte) error                                                      // send []byte data
	SendString(msg ...any) error                                                // send string to body
	SendStatus(code int, msg ...string) error                                   // send status to client, options msg with display
	SetHeader(key string, value string)                                         // set response header
	GetHeader(key string, defaultValue ...string) string                        // get request header
	Method() string                                                             // return method e.g: GET,POST,PUT,DELETE,OPTION,HEAD...
	GetStatus() int                                                             // get response status
	Status(code int) Ctx                                                        // set response status
	Core() *Core                                                                // return app(*Core)
	Abort(args ...any) Ctx                                                      // Deprecated: As of v2.0.0, this function simply calls Ctx.Format.
	JSON(any) error                                                             // send json
	JSONP(data any, callback ...string) error                                   // send jsonp
	ToJSON(data any, msg ...any) error                                          // send json with status
	ToJSONCode(data any, msg ...any) error                                      // send have code to json
	StartAt(t ...time.Time) time.Time                                           // set ctx start time if t set, else get start at
	Params(key string, defaultValue ...string) string                           // get Param data e.g c.Param("param")
	ParamsUid(key string, defaultValue ...uid.UID) (uid.UID, error)             // get Param UID type, return uid.Nil if failed
	ParamsUuid(key string, defaultValue ...UUID) (UUID, error)                  // get Param UID type, return uid.Nil if failed
	ParamsInt(key string, defaultValue ...int) (int, error)                     // get Param int type, return -1 if failed
	GetParamUid(key string, defaultValue ...uid.UID) (uid.UID, error)           // get param uid.UID, return uid.Nil if failed
	GetParamInt(key string, defaultValue ...int) (int, error)                   // get param int, return -1 if failed
	File(filePath string)                                                       // send file
	FileAttachment(filepath, filename string)                                   // send file attachment
	FileFromFS(filePath string, fs http.FileSystem)                             // send file from FS
	Append(key string, values ...string) Ctx                                    // append response header
	Vary(fields ...string) Ctx                                                  // set response vary
	SaveFile(key, dst string, args ...any) (relpath, abspath string, err error) // upload some one file
	SaveFiles(key, dst string, args ...any) (rel Array, err error)              // upload multi-file
	Query(key string, def ...string) string                                     // get request query string like ?id=12345
	Querys(key string, def ...[]string) []string                                // like query, but return []string values
	FormValue(key string, def ...string) string                                 // like Query support old version
	FromValueInt(key string, def ...int) int                                    // parse form value to int
	FromValueUid(key string, def ...uid.UID) uid.UID                            // parse form value to uid
	FromValueUUID(key string, def ...UUID) UUID                                 // parse form value to uuid
	FormValues(key string, def ...[]string) []string                            // like Querys
	Flush(data any, statusCode ...int) error                                    // flush
	Accepts(offers ...string) string                                            // Accepts checks if the specified extensions or content types are acceptable.
	AcceptsCharsets(offers ...string) string                                    // AcceptsCharsets checks if the specified charset is acceptable.
	AcceptsEncodings(offers ...string) string                                   // AcceptsEncodings checks if the specified encoding is acceptable.
	AcceptsLanguages(offers ...string) string                                   // AcceptsLanguages checks if the specified language is acceptable.
	Format(body any) error                                                      // Format performs content-negotiation on the Accept HTTP header. It uses Accepts to select a proper format. If the header is not specified or there is no proper format, text/plain is used.
	Type(extension string, charset ...string) Ctx                               // 发送 response content-type
	XML(data any) error                                                         // output xml
	Set(key string, val any)
	Get(key string) (val any, ok bool)
	GetString(key string, def ...string) (value string)
	GetBool(key string) (value bool)
	GetInt(key string, def ...int) (i int)
	GetInt64(key string, def ...int64) (i int64)
	GetUint(key string, def ...uint) (i uint)
	GetUint64(key string, def ...uint64) (i uint64)
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
	Render(f string, bind ...any) error
}

type BaseCtx struct {
	wm            resp
	app           *Core  // Reference to *App
	route         *Route // Reference to *Route
	indexRoute    int    // Index of the current route
	indexHandler  int    // Index of the current handler
	method        string // HTTP method
	methodInt     MethodType
	baseURI       string
	treePath      string            // Path for the search in the tree
	detectionPath string            // Route detection path                                  -> string copy from detectionPathBuffer
	path          string            // HTTP path with the modifications by the configuration -> string copy from pathBuffer
	pathOriginal  string            // Original HTTP path
	values        [maxParams]string // Route parameter values
	matched       bool              // Non use route matched
	theme         string
	W             ResponseWriter
	R             *http.Request
	ctx           context.Context
	vars          Map
	querys        url.Values
	startAt       time.Time
	respJsonKeys  *RestfulDefine
	mu            sync.RWMutex
}

// decoderPool helps to improve ReadBody's and QueryParser's performance
var decoderPool = &sync.Pool{New: func() any {
	var decoder = schema.NewDecoder()
	decoder.IgnoreUnknownKeys(true)
	return decoder
}}

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

// ReadBody binds the request body to a struct.
// It supports decoding the following content types based on the Content-Type header:
// application/json, application/xml, application/x-www-form-urlencoded, multipart/form-data
// If none of the content types above are matched, it will return a ErrUnprocessableEntity error
//
//	out any MIMEApplicationForm MIMEMultipartForm MIMETextXML must struct
func (c *BaseCtx) ReadBody(out any) error {
	// Get decoder from pool
	schemaDecoder := decoderPool.Get().(*schema.Decoder)
	defer decoderPool.Put(schemaDecoder)

	// Get content-type
	ctype := strings.ToLower(c.R.Header.Get(HeaderContentType))

	switch {
	case strings.HasPrefix(ctype, MIMEApplicationJSON):
		schemaDecoder.SetAliasTag("json")
		body, err := io.ReadAll(c.R.Body)
		if err != nil {
			return err
		}
		return sonic.Unmarshal(body, out)
	case strings.HasPrefix(ctype, MIMEApplicationForm):
		schemaDecoder.SetAliasTag("form")
		if err := c.R.ParseForm(); err != nil {
			return err
		}
		return schemaDecoder.Decode(out, c.R.PostForm)
	case strings.HasPrefix(ctype, MIMEMultipartForm):
		schemaDecoder.SetAliasTag("form")
		if err := c.R.ParseMultipartForm(1048576); err != nil {
			return nil
		}
		return schemaDecoder.Decode(out, c.R.MultipartForm.Value)
	case strings.HasPrefix(ctype, MIMETextXML), strings.HasPrefix(ctype, MIMEApplicationXML):
		schemaDecoder.SetAliasTag("xml")
		body, err := io.ReadAll(c.R.Body)
		if err != nil {
			return err
		}
		return xml.Unmarshal(body, out)
	}
	// No suitable content type found
	return ErrUnprocessableEntity
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
	remote := c.GetHeader("X-Forwarded-For")
	remoteIP := net.ParseIP(remote)
	if remoteIP != nil {
		return remoteIP
	}
	remote = c.GetHeader("X-Real-IP")
	remoteIP = net.ParseIP(remote)
	if remoteIP != nil {
		return remoteIP
	}
	ip, _, err := net.SplitHostPort(strings.TrimSpace(c.R.RemoteAddr))
	if err != nil {
		return nil
	}
	remoteIP = net.ParseIP(ip)
	if remoteIP == nil {
		return nil
	}

	return remoteIP
}

// set locals var
func (c *BaseCtx) Set(key string, val any) {
	c.mu.Lock()
	c.vars[key] = val
	c.mu.Unlock()
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

// GetString returns the value associated with the key as a string.
func (c *BaseCtx) GetString(key string, def ...string) (value string) {
	if val, ok := c.Get(key); ok && val != nil {
		if value, ok = val.(string); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

// GetBool returns the value associated with the key as a boolean.
func (c *BaseCtx) GetBool(key string) (value bool) {
	if val, ok := c.Get(key); ok && val != nil {
		if value, ok = val.(bool); ok {
			return
		}
	}
	return false
}

// GetInt returns the value associated with the key as an integer.
func (c *BaseCtx) GetInt(key string, def ...int) (i int) {
	if val, ok := c.Get(key); ok && val != nil {
		if i, ok = val.(int); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return -1
}

// GetInt64 returns the value associated with the key as an integer.
func (c *BaseCtx) GetInt64(key string, def ...int64) (i int64) {
	if val, ok := c.Get(key); ok && val != nil {
		if i, ok = val.(int64); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return 01
}

// GetUint returns the value associated with the key as an integer.
func (c *BaseCtx) GetUint(key string, def ...uint) (i uint) {
	if val, ok := c.Get(key); ok && val != nil {
		if i, ok = val.(uint); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return
}

// GetUint64 returns the value associated with the key as an integer.
func (c *BaseCtx) GetUint64(key string, def ...uint64) (i uint64) {
	if val, ok := c.Get(key); ok && val != nil {
		if i, ok = val.(uint64); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return
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
	if val, ok := c.Get(key); ok && val != nil {
		if value, ok = val.([]string); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return
}

// GetMap returns the value associated with the key as a map of interfaces.
//
//	> return map[string]any
func (c *BaseCtx) GetMap(key string, def ...map[string]any) (value map[string]any) {
	if val, ok := c.Get(key); ok && val != nil {
		if value, ok = val.(map[string]any); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return
}

// GetMapString returns the value associated with the key as a map of strings.
//
//	> return map[string]string
func (c *BaseCtx) GetMapString(key string, def ...map[string]string) (value map[string]string) {
	if val, ok := c.Get(key); ok && val != nil {
		if value, ok = val.(map[string]string); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return
}

// GetStringMapStringSlice returns the value associated with the key as a map to a slice of strings.
//
//	> return map[string][]string
func (c *BaseCtx) GetMapStringSlice(key string, def ...map[string][]string) (value map[string][]string) {
	if val, ok := c.Get(key); ok && val != nil {
		if value, ok = val.(map[string][]string); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return
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

func (c *BaseCtx) Redirect(to string, stCode ...int) {
	code := StatusTemporaryRedirect
	if len(stCode) > 0 {
		code = stCode[0]
	}
	http.Redirect(c.W, c.R, to, code)
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
func (c *BaseCtx) JSON(data any) error {
	raw, err := sonic.Marshal(data)
	if err != nil {
		return err
	}
	c.W.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
	_, err = c.W.Write(raw)
	return err
}

func (c *BaseCtx) ToJSONCode(data any, msg ...any) error {
	dat := Map{}
	dat[c.respJsonKeys.Data] = data
	dat[c.respJsonKeys.Status] = c.respJsonKeys.Code
	for _, v := range msg {
		switch d := v.(type) {
		case int, int32, int16, int8:
			dat[c.respJsonKeys.Status] = d
		case string:
			dat[c.respJsonKeys.Message] = d
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
			dat[c.respJsonKeys.Status] = d.Code
			dat[c.respJsonKeys.Message] = d.Message
		}
	}
	return c.JSON(dat)
}

func (c *BaseCtx) init(app *Core, w http.ResponseWriter, r *http.Request) {
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
	c.querys = c.R.URL.Query()
	c.detectionPath = c.path
	c.respJsonKeys = &app.defaultRestful
	c.vars = make(Map)
	if !app.Conf.GetBool("case-sensitive", true) {
		c.detectionPath = strings.ToLower(c.detectionPath)
	}
	if !app.Conf.GetBool("strict-routing", true) && len(c.detectionPath) > 1 && c.detectionPath[len(c.detectionPath)-1] == '/' {
		c.detectionPath = strings.TrimRight(c.detectionPath, "/")
	}
	c.treePath = c.treePath[0:0]
	if len(c.detectionPath) >= 3 {
		c.treePath = c.detectionPath[:3]
	}
}
func (c *BaseCtx) release() {
	c.route = nil
	c.ctx = nil
	c.vars = Map{}
}

func (c *BaseCtx) Method() string {
	return c.method
}

func (c *BaseCtx) Path() string {
	return c.path
}

func (c *BaseCtx) Params(key string, defaultValue ...string) string {
	if key == "*" || key == "+" {
		key += "1"
	}

	for i := range c.route.Params {
		if len(key) != len(c.route.Params[i]) {
			continue
		}
		if c.route.Params[i] == key || (!c.app.Conf.GetBool("case-sensitive", true) && EqualFold(c.route.Params[i], key)) {
			if len(c.values) <= i || len(c.values[i]) == 0 {
				break
			}
			return c.values[i]
		}
	}
	return defaultString("", defaultValue)
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
	return defaultString(c.Request().Header.Get(key), defaultValue)
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
		buf = fmt.Sprintf(str[0].(string), str[1:]...)
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

func (app *Core) AcquireCtx(w http.ResponseWriter, r *http.Request) Ctx {
	ctx, ok := app.pool.Get().(Ctx)
	if !ok {
		panic(fmt.Errorf("failed to type-assert to Ctx"))
	}
	ctx.init(app, w, r)
	return ctx
}

func (app *Core) ReleaseCtx(c Ctx) {
	c.release()
	app.pool.Put(c)
}

func (c *BaseCtx) Next() error {
	// Increment handler index
	c.indexHandler++
	var err error
	// Did we executed all route handlers?
	if c.indexHandler < len(c.route.Handlers) {
		// Continue route stack
		err = c.route.Handlers[c.indexHandler](c)
	} else {
		_, err = c.app.next(c)
	}
	return err
}

func (c *BaseCtx) getValues() *[maxParams]string {
	return &c.values
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
