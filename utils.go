package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/xs23933/uid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func IsNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
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

type Errors interface {
	Error() string
	Errors() (int, string)
}

func IsErrors(v any) bool {
	_, ok := v.(Errors)
	return ok
}

type Error struct {
	status *status.Status
}

func NewError(code int, args ...any) error {
	msg := StatusMessage(code)
	if len(args) > 1 {
		msg = fmt.Sprintf(args[0].(string), args[1:]...)
	} else if len(args) == 1 {
		msg = args[0].(string)
	}

	return &Error{
		status: status.New(codes.Code(code), msg),
	}
}

// Error 实现 error 接口
func (e *Error) Error() string {
	return e.status.Message()
}

// GRPCStatus 实现 gRPC 状态接口，让 status.FromError 能正常工作
func (e *Error) GRPCStatus() *status.Status {
	return e.status
}

// Errors 实现自定义接口，返回业务错误码和消息
func (e *Error) Errors() (int, string) {
	return int(e.status.Code()), e.status.Message()
}

func (e *Error) Unwrap() error {
	return e.status.Err()
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

func ToNamer(name string) string {
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

		// 当前字符是占位符时特殊处理
		if v == '\x00' || v == '\x01' {
			buf.WriteRune(v)
			lastCase = currCase
			currCase = false
			continue
		}

		if i > 0 {
			if currCase {
				if lastCase && (nextCase || nextNumber) {
					buf.WriteRune(v)
				} else {
					if value[i-1] != '/' && value[iPlus] != '/' &&
						value[i-1] != '\x00' && value[iPlus] != '\x00' &&
						value[i-1] != '\x01' && value[iPlus] != '\x01' {
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

	// 处理最后一个字符
	lastChar := value[len(value)-1]
	if lastChar != '\x00' && lastChar != '\x01' {
		buf.WriteByte(lastChar)
	}

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
	result := replacer.Replace(s)

	// 清理占位符周围的斜杠，然后替换为中横线
	result = strings.ReplaceAll(result, "/\x00/", "\x00")
	result = strings.ReplaceAll(result, "/\x00", "\x00")
	result = strings.ReplaceAll(result, "\x00/", "\x00")
	result = strings.ReplaceAll(result, "\x00", "-")
	// 处理点号（__dot__）
	result = strings.ReplaceAll(result, "/\x01/", "\x01")
	result = strings.ReplaceAll(result, "/\x01", "\x01")
	result = strings.ReplaceAll(result, "\x01/", "\x01")
	result = strings.ReplaceAll(result, "\x01", ".")
	return result
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

func (d Map) Contains(k string) bool {
	_, ok := d[k]
	return ok
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
		if v, ok := val.(int); ok {
			value = v
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

func (d Map) GetAs(k string, v any) error {
	if val, ok := d[k]; ok && val != nil {
		rv := reflect.ValueOf(v)
		if rv.Kind() != reflect.Pointer || rv.IsNil() {
			return &InvalidUnmarshalError{reflect.TypeOf(v)}
		}
		// 先尝试直接转换
		srcVal := reflect.ValueOf(val)
		srcType := srcVal.Type()
		dstType := rv.Elem().Type()

		if srcType.ConvertibleTo(dstType) {
			rv.Elem().Set(srcVal.Convert(dstType))
			return nil
		}

		// 如果不能直接转换，使用 JSON 序列化/反序列化
		buf, err := sonic.Marshal(val)
		if err != nil {
			return err
		}
		return sonic.Unmarshal(buf, v)
	}
	return ErrDataTypeNotSupport
}

func (d Map) UnmarshalTo(k string, v any) error {
	if val, ok := d[k]; ok && val != nil {
		buf, _ := sonic.Marshal(val)
		return sonic.Unmarshal(buf, v)
	}
	return ErrDataTypeNotSupport
}

func (d Map) MarshalBinary() (data []byte, err error) {
	return sonic.Marshal(d)
}

func (d *Map) UnmarshalBinary(data []byte) error {
	return sonic.Unmarshal(data, d)
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
	arr := make([]string, 0, len(d))
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

func (d Array) MarshalBinary() (data []byte, err error) {
	return sonic.Marshal(d)
}

func (d *Array) UnmarshalBinary(data []byte) error {
	return sonic.Unmarshal(data, d)
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

// 删除指定元素
func Remove[T comparable](elems []T, v T) []T {
	idx := Index(elems, v)
	if idx < 0 {
		return elems
	}
	return append(elems[:idx], elems[idx+1:]...)
}

// RemoveAll 删除所有匹配的元素
func RemoveAll[S ~[]E, E comparable](s S, v E) S {
	result := make(S, 0, len(s))
	for _, item := range s {
		if item != v {
			result = append(result, item)
		}
	}
	return result
}

// Unique 去重
//
// uniqueScopes := core.Unique(scopes)
func Unique[S ~[]E, E comparable](s S) S {
	seen := make(map[E]struct{})
	result := make(S, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			result = append(result, v)
		}
	}
	return result
}

func PrintJSON(v any, tags ...any) {
	buf, _ := json.MarshalIndent(v, "", " ")
	if len(tags) > 0 {
		format := fmt.Sprint(tags[0])
		if len(tags) > 1 {
			args := tags[1:]
			tagStr := fmt.Sprintf(format, args...) // 展开参数
			Warn("[%s]\n%s", tagStr, string(buf))
			return
		}
		Warn("[%s]\n%s", format, string(buf))
		return
	}
	Warn("%s", buf)
}

// RemoveDuplicates 去重函数，适用于任何类型的切片
func RemoveDuplicates[T comparable](slice []T) []T {
	// 使用 map 来记录已经出现过的元素
	seen := make(map[T]struct{})
	result := []T{}

	for _, v := range slice {
		if _, ok := seen[v]; !ok {
			// 如果没有出现过，加入到结果中
			result = append(result, v)
			seen[v] = struct{}{}
		}
	}

	return result
}

// ParseAndDeduplicate 将输入字符串按逗号分割，去除空字段及重复值
func ParseAndDeduplicate(s string) Array {
	parts := strings.Split(s, ",") // 分割字符串
	uniqueMap := make(map[string]struct{})
	var result Array
	for _, part := range parts {
		trimmed := strings.TrimSpace(part) // 去除空白字符
		if trimmed == "" {
			continue // 过滤空字符串
		}
		if _, exists := uniqueMap[trimmed]; !exists {
			uniqueMap[trimmed] = struct{}{}
			result = append(result, trimmed)
		}
	}
	return result
}

func ContainsAny(elems Array, v any) bool {
	for _, s := range elems {
		switch val := s.(type) {
		case string:
			if str, ok := v.(string); ok && val == str {
				return true
			}
		case int:
			if num, ok := v.(int); ok && val == num {
				return true
			}
		case int64:
			if num, ok := v.(int64); ok && val == num {
				return true
			}
		case float64:
			if num, ok := v.(float64); ok && val == num {
				return true
			}
		case bool:
			if num, ok := v.(bool); ok && val == num {
				return true
			}
		case time.Time:
			if num, ok := v.(time.Time); ok && val == num {
				return true
			}
		case []byte:
			if num, ok := v.([]byte); ok && bytes.Equal(val, num) {
				return true
			}
		default:
			if reflect.DeepEqual(s, v) {
				return true
			}
		}
	}
	return false
}

// Filter 过滤
//
//	activeUsers := core.Filter(users, func(u User) bool {
//	    return u.Status == "active"
//	})
func Filter[T any](slice []T, test func(T) bool) []T {
	result := make([]T, 0)
	for _, item := range slice {
		if test(item) {
			result = append(result, item)
		}
	}
	return result
}

// ExtractPrimaryDomain 提取一级域名（主域名）
// 示例：
//
//	"webin.work" -> "webin.work"
//	"www.webin.work" -> "webin.work"
//	"api.webin.work" -> "webin.work"
//	"customer.com" -> "customer.com"
//	"www.customer.com" -> "customer.com"
func ExtractPrimaryDomain(host string) string {
	// 移除端口号（如果有）
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}

	// 转为小写
	host = strings.ToLower(host)

	// 移除常见的 www 前缀
	host = strings.TrimPrefix(host, "www.")

	// 处理多级子域名的情况（如 api.webin.work）
	// 方法1：简单的去掉第一个标签（适用于大部分情况）
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		// 这里简单返回最后两个部分
		return strings.Join(parts[len(parts)-2:], ".")
	}

	return host
}

var ipHeaders = []string{"Cf-Connecting-Ip", "X-Real-Ip", "X-Forwarded-For"}

type iGet interface {
	Get(key string) string
}

func RemoteIP(h iGet, ip string) net.IP {
	// 按照优先级检查各个HTTP头
	for _, header := range ipHeaders {
		ip := strings.TrimSpace(h.Get(header))
		if ip == "" {
			continue
		}
		// 多个 IP 时取第一个（用户真实 IP）
		if header == "X-Forwarded-For" {
			parts := strings.Split(ip, ",")
			ip = strings.TrimSpace(parts[0])
		}
		if realIP := net.ParseIP(ip); realIP != nil {
			return realIP
		}
	}

	// 最后 RemoteAddr
	if host, _, err := net.SplitHostPort(strings.TrimSpace(ip)); err == nil {
		if realIP := net.ParseIP(host); realIP != nil {
			return realIP
		}
	}

	return nil
}

type mda struct {
	md metadata.MD
}

func (m *mda) Get(key string) string {
	v := m.md.Get(key)
	if len(v) > 0 {
		return v[0]
	}
	return ""
}

type ClientInfo struct {
	IP string
	UA string
}

func ExtractClientInfo(ctx context.Context) *ClientInfo {
	peerInfo, _ := peer.FromContext(ctx)

	result := &ClientInfo{}
	if p, ok := metadata.FromIncomingContext(ctx); ok {
		ip := RemoteIP(&mda{md: p}, peerInfo.Addr.String())
		result.IP = ip.String()
		if ua := p.Get("user-agent"); len(ua) > 0 {
			result.UA = ua[0]
		}
	}
	return result
}
func GrpcHeader(ctx context.Context, key string) string {
	if p, ok := metadata.FromIncomingContext(ctx); ok {
		vals := p.Get(key)
		if len(vals) > 0 {
			return vals[0]
		}
	}
	return ""
}

func SHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
