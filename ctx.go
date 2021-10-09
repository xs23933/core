package core

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/schema"
)

type Ctx struct {
	wm resp
	context.Context
	w        ResponseWriter
	r        *http.Request
	core     *Core
	Config   *config
	params   params
	path     string
	idx      int8
	done     bool
	mu       sync.RWMutex
	vars     map[string]interface{}
	querys   url.Values
	sameSite http.SameSite
	handlers HandlerFuncs
}

func (c *Ctx) init(w http.ResponseWriter, r *http.Request, core *Core) {
	c.r = r
	c.wm.init(w)
	c.w = &c.wm
	c.path = r.URL.Path
	c.Context = r.Context()
	c.params = make(params, 0)
	c.idx = -1
	c.handlers = nil
	c.core = core
	c.sameSite = http.SameSiteDefaultMode
	c.vars = make(map[string]interface{})
	c.querys = c.r.URL.Query()
}

func (c *Ctx) Next() {
	c.idx++
	for c.idx < int8(len(c.handlers)) {
		c.handlers[c.idx](c)
		c.idx++
	}
}

func (c *Ctx) Abort(args ...interface{}) {
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
	c.idx = abortIdx
}

func (c *Ctx) Core() *Core {
	return c.core
}

// GetStatus get response statusCode
func (c *Ctx) GetStatus() int {
	return c.w.Status()
}

// Flush response dat and break
func (c *Ctx) Flush(data interface{}, statusCode ...int) error {
	c.Final()
	if len(statusCode) > 0 {
		c.SendStatus(statusCode[0])
	}
	switch v := data.(type) {
	case string:
		return c.SendString(v)
	case []byte:
		return c.Send(v)
		// default:
		// switch reflect.TypeOf(v).Kind() {
		// case reflect.Struct, reflect.Map, reflect.Slice:
		// return c.JSON(v)
		// }
	}
	return c.JSON(data)
	// return ErrDataTypeNotSupport
}

func (c *Ctx) FinalToJSON(data interface{}, err error) error {
	return c.Final().ToJSON(data, err)
}

func (c *Ctx) FinalJSON(data interface{}) error {
	return c.Final().JSON(data)
}

func (c *Ctx) Final() *Ctx {
	c.done = true
	return c
}

// SetParam set param
func (c *Ctx) SetParam(k, v string) {
	for i, p := range c.params {
		if p.key == k {
			c.params[i].value = v
			return
		}
	}
	c.params = append(c.params, &param{key: k, value: v})
}

// Params All params
func (c *Ctx) Params() params {
	return c.params
}

// GetParam get param
func (c *Ctx) GetParam(k string, def ...string) string {
	for _, v := range c.params {
		if v.key == k {
			return v.value
		}
	}

	if len(def) > 0 {
		return def[0]
	}
	return ""
}

func (c *Ctx) JSON(data interface{}) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	c.w.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
	_, err = c.w.Write(raw)
	return err
}

// SetHeader set response header
func (c *Ctx) SetHeader(key, value string) {
	if value == "" {
		c.w.Header().Del(key)
		return
	}
	c.w.Header().Set(key, value)
}

// GetHeader get request header
func (c *Ctx) GetHeader(key string) string {
	return c.r.Header.Get(key)
}

func (c *Ctx) JSONP(data interface{}, callback ...string) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}

	cb := "callback"
	if len(callback) > 0 {
		cb = callback[0]
	}

	result := fmt.Sprintf("%s(%s);", cb, string(raw))
	c.w.Header().Set(HeaderContentType, MIMEApplicationJavaScriptCharsetUTF8)
	return c.SendString(result)
}

func (c *Ctx) ToJSON(data interface{}, err error) error {
	dat := map[string]interface{}{
		"status": true,
		"msg":    "ok",
		"result": data,
	}
	if err != nil {
		dat["status"] = false
		dat["msg"] = err.Error()
	}
	return c.JSON(dat)
}

// SendStatus send status code
func (c *Ctx) SendStatus(status int, msg ...string) error {
	c.Status(status)
	if len(msg) > 0 {
		return c.SendString(msg[0])
	}
	return nil
}

// Send send []byte to client
func (c *Ctx) Send(buf []byte) error {
	_, err := c.w.Write(buf)
	return err
}

// SendString send string to client
func (c *Ctx) SendString(str ...interface{}) error {
	buf := ""
	if len(str) == 1 {
		buf = fmt.Sprint(str...)
	} else if len(str) > 1 {
		buf = fmt.Sprintf(str[0].(string), str[1:]...)
	}
	_, err := c.w.WriteString(buf)
	return err
}

// Status WriteHeader status code
func (c *Ctx) Status(status int) *Ctx {
	c.w.WriteHeader(status)
	return c
}

// FormFile returns the first file for the provided form key.
// FormFile calls ParseMultipartForm and ParseForm if necessary.
func (c *Ctx) FormFile(key string) (*multipart.FileHeader, error) {
	if c.r.MultipartForm == nil {
		if err := c.r.ParseMultipartForm(c.core.MaxMultipartMemory); err != nil {
			return nil, err
		}
	}
	f, fh, err := c.r.FormFile(key)
	if err != nil {
		return nil, err
	}
	f.Close()
	return fh, err
}

// SaveFile upload file save to a folder
//
//  path = {root}/{dst}/{id}
//  @param
//  name string filename
//  dst string dst path
//  root string root path optional
//  id   path optional type uid.UID, int, uint, int64,uint64
//  rename bool optional
//  return relPath, absPath
//
//     c.SaveFile("file", "/images")
//     (string) relpath "/images/10/favicon.png"
//     (string) abspath "/images/10/favicon.png"
//
//     c.SaveFile("file", "/images", "./static")
//     (string) relpath "/images/10/5hsbkthaadld/favicon.png"
//     (string) abspath "/static/images/10/5hsbkthaadld/favicon.png"
//
//     c.SaveFile("file", "/images", "./static", uid.New())
//     (string) relpath "/images/10/5hsbkthaadld/5hsbkthaadld.png"
//     (string) abspath "/static/images/10/5hsbkthaadld/5hsbkthaadld.png"
//                👇file    👇dst      👇root     👇id      👇rename
//     c.SaveFile("file", "/images", "./static", uid.New(), true)
//     (string) relpath "/images/10/5hsbkthaadld/5hsbkthaadld.png"
//     (string) abspath "/static/images/10/5hsbkthaadld/5hsbkthaadld.png"
func (c *Ctx) SaveFile(key, dst string, args ...interface{}) (relpath, abspath string, err error) {
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

// FormValue Get query
//
//  key string
//  def string default val optional
//
// >  GET /?name=Jack&id=
//
//   `
//     name := c.FromValue("name")  // name = Jack
//     id := c.FromValue("id", "1") // id = 1 Because the default value is used
//   `
func (c *Ctx) FormValue(key string, def ...string) string {
	if val := c.r.FormValue(key); val != "" {
		return val
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

// FromValues returns a slice of strings for a given query key.
func (c *Ctx) FromValues(key string, def ...[]string) []string {
	if val, ok := c.querys[key]; ok {
		return val
	}
	if len(def) > 0 {
		return def[0]
	}
	return make([]string, 0)
}

func (c *Ctx) Set(key string, val interface{}) {
	c.mu.Lock()
	c.vars[key] = val
	c.mu.Unlock()
}

// Get returns the value for the given key, ie: (value, true).
// If the value does not exists it returns (nil, false)
func (c *Ctx) Get(key string) (val interface{}, ok bool) {
	c.mu.RLock()
	val, ok = c.vars[key]
	c.mu.RUnlock()
	return
}

// GetString returns the value associated with the key as a string.
func (c *Ctx) GetString(key string, def ...string) (value string) {
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
func (c *Ctx) GetBool(key string) (value bool) {
	if val, ok := c.Get(key); ok && val != nil {
		if value, ok = val.(bool); ok {
			return
		}
	}
	return false
}

// GetInt returns the value associated with the key as an integer.
func (c *Ctx) GetInt(key string, def ...int) (i int) {
	if val, ok := c.Get(key); ok && val != nil {
		if i, ok = val.(int); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return
}

// GetInt64 returns the value associated with the key as an integer.
func (c *Ctx) GetInt64(key string, def ...int64) (i int64) {
	if val, ok := c.Get(key); ok && val != nil {
		if i, ok = val.(int64); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return
}

// GetUint returns the value associated with the key as an integer.
func (c *Ctx) GetUint(key string, def ...uint) (i uint) {
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
func (c *Ctx) GetUint64(key string, def ...uint64) (i uint64) {
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
func (c *Ctx) GetFloat64(key string, def ...float64) (value float64) {
	if val, ok := c.Get(key); ok && val != nil {
		value, _ = val.(float64)
	}
	if len(def) > 0 {
		return def[0]
	}
	return
}

// GetTime returns the value associated with the key as time.
func (c *Ctx) GetTime(key string) (t time.Time) {
	if val, ok := c.Get(key); ok && val != nil {
		t, _ = val.(time.Time)
	}
	return
}

// GetDuration returns the value associated with the key as a duration.
func (c *Ctx) GetDuration(key string) (d time.Duration) {
	if val, ok := c.Get(key); ok && val != nil {
		d, _ = val.(time.Duration)
	}
	return
}

// GetStrings String Slice returns the value associated with the key as a slice of strings.
func (c *Ctx) GetStrings(key string, def ...[]string) (value []string) {
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
//  > return map[string]interface{}
func (c *Ctx) GetMap(key string, def ...map[string]interface{}) (value map[string]interface{}) {
	if val, ok := c.Get(key); ok && val != nil {
		if value, ok = val.(map[string]interface{}); ok {
			return
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return
}

// GetMapString returns the value associated with the key as a map of strings.
// 	> return map[string]string
func (c *Ctx) GetMapString(key string, def ...map[string]string) (value map[string]string) {
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
// 	> return map[string][]string
func (c *Ctx) GetMapStringSlice(key string, def ...map[string][]string) (value map[string][]string) {
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
//  > Experimental function, problem unknown
func (c *Ctx) GetAs(key string, v interface{}) error {
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

// RemoteIP parses the IP from Request.RemoteAddr, normalizes and returns the IP (without the port).
// It also checks if the remoteIP is a trusted proxy or not.
// In order to perform this validation, it will see if the IP is contained within at least one of the CIDR blocks
// defined by Engine.SetTrustedProxies()
func (c *Ctx) RemoteIP() (net.IP, bool) {
	ip, _, err := net.SplitHostPort(strings.TrimSpace(c.r.RemoteAddr))
	if err != nil {
		return nil, false
	}
	remoteIP := net.ParseIP(ip)
	if remoteIP == nil {
		return nil, false
	}

	return remoteIP, false
}

// Cookie

// SetCookie adds a Set-Cookie header to the ResponseWriter's headers.
// The provided cookie must have a valid Name. Invalid cookies may be
// silently dropped.
func (c *Ctx) SetCookie(name, value string, maxAge int, path, domain string, secure, httpOnly bool) {
	if path == "" {
		path = "/"
	}
	http.SetCookie(c.w, &http.Cookie{
		Name:     name,
		Value:    url.QueryEscape(value),
		MaxAge:   maxAge,
		Path:     path,
		Domain:   domain,
		SameSite: c.sameSite,
		Secure:   secure,
		HttpOnly: httpOnly,
	})
}

// Cookie returns the named cookie provided in the request or
// ErrNoCookie if not found. And return the named cookie is unescaped.
// If multiple cookies match the given name, only one cookie will
// be returned.
func (c *Ctx) Cookie(name string) (string, error) {
	cookie, err := c.r.Cookie(name)
	if err != nil {
		return "", err
	}
	val, _ := url.QueryUnescape(cookie.Value)
	return val, nil
}

// File writes the specified file into the body stream in an efficient way.
func (c *Ctx) File(filepath string) {
	http.ServeFile(c.w, c.r, filepath)
}

// FileAttachment writes the specified file into the body stream in an efficient way
// On the client side, the file will typically be downloaded with the given filename
func (c *Ctx) FileAttachment(filepath, filename string) {
	c.SetHeader(HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeFile(c.w, c.r, filepath)
}

// FileFromFS writes the specified file from http.FileSystem into the body stream in an efficient way.
func (c *Ctx) FileFromFS(filepath string, fs http.FileSystem) {
	defer func(old string) {
		c.r.URL.Path = old
	}(c.r.URL.Path)

	c.r.URL.Path = filepath

	http.FileServer(fs).ServeHTTP(c.w, c.r)
}

// Stream sends a streaming response and returns a boolean
// indicates "Is client disconnected in middle of stream"
func (c *Ctx) Stream(step func(w io.Writer) bool) bool {
	w := c.w
	ctx := c.Context
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
//   out interface{} MIMEApplicationForm MIMEMultipartForm MIMETextXML must struct
func (c *Ctx) ReadBody(out interface{}) error {
	// Get decoder from pool
	schemaDecoder := decoderPool.Get().(*schema.Decoder)
	defer decoderPool.Put(schemaDecoder)

	// Get content-type
	ctype := strings.ToLower(c.r.Header.Get(HeaderContentType))

	switch {
	case strings.HasPrefix(ctype, MIMEApplicationJSON):
		schemaDecoder.SetAliasTag("json")
		body, err := ioutil.ReadAll(c.r.Body)
		if err != nil {
			return err
		}
		return json.Unmarshal(body, out)
	case strings.HasPrefix(ctype, MIMEApplicationForm):
		schemaDecoder.SetAliasTag("form")
		if err := c.r.ParseForm(); err != nil {
			return err
		}
		return schemaDecoder.Decode(out, c.r.PostForm)
	case strings.HasPrefix(ctype, MIMEMultipartForm):
		schemaDecoder.SetAliasTag("form")
		if err := c.r.ParseMultipartForm(1048576); err != nil {
			return nil
		}
		return schemaDecoder.Decode(out, c.r.MultipartForm.Value)
	case strings.HasPrefix(ctype, MIMETextXML), strings.HasPrefix(ctype, MIMEApplicationXML):
		schemaDecoder.SetAliasTag("xml")
		body, err := ioutil.ReadAll(c.r.Body)
		if err != nil {
			return err
		}
		return xml.Unmarshal(body, out)
	}
	// No suitable content type found
	return ErrUnprocessableEntity
}

// decoderPool helps to improve ReadBody's and QueryParser's performance
var decoderPool = &sync.Pool{New: func() interface{} {
	var decoder = schema.NewDecoder()
	decoder.IgnoreUnknownKeys(true)
	return decoder
}}

// An InvalidUnmarshalError describes an invalid argument passed to Unmarshal.
// (The argument to Unmarshal must be a non-nil pointer.)
type InvalidUnmarshalError struct {
	Type reflect.Type
}

func (e *InvalidUnmarshalError) Error() string {
	if e.Type == nil {
		return "ctx: Unmarshal(nil)"
	}

	if e.Type.Kind() != reflect.Ptr {
		return "ctx: Unmarshal(non-pointer " + e.Type.String() + ")"
	}
	return "ctx: Unmarshal(nil " + e.Type.String() + ")"
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
			log.Printf("headers were already written. Wanted to override status code %d with %d", w.status, code)
		}
		w.status = code
	}
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
