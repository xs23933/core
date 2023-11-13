package core

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/xs23933/uid"
)

func Index[S ~[]E, E comparable](s S, v E) int {
	for i := range s {
		if v == s[i] {
			return i
		}
	}
	return -1
}

func Contains[S ~[]E, E comparable](s S, v E) bool {
	return Index(s, v) >= 0
}

func lastChar(str string) uint8 {
	if str == "" {
		panic("The length of the string can't be 0")
	}
	return str[len(str)-1]
}

func joinPaths(absolutePath, relativePath string) string {
	if relativePath == "" {
		return absolutePath
	}

	finalPath := path.Join(absolutePath, relativePath)
	if lastChar(relativePath) == '/' && lastChar(finalPath) != '/' {
		return finalPath + "/"
	}
	return finalPath
}

type onlyFilesFS struct {
	fs http.FileSystem
}

type neuteredReaddirFile struct {
	http.File
}

// Dir returns a http.FileSystem that can be used by http.FileServer(). It is used internally
// in router.Static().
// if listDirectory == true, then it works the same as http.Dir() otherwise it returns
// a filesystem that prevents http.FileServer() to list the directory files.
func Dir(root string, listDirectory bool) http.FileSystem {
	fs := http.Dir(root)
	if listDirectory {
		return fs
	}
	return &onlyFilesFS{fs}
}

// Open conforms to http.Filesystem.
func (fs onlyFilesFS) Open(name string) (http.File, error) {
	f, err := fs.fs.Open(name)
	if err != nil {
		return nil, err
	}
	return neuteredReaddirFile{f}, nil
}

// Readdir overrides the http.File default implementation.
func (f neuteredReaddirFile) Readdir(_ int) ([]os.FileInfo, error) {
	// this disables directory listing
	return nil, nil
}

// Delete removes the elements s[i:j] from s, returning the modified slice.
// Delete panics if s[i:j] is not a valid slice of s.
// Delete is O(len(s)-j), so if many items must be deleted, it is better to
// make a single call deleting them all together than to delete one at a time.
// Delete might not modify the elements s[len(s)-(j-i):len(s)]. If those
// elements contain pointers you might consider zeroing those elements so that
// objects they reference can be garbage collected.
func Delete[S ~[]E, E any](s S, i, j int) S {
	_ = s[i:j] // bounds check

	return append(s[:i], s[j:]...)
}

// uniqueRouteStack drop all not unique routes from the slice
func uniqueRouteStack(stack []*Route) []*Route {
	var unique []*Route
	m := make(map[*Route]int)
	for _, v := range stack {
		if _, ok := m[v]; !ok {
			// Unique key found. Record position and collect
			// in result.
			m[v] = len(unique)
			unique = append(unique, v)
		}
	}

	return unique
}

// Error represents an error that occurred while handling a request.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewError creates a new Error instance with an optional message
func NewError(code int, message ...string) *Error {
	err := &Error{
		Code:    code,
		Message: StatusMessage(code),
	}
	if len(message) > 0 {
		err.Message = message[0]
	}
	return err
}

// Error makes it compatible with the `error` interface.
func (e *Error) Error() string {
	return e.Message
}

func getGroupPath(prefix, path string) string {
	if len(path) == 0 {
		return prefix
	}

	if path[0] != '/' {
		path = "/" + path
	}

	return strings.TrimRight(prefix, "/") + path
}

type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (net.Conn, error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return nil, err
	}
	if err = tc.SetKeepAlive(true); err != nil {
		return nil, err
	}
	return tc, err
}

func toNamer(name string) string {
	if name == "" {
		return ""
	}
	var (
		value                                    = commonInitialismsReplacer.Replace(name)
		buf                                      = bytes.NewBufferString("")
		lastCase, currCase, nextCase, nextNumber bool
	)
	for i, v := range value[:len(value)-1] {
		iPlus := i + 1
		nextCase = bool(value[iPlus] >= 'A' && value[iPlus] <= 'Z')
		nextNumber = bool(value[iPlus] >= '0' && value[iPlus] <= '9')

		if i > 0 {
			if currCase {
				if lastCase && (nextCase || nextNumber) {
					buf.WriteRune(v)
				} else {
					if value[i-1] != '/' && value[iPlus] != '/' {
						buf.WriteRune('/')
					}
					buf.WriteRune(v)
				}
			} else {
				buf.WriteRune(v)
				if i == len(value)-2 && (nextCase && !nextNumber) {
					buf.WriteRune('/')
				}
			}
		} else {
			currCase = true
			buf.WriteRune(v)
		}
		lastCase = currCase
		currCase = nextCase
	}

	buf.WriteByte(value[len(value)-1])

	s := strings.ToLower(buf.String())

	reps := []string{
		"param5", ":param5",
		"param4", ":param4",
		"param3", ":param3",
		"param2", ":param2",
		"param1", ":param1",
		"params", ":param?",
		"param", ":param",
		"/dot/", ".",
		"_/", "/:",
		"_", "/:",
	}

	replacer := strings.NewReplacer(reps...)

	return replacer.Replace(s)
}

func FixURI(pre, src, tag string) string {
	tag = strings.ToLower(tag)
	uri := path.Join(pre, strings.TrimLeft(src, tag))
	if len(uri) == 0 {
		uri = "/"
	}
	return uri
}

const (
	toLowerTable = "\x00\x01\x02\x03\x04\x05\x06\a\b\t\n\v\f\r\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f !\"#$%&'()*+,-./0123456789:;<=>?@abcdefghijklmnopqrstuvwxyz[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~\u007f\x80\x81\x82\x83\x84\x85\x86\x87\x88\x89\x8a\x8b\x8c\x8d\x8e\x8f\x90\x91\x92\x93\x94\x95\x96\x97\x98\x99\x9a\x9b\x9c\x9d\x9e\x9f\xa0\xa1\xa2\xa3\xa4\xa5\xa6\xa7\xa8\xa9\xaa\xab\xac\xad\xae\xaf\xb0\xb1\xb2\xb3\xb4\xb5\xb6\xb7\xb8\xb9\xba\xbb\xbc\xbd\xbe\xbf\xc0\xc1\xc2\xc3\xc4\xc5\xc6\xc7\xc8\xc9\xca\xcb\xcc\xcd\xce\xcf\xd0\xd1\xd2\xd3\xd4\xd5\xd6\xd7\xd8\xd9\xda\xdb\xdc\xdd\xde\xdf\xe0\xe1\xe2\xe3\xe4\xe5\xe6\xe7\xe8\xe9\xea\xeb\xec\xed\xee\xef\xf0\xf1\xf2\xf3\xf4\xf5\xf6\xf7\xf8\xf9\xfa\xfb\xfc\xfd\xfe\xff"
	toUpperTable = "\x00\x01\x02\x03\x04\x05\x06\a\b\t\n\v\f\r\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f !\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`ABCDEFGHIJKLMNOPQRSTUVWXYZ{|}~\u007f\x80\x81\x82\x83\x84\x85\x86\x87\x88\x89\x8a\x8b\x8c\x8d\x8e\x8f\x90\x91\x92\x93\x94\x95\x96\x97\x98\x99\x9a\x9b\x9c\x9d\x9e\x9f\xa0\xa1\xa2\xa3\xa4\xa5\xa6\xa7\xa8\xa9\xaa\xab\xac\xad\xae\xaf\xb0\xb1\xb2\xb3\xb4\xb5\xb6\xb7\xb8\xb9\xba\xbb\xbc\xbd\xbe\xbf\xc0\xc1\xc2\xc3\xc4\xc5\xc6\xc7\xc8\xc9\xca\xcb\xcc\xcd\xce\xcf\xd0\xd1\xd2\xd3\xd4\xd5\xd6\xd7\xd8\xd9\xda\xdb\xdc\xdd\xde\xdf\xe0\xe1\xe2\xe3\xe4\xe5\xe6\xe7\xe8\xe9\xea\xeb\xec\xed\xee\xef\xf0\xf1\xf2\xf3\xf4\xf5\xf6\xf7\xf8\xf9\xfa\xfb\xfc\xfd\xfe\xff"
)

type byteSeq interface {
	~string | ~[]byte
}

// EqualFold tests ascii strings or bytes for equality case-insensitively
func EqualFold[S byteSeq](b, s S) bool {
	if len(b) != len(s) {
		return false
	}
	for i := len(b) - 1; i >= 0; i-- {
		if toUpperTable[b[i]] != toUpperTable[s[i]] {
			return false
		}
	}
	return true
}

// defaultString returns the value or a default value if it is set
func defaultString(value string, defaultValue []string) string {
	if len(value) == 0 && len(defaultValue) > 0 {
		return defaultValue[0]
	}
	return value
}

func LocalIP() (ip net.IP, err error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return
	}
	defer conn.Close()

	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP, nil
}

type Map map[string]any

// Value 数据驱动接口
func (d Map) Value() (driver.Value, error) {
	bytes, err := sonic.Marshal(d)
	return string(bytes), err
}

// Scan 数据驱动接口
func (d *Map) Scan(src any) error {
	switch val := src.(type) {
	case string:
		return sonic.Unmarshal([]byte(val), d)
	case []byte:
		if strings.EqualFold(string(val), "null") {
			*d = make(Map)
			return nil
		}
		if err := sonic.Unmarshal(val, d); err != nil {
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

func (d Map) GetString(k string, defaultValue ...string) (value string) {
	if val, ok := d[k]; ok && val != nil {
		if value, ok = val.(string); ok {
			return
		}
	}
	return defaultString("", defaultValue)
}

func (d Map) GetInt(k string, defaultValue ...int) (value int) {
	if val, ok := d[k]; ok && val != nil {
		if v, ok := val.(float64); ok {
			value = int(v)
			return
		}
	}
	if len(defaultValue) > 0 {
		return defaultValue[0]
	}
	return 0
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
	return defaultString("", def)
}

func (d Map) GetBool(k string) (value bool) {
	if val, ok := d[k]; ok && val != nil {
		if value, ok = val.(bool); ok {
			return
		}
	}
	return false
}

// Array 数组类型
type Array []any

func (d Array) FindHandle(handle, value string) Map {
	for _, r := range d {
		var v Map
		switch val := r.(type) {
		case map[string]any:
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
	bytes, err := sonic.Marshal(d)
	return string(bytes), err
}

// Scan 数据驱动接口
func (d *Array) Scan(src any) error {
	*d = Array{}
	switch val := src.(type) {
	case string:
		return sonic.Unmarshal([]byte(val), d)
	case []byte:
		if strings.EqualFold(string(val), "null") {
			return nil
		}
		if err := sonic.Unmarshal(val, d); err != nil {
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
func (s *StringOrNil) Scan(src any) error {
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

// MakePath make dir
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
//	 `
//	   MakePath("favicon.png", "/images")
//	   (string) relpath "/images/10/favicon.png"
//	   (string) abspath "/images/10/favicon.png"
//
//	   MakePath("favicon.png", "/images", "/static")
//	   (string) relpath "/images/10/5hsbkthaadld/favicon.png"
//	   (string) abspath "/static/images/10/5hsbkthaadld/favicon.png"
//
//	   MakePath("favicon.png", "/images", "/static", uid.New())
//	   (string) relpath "/images/10/5hsbkthaadld/5hsbkthaadld.png"
//	   (string) abspath "/static/images/10/5hsbkthaadld/5hsbkthaadld.png"
//	              👇filename    👇dst      👇root     👇id      👇rename
//	   MakePath("favicon.png", "/images", "/static", uid.New(), true)
//	   (string) relpath "/images/10/5hsbkthaadld/5hsbkthaadld.png"
//	   (string) abspath "/static/images/10/5hsbkthaadld/5hsbkthaadld.png"
//	 `
func MakePath(name, dst string, args ...any) (string, string, error) {
	mon := time.Now().String()[5:7]
	pathArr := []string{dst, mon}
	root := ""
	rename := false
	rName := strings.ToLower(NewUUID().String())
	for _, arg := range args {
		switch a := arg.(type) {
		case int, int64, uint, uint64:
			pathArr = append(pathArr, fmt.Sprintf("%08d", a))
		case uid.UID:
			pathArr = append(pathArr, strings.ToLower(a.String()))
		case uuid.UUID:
			pathArr = append(pathArr, a.String())
		case string:
			root = a
		case bool:
			rename = true
		}
	}
	nName := filepath.Base(name)
	if rename {
		ext := filepath.Ext(nName)
		nName = rName + ext
	}
	pathArr = append(pathArr, nName)
	relPath := filepath.Join(pathArr...)
	absPath := relPath
	if root != "" {
		absPath = filepath.Join(root, relPath)
	}
	absPath, _ = filepath.Abs(absPath)
	absDir := filepath.Dir(absPath)
	if !Exists(absDir) {
		if err := os.MkdirAll(absDir, 0755); err != nil {
			return "", absDir, err
		}
	}
	return relPath, absPath, nil
}

// Exists check file or path exists
func Exists(absDir string) bool {
	_, err := os.Stat(absDir) //os.Stat获取文件信息
	if err == nil || os.IsExist(err) {
		return true
	}
	return false
}

// return valid offer for header negotiation
func getOffer(header string, offers ...string) string {
	if len(offers) == 0 {
		return ""
	} else if header == "" {
		return offers[0]
	}

	spec, commaPos := "", 0
	for len(header) > 0 && commaPos != -1 {
		commaPos = strings.IndexByte(header, ',')
		if commaPos != -1 {
			spec = strings.TrimSpace(header[:commaPos])
		} else {
			spec = header
		}
		if factorSign := strings.IndexByte(spec, ';'); factorSign != -1 {
			spec = spec[:factorSign]
		}

		for _, offer := range offers {
			// has star prefix
			if len(spec) >= 1 && spec[len(spec)-1] == '*' {
				return offer
			} else if strings.HasPrefix(spec, offer) {
				return offer
			}
		}
		if commaPos != -1 {
			header = header[commaPos+1:]
		}
	}

	return ""
}

// IsChild determines if the current process is a child of Prefork
func IsChild() bool {
	return os.Getenv(envPreforkChildKey) == envPreforkChildVal
}

// watchMaster watches child procs
func watchMaster() {
	if runtime.GOOS == "windows" {
		// finds parent process,
		// and waits for it to exit
		p, err := os.FindProcess(os.Getppid())
		if err == nil {
			_, _ = p.Wait() //nolint:errcheck // It is fine to ignore the error here
		}
		os.Exit(1) //nolint:revive // Calling os.Exit is fine here in the prefork
	}
	// if it is equal to 1 (init process ID),
	// it indicates that the master process has exited
	const watchInterval = 500 * time.Millisecond
	for range time.NewTicker(watchInterval).C {
		if os.Getppid() == 1 {
			os.Exit(1) //nolint:revive // Calling os.Exit is fine here in the prefork
		}
	}
}
