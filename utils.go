package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/google/uuid"
	"github.com/xs23933/uid"
)

// Exists check file or path exists
func Exists(path string) bool {
	_, err := os.Stat(path) //os.Stat获取文件信息
	if err == nil || os.IsExist(err) {
		return true
	}
	return false
}

// MakePath make dir
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
//   `
//     MakePath("favicon.png", "/images")
//     (string) relpath "/images/10/favicon.png"
//     (string) abspath "/images/10/favicon.png"
//
//     MakePath("favicon.png", "/images", "/static")
//     (string) relpath "/images/10/5hsbkthaadld/favicon.png"
//     (string) abspath "/static/images/10/5hsbkthaadld/favicon.png"
//
//     MakePath("favicon.png", "/images", "/static", uid.New())
//     (string) relpath "/images/10/5hsbkthaadld/5hsbkthaadld.png"
//     (string) abspath "/static/images/10/5hsbkthaadld/5hsbkthaadld.png"
//                👇filename    👇dst      👇root     👇id      👇rename
//     MakePath("favicon.png", "/images", "/static", uid.New(), true)
//     (string) relpath "/images/10/5hsbkthaadld/5hsbkthaadld.png"
//     (string) abspath "/static/images/10/5hsbkthaadld/5hsbkthaadld.png"
//   `
func MakePath(name, dst string, args ...interface{}) (string, string, error) {
	mon := time.Now().String()[5:7]
	pathArr := []string{dst, mon}
	root := ""
	rename := false
	rName := strings.ToLower(uid.New().String())
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

func init() {
	var commonInitialismsForReplacer []string
	for _, initialism := range commonInitialisms {
		commonInitialismsForReplacer = append(commonInitialismsForReplacer, initialism, strings.Title(strings.ToLower(initialism)))
	}
	commonInitialismsReplacer = strings.NewReplacer(commonInitialismsForReplacer...)
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
		nextCase = bool(value[i+1] >= 'A' && value[i+1] <= 'Z')
		nextNumber = bool(value[i+1] >= '0' && value[i+1] <= '9')

		if i > 0 {
			if currCase {
				if lastCase && (nextCase || nextNumber) {
					buf.WriteRune(v)
				} else {
					if value[i-1] != '/' && value[i+1] != '/' {
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
		"param4", ":param4",
		"param3", ":param3",
		"param2", ":param2",
		"params", ":param?",
		"param", ":param",
		"_", ".",
	}

	replacer := strings.NewReplacer(reps...)
	return replacer.Replace(s)
}

func fixURI(pre, src, tag string) string {
	tag = strings.ToLower(tag)
	uri := path.Join(pre, strings.TrimLeft(src, tag))
	if len(uri) == 0 {
		uri = "/"
	}
	return uri
}

// Panicf format panic
func Panicf(s string, args ...interface{}) {
	panic(fmt.Sprintf(s, args...))
}

// Dump 打印数据信息
func Dump(v ...interface{}) {
	spew.Dump(v...)
}

// limits for HTTP statuscodes
const (
	statusMessageMin            = 100
	statusMessageMax            = 511
	defaultMultipartMemory      = 32 << 20 // 32 MB
	abortIdx               int8 = math.MaxInt8 >> 1
)

// StatusMessage returns the correct message for the provided HTTP statuscode
func StatusMessage(status int) string {
	if status < statusMessageMin || status > statusMessageMax {
		return ""
	}
	return statusMessage[status]
}

// HTTP status codes were copied from net/http.
var statusMessage = []string{
	100: "Continue",
	101: "Switching Protocols",
	102: "Processing",
	103: "Early Hints",
	200: "OK",
	201: "Created",
	202: "Accepted",
	203: "Non-Authoritative Information",
	204: "No Content",
	205: "Reset Content",
	206: "Partial Content",
	207: "Multi-Status",
	208: "Already Reported",
	226: "IM Used",
	300: "Multiple Choices",
	301: "Moved Permanently",
	302: "Found",
	303: "See Other",
	304: "Not Modified",
	305: "Use Proxy",
	306: "Switch Proxy",
	307: "Temporary Redirect",
	308: "Permanent Redirect",
	400: "Bad Request",
	401: "Unauthorized",
	402: "Payment Required",
	403: "Forbidden",
	404: "Not Found",
	405: "Method Not Allowed",
	406: "Not Acceptable",
	407: "Proxy Authentication Required",
	408: "Request Timeout",
	409: "Conflict",
	410: "Gone",
	411: "Length Required",
	412: "Precondition Failed",
	413: "Request Entity Too Large",
	414: "Request URI Too Long",
	415: "Unsupported Media Type",
	416: "Requested Range Not Satisfiable",
	417: "Expectation Failed",
	418: "I'm a teapot",
	421: "Misdirected Request",
	422: "Unprocessable Entity",
	423: "Locked",
	424: "Failed Dependency",
	426: "Upgrade Required",
	428: "Precondition Required",
	429: "Too Many Requests",
	431: "Request Header Fields Too Large",
	451: "Unavailable For Legal Reasons",
	500: "Internal Server Error",
	501: "Not Implemented",
	502: "Bad Gateway",
	503: "Service Unavailable",
	504: "Gateway Timeout",
	505: "HTTP Version Not Supported",
	506: "Variant Also Negotiates",
	507: "Insufficient Storage",
	508: "Loop Detected",
	510: "Not Extended",
	511: "Network Authentication Required",
}

// HTTP status codes were copied from net/http.
const (
	StatusContinue                      = 100 // RFC 7231, 6.2.1
	StatusSwitchingProtocols            = 101 // RFC 7231, 6.2.2
	StatusProcessing                    = 102 // RFC 2518, 10.1
	StatusEarlyHints                    = 103 // RFC 8297
	StatusOK                            = 200 // RFC 7231, 6.3.1
	StatusCreated                       = 201 // RFC 7231, 6.3.2
	StatusAccepted                      = 202 // RFC 7231, 6.3.3
	StatusNonAuthoritativeInformation   = 203 // RFC 7231, 6.3.4
	StatusNoContent                     = 204 // RFC 7231, 6.3.5
	StatusResetContent                  = 205 // RFC 7231, 6.3.6
	StatusPartialContent                = 206 // RFC 7233, 4.1
	StatusMultiStatus                   = 207 // RFC 4918, 11.1
	StatusAlreadyReported               = 208 // RFC 5842, 7.1
	StatusIMUsed                        = 226 // RFC 3229, 10.4.1
	StatusMultipleChoices               = 300 // RFC 7231, 6.4.1
	StatusMovedPermanently              = 301 // RFC 7231, 6.4.2
	StatusFound                         = 302 // RFC 7231, 6.4.3
	StatusSeeOther                      = 303 // RFC 7231, 6.4.4
	StatusNotModified                   = 304 // RFC 7232, 4.1
	StatusUseProxy                      = 305 // RFC 7231, 6.4.5
	StatusTemporaryRedirect             = 307 // RFC 7231, 6.4.7
	StatusPermanentRedirect             = 308 // RFC 7538, 3
	StatusBadRequest                    = 400 // RFC 7231, 6.5.1
	StatusUnauthorized                  = 401 // RFC 7235, 3.1
	StatusPaymentRequired               = 402 // RFC 7231, 6.5.2
	StatusForbidden                     = 403 // RFC 7231, 6.5.3
	StatusNotFound                      = 404 // RFC 7231, 6.5.4
	StatusMethodNotAllowed              = 405 // RFC 7231, 6.5.5
	StatusNotAcceptable                 = 406 // RFC 7231, 6.5.6
	StatusProxyAuthRequired             = 407 // RFC 7235, 3.2
	StatusRequestTimeout                = 408 // RFC 7231, 6.5.7
	StatusConflict                      = 409 // RFC 7231, 6.5.8
	StatusGone                          = 410 // RFC 7231, 6.5.9
	StatusLengthRequired                = 411 // RFC 7231, 6.5.10
	StatusPreconditionFailed            = 412 // RFC 7232, 4.2
	StatusRequestEntityTooLarge         = 413 // RFC 7231, 6.5.11
	StatusRequestURITooLong             = 414 // RFC 7231, 6.5.12
	StatusUnsupportedMediaType          = 415 // RFC 7231, 6.5.13
	StatusRequestedRangeNotSatisfiable  = 416 // RFC 7233, 4.4
	StatusExpectationFailed             = 417 // RFC 7231, 6.5.14
	StatusTeapot                        = 418 // RFC 7168, 2.3.3
	StatusMisdirectedRequest            = 421 // RFC 7540, 9.1.2
	StatusUnprocessableEntity           = 422 // RFC 4918, 11.2
	StatusLocked                        = 423 // RFC 4918, 11.3
	StatusFailedDependency              = 424 // RFC 4918, 11.4
	StatusTooEarly                      = 425 // RFC 8470, 5.2.
	StatusUpgradeRequired               = 426 // RFC 7231, 6.5.15
	StatusPreconditionRequired          = 428 // RFC 6585, 3
	StatusTooManyRequests               = 429 // RFC 6585, 4
	StatusRequestHeaderFieldsTooLarge   = 431 // RFC 6585, 5
	StatusUnavailableForLegalReasons    = 451 // RFC 7725, 3
	StatusInternalServerError           = 500 // RFC 7231, 6.6.1
	StatusNotImplemented                = 501 // RFC 7231, 6.6.2
	StatusBadGateway                    = 502 // RFC 7231, 6.6.3
	StatusServiceUnavailable            = 503 // RFC 7231, 6.6.4
	StatusGatewayTimeout                = 504 // RFC 7231, 6.6.5
	StatusHTTPVersionNotSupported       = 505 // RFC 7231, 6.6.6
	StatusVariantAlsoNegotiates         = 506 // RFC 2295, 8.1
	StatusInsufficientStorage           = 507 // RFC 4918, 11.5
	StatusLoopDetected                  = 508 // RFC 5842, 7.2
	StatusNotExtended                   = 510 // RFC 2774, 7
	StatusNetworkAuthenticationRequired = 511 // RFC 6585, 6
)

// Error represents an error that occurred while handling a request.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error makes it compatible with the `error` interface.
func (e *Error) Error() string {
	return e.Message
}

// NewError creates a new Error instance with an optional message
func NewError(code int, message ...string) *Error {
	e := &Error{
		Code: code,
	}
	if len(message) > 0 {
		e.Message = message[0]
	} else {
		e.Message = StatusMessage(code)
	}
	return e
}

// Errors
var (
	ErrBadRequest                    = NewError(StatusBadRequest)                    // RFC 7231, 6.5.1
	ErrUnauthorized                  = NewError(StatusUnauthorized)                  // RFC 7235, 3.1
	ErrPaymentRequired               = NewError(StatusPaymentRequired)               // RFC 7231, 6.5.2
	ErrForbidden                     = NewError(StatusForbidden)                     // RFC 7231, 6.5.3
	ErrNotFound                      = NewError(StatusNotFound)                      // RFC 7231, 6.5.4
	ErrMethodNotAllowed              = NewError(StatusMethodNotAllowed)              // RFC 7231, 6.5.5
	ErrNotAcceptable                 = NewError(StatusNotAcceptable)                 // RFC 7231, 6.5.6
	ErrProxyAuthRequired             = NewError(StatusProxyAuthRequired)             // RFC 7235, 3.2
	ErrRequestTimeout                = NewError(StatusRequestTimeout)                // RFC 7231, 6.5.7
	ErrConflict                      = NewError(StatusConflict)                      // RFC 7231, 6.5.8
	ErrGone                          = NewError(StatusGone)                          // RFC 7231, 6.5.9
	ErrLengthRequired                = NewError(StatusLengthRequired)                // RFC 7231, 6.5.10
	ErrPreconditionFailed            = NewError(StatusPreconditionFailed)            // RFC 7232, 4.2
	ErrRequestEntityTooLarge         = NewError(StatusRequestEntityTooLarge)         // RFC 7231, 6.5.11
	ErrRequestURITooLong             = NewError(StatusRequestURITooLong)             // RFC 7231, 6.5.12
	ErrUnsupportedMediaType          = NewError(StatusUnsupportedMediaType)          // RFC 7231, 6.5.13
	ErrRequestedRangeNotSatisfiable  = NewError(StatusRequestedRangeNotSatisfiable)  // RFC 7233, 4.4
	ErrExpectationFailed             = NewError(StatusExpectationFailed)             // RFC 7231, 6.5.14
	ErrTeapot                        = NewError(StatusTeapot)                        // RFC 7168, 2.3.3
	ErrMisdirectedRequest            = NewError(StatusMisdirectedRequest)            // RFC 7540, 9.1.2
	ErrUnprocessableEntity           = NewError(StatusUnprocessableEntity)           // RFC 4918, 11.2
	ErrLocked                        = NewError(StatusLocked)                        // RFC 4918, 11.3
	ErrFailedDependency              = NewError(StatusFailedDependency)              // RFC 4918, 11.4
	ErrTooEarly                      = NewError(StatusTooEarly)                      // RFC 8470, 5.2.
	ErrUpgradeRequired               = NewError(StatusUpgradeRequired)               // RFC 7231, 6.5.15
	ErrPreconditionRequired          = NewError(StatusPreconditionRequired)          // RFC 6585, 3
	ErrTooManyRequests               = NewError(StatusTooManyRequests)               // RFC 6585, 4
	ErrRequestHeaderFieldsTooLarge   = NewError(StatusRequestHeaderFieldsTooLarge)   // RFC 6585, 5
	ErrUnavailableForLegalReasons    = NewError(StatusUnavailableForLegalReasons)    // RFC 7725, 3
	ErrInternalServerError           = NewError(StatusInternalServerError)           // RFC 7231, 6.6.1
	ErrNotImplemented                = NewError(StatusNotImplemented)                // RFC 7231, 6.6.2
	ErrBadGateway                    = NewError(StatusBadGateway)                    // RFC 7231, 6.6.3
	ErrServiceUnavailable            = NewError(StatusServiceUnavailable)            // RFC 7231, 6.6.4
	ErrGatewayTimeout                = NewError(StatusGatewayTimeout)                // RFC 7231, 6.6.5
	ErrHTTPVersionNotSupported       = NewError(StatusHTTPVersionNotSupported)       // RFC 7231, 6.6.6
	ErrVariantAlsoNegotiates         = NewError(StatusVariantAlsoNegotiates)         // RFC 2295, 8.1
	ErrInsufficientStorage           = NewError(StatusInsufficientStorage)           // RFC 4918, 11.5
	ErrLoopDetected                  = NewError(StatusLoopDetected)                  // RFC 5842, 7.2
	ErrNotExtended                   = NewError(StatusNotExtended)                   // RFC 2774, 7
	ErrNetworkAuthenticationRequired = NewError(StatusNetworkAuthenticationRequired) // RFC 6585, 6
)

// MIME types were copied from https://github.com/nginx/nginx/blob/master/conf/mime.types
var mimeExtensions = map[string]string{
	"html":    "text/html",
	"htm":     "text/html",
	"shtml":   "text/html",
	"css":     "text/css",
	"gif":     "image/gif",
	"jpeg":    "image/jpeg",
	"jpg":     "image/jpeg",
	"xml":     "application/xml",
	"js":      "application/javascript",
	"atom":    "application/atom+xml",
	"rss":     "application/rss+xml",
	"mml":     "text/mathml",
	"txt":     "text/plain",
	"jad":     "text/vnd.sun.j2me.app-descriptor",
	"wml":     "text/vnd.wap.wml",
	"htc":     "text/x-component",
	"png":     "image/png",
	"svg":     "image/svg+xml",
	"svgz":    "image/svg+xml",
	"tif":     "image/tiff",
	"tiff":    "image/tiff",
	"wbmp":    "image/vnd.wap.wbmp",
	"webp":    "image/webp",
	"ico":     "image/x-icon",
	"jng":     "image/x-jng",
	"bmp":     "image/x-ms-bmp",
	"woff":    "font/woff",
	"woff2":   "font/woff2",
	"jar":     "application/java-archive",
	"war":     "application/java-archive",
	"ear":     "application/java-archive",
	"json":    "application/json",
	"hqx":     "application/mac-binhex40",
	"doc":     "application/msword",
	"pdf":     "application/pdf",
	"ps":      "application/postscript",
	"eps":     "application/postscript",
	"ai":      "application/postscript",
	"rtf":     "application/rtf",
	"m3u8":    "application/vnd.apple.mpegurl",
	"kml":     "application/vnd.google-earth.kml+xml",
	"kmz":     "application/vnd.google-earth.kmz",
	"xls":     "application/vnd.ms-excel",
	"eot":     "application/vnd.ms-fontobject",
	"ppt":     "application/vnd.ms-powerpoint",
	"odg":     "application/vnd.oasis.opendocument.graphics",
	"odp":     "application/vnd.oasis.opendocument.presentation",
	"ods":     "application/vnd.oasis.opendocument.spreadsheet",
	"odt":     "application/vnd.oasis.opendocument.text",
	"wmlc":    "application/vnd.wap.wmlc",
	"7z":      "application/x-7z-compressed",
	"cco":     "application/x-cocoa",
	"jardiff": "application/x-java-archive-diff",
	"jnlp":    "application/x-java-jnlp-file",
	"run":     "application/x-makeself",
	"pl":      "application/x-perl",
	"pm":      "application/x-perl",
	"prc":     "application/x-pilot",
	"pdb":     "application/x-pilot",
	"rar":     "application/x-rar-compressed",
	"rpm":     "application/x-redhat-package-manager",
	"sea":     "application/x-sea",
	"swf":     "application/x-shockwave-flash",
	"sit":     "application/x-stuffit",
	"tcl":     "application/x-tcl",
	"tk":      "application/x-tcl",
	"der":     "application/x-x509-ca-cert",
	"pem":     "application/x-x509-ca-cert",
	"crt":     "application/x-x509-ca-cert",
	"xpi":     "application/x-xpinstall",
	"xhtml":   "application/xhtml+xml",
	"xspf":    "application/xspf+xml",
	"zip":     "application/zip",
	"bin":     "application/octet-stream",
	"exe":     "application/octet-stream",
	"dll":     "application/octet-stream",
	"deb":     "application/octet-stream",
	"dmg":     "application/octet-stream",
	"iso":     "application/octet-stream",
	"img":     "application/octet-stream",
	"msi":     "application/octet-stream",
	"msp":     "application/octet-stream",
	"msm":     "application/octet-stream",
	"mid":     "audio/midi",
	"midi":    "audio/midi",
	"kar":     "audio/midi",
	"mp3":     "audio/mpeg",
	"ogg":     "audio/ogg",
	"m4a":     "audio/x-m4a",
	"ra":      "audio/x-realaudio",
	"3gpp":    "video/3gpp",
	"3gp":     "video/3gpp",
	"ts":      "video/mp2t",
	"mp4":     "video/mp4",
	"mpeg":    "video/mpeg",
	"mpg":     "video/mpeg",
	"mov":     "video/quicktime",
	"webm":    "video/webm",
	"flv":     "video/x-flv",
	"m4v":     "video/x-m4v",
	"mng":     "video/x-mng",
	"asx":     "video/x-ms-asf",
	"asf":     "video/x-ms-asf",
	"wmv":     "video/x-ms-wmv",
	"avi":     "video/x-msvideo",
}

// GetMIME returns the content-type of a file extension
func MIME(extension string) (mime string) {
	if len(extension) == 0 {
		return mime
	}
	if extension[0] == '.' {
		mime = mimeExtensions[extension[1:]]
	} else {
		mime = mimeExtensions[extension]
	}
	if len(mime) == 0 {
		return MIMEOctetStream
	}
	return mime
}

// HTTP Headers were copied from net/http.
const (
	HeaderAuthorization                   = "Authorization"
	HeaderProxyAuthenticate               = "Proxy-Authenticate"
	HeaderProxyAuthorization              = "Proxy-Authorization"
	HeaderWWWAuthenticate                 = "WWW-Authenticate"
	HeaderAge                             = "Age"
	HeaderCacheControl                    = "Cache-Control"
	HeaderClearSiteData                   = "Clear-Site-Data"
	HeaderExpires                         = "Expires"
	HeaderPragma                          = "Pragma"
	HeaderWarning                         = "Warning"
	HeaderAcceptCH                        = "Accept-CH"
	HeaderAcceptCHLifetime                = "Accept-CH-Lifetime"
	HeaderContentDPR                      = "Content-DPR"
	HeaderDPR                             = "DPR"
	HeaderEarlyData                       = "Early-Data"
	HeaderSaveData                        = "Save-Data"
	HeaderViewportWidth                   = "Viewport-Width"
	HeaderWidth                           = "Width"
	HeaderETag                            = "ETag"
	HeaderIfMatch                         = "If-Match"
	HeaderIfModifiedSince                 = "If-Modified-Since"
	HeaderIfNoneMatch                     = "If-None-Match"
	HeaderIfUnmodifiedSince               = "If-Unmodified-Since"
	HeaderLastModified                    = "Last-Modified"
	HeaderVary                            = "Vary"
	HeaderConnection                      = "Connection"
	HeaderKeepAlive                       = "Keep-Alive"
	HeaderAccept                          = "Accept"
	HeaderAcceptCharset                   = "Accept-Charset"
	HeaderAcceptEncoding                  = "Accept-Encoding"
	HeaderAcceptLanguage                  = "Accept-Language"
	HeaderCookie                          = "Cookie"
	HeaderExpect                          = "Expect"
	HeaderMaxForwards                     = "Max-Forwards"
	HeaderSetCookie                       = "Set-Cookie"
	HeaderAccessControlAllowCredentials   = "Access-Control-Allow-Credentials"
	HeaderAccessControlAllowHeaders       = "Access-Control-Allow-Headers"
	HeaderAccessControlAllowMethods       = "Access-Control-Allow-Methods"
	HeaderAccessControlAllowOrigin        = "Access-Control-Allow-Origin"
	HeaderAccessControlExposeHeaders      = "Access-Control-Expose-Headers"
	HeaderAccessControlMaxAge             = "Access-Control-Max-Age"
	HeaderAccessControlRequestHeaders     = "Access-Control-Request-Headers"
	HeaderAccessControlRequestMethod      = "Access-Control-Request-Method"
	HeaderOrigin                          = "Origin"
	HeaderTimingAllowOrigin               = "Timing-Allow-Origin"
	HeaderXPermittedCrossDomainPolicies   = "X-Permitted-Cross-Domain-Policies"
	HeaderDNT                             = "DNT"
	HeaderTk                              = "Tk"
	HeaderContentDisposition              = "Content-Disposition"
	HeaderContentEncoding                 = "Content-Encoding"
	HeaderContentLanguage                 = "Content-Language"
	HeaderContentLength                   = "Content-Length"
	HeaderContentLocation                 = "Content-Location"
	HeaderContentType                     = "Content-Type"
	HeaderForwarded                       = "Forwarded"
	HeaderVia                             = "Via"
	HeaderXForwardedFor                   = "X-Forwarded-For"
	HeaderXForwardedHost                  = "X-Forwarded-Host"
	HeaderXForwardedProto                 = "X-Forwarded-Proto"
	HeaderXForwardedProtocol              = "X-Forwarded-Protocol"
	HeaderXForwardedSsl                   = "X-Forwarded-Ssl"
	HeaderXUrlScheme                      = "X-Url-Scheme"
	HeaderLocation                        = "Location"
	HeaderFrom                            = "From"
	HeaderHost                            = "Host"
	HeaderReferer                         = "Referer"
	HeaderReferrerPolicy                  = "Referrer-Policy"
	HeaderUserAgent                       = "User-Agent"
	HeaderAllow                           = "Allow"
	HeaderServer                          = "Server"
	HeaderAcceptRanges                    = "Accept-Ranges"
	HeaderContentRange                    = "Content-Range"
	HeaderIfRange                         = "If-Range"
	HeaderRange                           = "Range"
	HeaderContentSecurityPolicy           = "Content-Security-Policy"
	HeaderContentSecurityPolicyReportOnly = "Content-Security-Policy-Report-Only"
	HeaderCrossOriginResourcePolicy       = "Cross-Origin-Resource-Policy"
	HeaderExpectCT                        = "Expect-CT"
	HeaderFeaturePolicy                   = "Feature-Policy"
	HeaderPublicKeyPins                   = "Public-Key-Pins"
	HeaderPublicKeyPinsReportOnly         = "Public-Key-Pins-Report-Only"
	HeaderStrictTransportSecurity         = "Strict-Transport-Security"
	HeaderUpgradeInsecureRequests         = "Upgrade-Insecure-Requests"
	HeaderXContentTypeOptions             = "X-Content-Type-Options"
	HeaderXDownloadOptions                = "X-Download-Options"
	HeaderXFrameOptions                   = "X-Frame-Options"
	HeaderXPoweredBy                      = "X-Powered-By"
	HeaderXXSSProtection                  = "X-XSS-Protection"
	HeaderLastEventID                     = "Last-Event-ID"
	HeaderNEL                             = "NEL"
	HeaderPingFrom                        = "Ping-From"
	HeaderPingTo                          = "Ping-To"
	HeaderReportTo                        = "Report-To"
	HeaderTE                              = "TE"
	HeaderTrailer                         = "Trailer"
	HeaderTransferEncoding                = "Transfer-Encoding"
	HeaderSecWebSocketAccept              = "Sec-WebSocket-Accept"
	HeaderSecWebSocketExtensions          = "Sec-WebSocket-Extensions"
	HeaderSecWebSocketKey                 = "Sec-WebSocket-Key"
	HeaderSecWebSocketProtocol            = "Sec-WebSocket-Protocol"
	HeaderSecWebSocketVersion             = "Sec-WebSocket-Version"
	HeaderAcceptPatch                     = "Accept-Patch"
	HeaderAcceptPushPolicy                = "Accept-Push-Policy"
	HeaderAcceptSignature                 = "Accept-Signature"
	HeaderAltSvc                          = "Alt-Svc"
	HeaderDate                            = "Date"
	HeaderIndex                           = "Index"
	HeaderLargeAllocation                 = "Large-Allocation"
	HeaderLink                            = "Link"
	HeaderPushPolicy                      = "Push-Policy"
	HeaderRetryAfter                      = "Retry-After"
	HeaderServerTiming                    = "Server-Timing"
	HeaderSignature                       = "Signature"
	HeaderSignedHeaders                   = "Signed-Headers"
	HeaderSourceMap                       = "SourceMap"
	HeaderUpgrade                         = "Upgrade"
	HeaderXDNSPrefetchControl             = "X-DNS-Prefetch-Control"
	HeaderXPingback                       = "X-Pingback"
	HeaderXRequestID                      = "X-Request-ID"
	HeaderXRequestedWith                  = "X-Requested-With"
	HeaderXRobotsTag                      = "X-Robots-Tag"
	HeaderXUACompatible                   = "X-UA-Compatible"
)

// MIME types that are commonly used
const (
	MIMETextXML               = "text/xml"
	MIMETextHTML              = "text/html"
	MIMETextPlain             = "text/plain"
	MIMEApplicationXML        = "application/xml"
	MIMEApplicationJSON       = "application/json"
	MIMEApplicationJavaScript = "application/javascript"
	MIMEApplicationForm       = "application/x-www-form-urlencoded"
	MIMEOctetStream           = "application/octet-stream"
	MIMEMultipartForm         = "multipart/form-data"

	MIMETextXMLCharsetUTF8               = "text/xml; charset=utf-8"
	MIMETextHTMLCharsetUTF8              = "text/html; charset=utf-8"
	MIMETextPlainCharsetUTF8             = "text/plain; charset=utf-8"
	MIMEApplicationXMLCharsetUTF8        = "application/xml; charset=utf-8"
	MIMEApplicationJSONCharsetUTF8       = "application/json; charset=utf-8"
	MIMEApplicationJavaScriptCharsetUTF8 = "application/javascript; charset=utf-8"
)

// HTTP methods were copied from net/http.
const (
	MethodUse     = "USE"
	MethodConnect = "CONNECT" // RFC 7231, 4.3.6
	MethodDelete  = "DELETE"  // RFC 7231, 4.3.5
	MethodGet     = "GET"     // RFC 7231, 4.3.1
	MethodHead    = "HEAD"    // RFC 7231, 4.3.2
	MethodOptions = "OPTIONS" // RFC 7231, 4.3.7
	MethodPatch   = "PATCH"   // RFC 5789
	MethodPost    = "POST"    // RFC 7231, 4.3.3
	MethodPut     = "PUT"     // RFC 7231, 4.3.4
	MethodTrace   = "TRACE"   // RFC 7231, 4.3.8
)

// Methods methods slice
var Methods = []string{
	MethodUse,
	MethodGet,
	MethodHead,
	MethodPost,
	MethodPut,
	MethodDelete,
	MethodConnect,
	MethodOptions,
	MethodTrace,
	MethodPatch,
}

// HTTP methods and their unique INTs
func methodInt(s string) int8 {
	for i, v := range Methods {
		if strings.Compare(s, v) == 0 {
			return int8(i)
		}
	}
	return int8(-1)
}

var (
	// Error for handle not support.
	ErrHandleNotSupport   = errors.New("handle does not support. Must use core.Handle, http.HandlerFunc or http.Handler")
	ErrDataTypeNotSupport = errors.New("dataType does not support")
)

// ErrInmemoryListenerClosed indicates that the InmemoryListener is already closed.
var ErrInmemoryListenerClosed = errors.New("InmemoryListener is already closed: use of closed network connection")

// InmemoryListener provides in-memory dialer<->net.Listener implementation.
//
// It may be used either for fast in-process client<->server communications
// without network stack overhead or for client<->server tests.
type InmemoryListener struct {
	lock   sync.Mutex
	closed bool
	conns  chan acceptConn
}

type acceptConn struct {
	conn     net.Conn
	accepted chan struct{}
}

// NewInmemoryListener returns new in-memory dialer<->net.Listener.
func NewInmemoryListener() *InmemoryListener {
	return &InmemoryListener{
		conns: make(chan acceptConn, 1024),
	}
}

// Accept implements net.Listener's Accept.
//
// It is safe calling Accept from concurrently running goroutines.
//
// Accept returns new connection per each Dial call.
func (ln *InmemoryListener) Accept() (net.Conn, error) {
	c, ok := <-ln.conns
	if !ok {
		return nil, ErrInmemoryListenerClosed
	}
	close(c.accepted)
	return c.conn, nil
}

// Close implements net.Listener's Close.
func (ln *InmemoryListener) Close() error {
	var err error

	ln.lock.Lock()
	if !ln.closed {
		close(ln.conns)
		ln.closed = true
	} else {
		err = ErrInmemoryListenerClosed
	}
	ln.lock.Unlock()
	return err
}

// Addr implements net.Listener's Addr.
func (ln *InmemoryListener) Addr() net.Addr {
	return &net.UnixAddr{
		Name: "InmemoryListener",
		Net:  "memory",
	}
}

// Dial creates new client<->server connection.
// Just like a real Dial it only returns once the server
// has accepted the connection.
//
// It is safe calling Dial from concurrently running goroutines.
func (ln *InmemoryListener) Dial() (net.Conn, error) {
	pc := NewPipeConns()
	cConn := pc.Conn1()
	sConn := pc.Conn2()
	ln.lock.Lock()
	accepted := make(chan struct{})
	if !ln.closed {
		ln.conns <- acceptConn{sConn, accepted}
		// Wait until the connection has been accepted.
		<-accepted
	} else {
		sConn.Close() //nolint:errcheck
		cConn.Close() //nolint:errcheck
		cConn = nil
	}
	ln.lock.Unlock()

	if cConn == nil {
		return nil, ErrInmemoryListenerClosed
	}
	return cConn, nil
}

// NewPipeConns returns new bi-directional connection pipe.
//
// PipeConns is NOT safe for concurrent use by multiple goroutines!
func NewPipeConns() *PipeConns {
	ch1 := make(chan *byteBuffer, 4)
	ch2 := make(chan *byteBuffer, 4)

	pc := &PipeConns{
		stopCh: make(chan struct{}),
	}
	pc.c1.rCh = ch1
	pc.c1.wCh = ch2
	pc.c2.rCh = ch2
	pc.c2.wCh = ch1
	pc.c1.pc = pc
	pc.c2.pc = pc
	return pc
}

// PipeConns provides bi-directional connection pipe,
// which use in-process memory as a transport.
//
// PipeConns must be created by calling NewPipeConns.
//
// PipeConns has the following additional features comparing to connections
// returned from net.Pipe():
//
//   * It is faster.
//   * It buffers Write calls, so there is no need to have concurrent goroutine
//     calling Read in order to unblock each Write call.
//   * It supports read and write deadlines.
//
// PipeConns is NOT safe for concurrent use by multiple goroutines!
type PipeConns struct {
	c1         pipeConn
	c2         pipeConn
	stopCh     chan struct{}
	stopChLock sync.Mutex
}

// Conn1 returns the first end of bi-directional pipe.
//
// Data written to Conn1 may be read from Conn2.
// Data written to Conn2 may be read from Conn1.
func (pc *PipeConns) Conn1() net.Conn {
	return &pc.c1
}

// Conn2 returns the second end of bi-directional pipe.
//
// Data written to Conn2 may be read from Conn1.
// Data written to Conn1 may be read from Conn2.
func (pc *PipeConns) Conn2() net.Conn {
	return &pc.c2
}

// Close closes pipe connections.
func (pc *PipeConns) Close() error {
	pc.stopChLock.Lock()
	select {
	case <-pc.stopCh:
	default:
		close(pc.stopCh)
	}
	pc.stopChLock.Unlock()

	return nil
}

type pipeConn struct {
	b  *byteBuffer
	bb []byte

	rCh chan *byteBuffer
	wCh chan *byteBuffer
	pc  *PipeConns

	readDeadlineTimer  *time.Timer
	writeDeadlineTimer *time.Timer

	readDeadlineCh  <-chan time.Time
	writeDeadlineCh <-chan time.Time

	readDeadlineChLock sync.Mutex
}

func (c *pipeConn) Write(p []byte) (int, error) {
	b := acquireByteBuffer()
	b.b = append(b.b[:0], p...)

	select {
	case <-c.pc.stopCh:
		releaseByteBuffer(b)
		return 0, errConnectionClosed
	default:
	}

	select {
	case c.wCh <- b:
	default:
		select {
		case c.wCh <- b:
		case <-c.writeDeadlineCh:
			c.writeDeadlineCh = closedDeadlineCh
			return 0, ErrTimeout
		case <-c.pc.stopCh:
			releaseByteBuffer(b)
			return 0, errConnectionClosed
		}
	}

	return len(p), nil
}

func (c *pipeConn) Read(p []byte) (int, error) {
	mayBlock := true
	nn := 0
	for len(p) > 0 {
		n, err := c.read(p, mayBlock)
		nn += n
		if err != nil {
			if !mayBlock && err == errWouldBlock {
				err = nil
			}
			return nn, err
		}
		p = p[n:]
		mayBlock = false
	}

	return nn, nil
}

func (c *pipeConn) read(p []byte, mayBlock bool) (int, error) {
	if len(c.bb) == 0 {
		if err := c.readNextByteBuffer(mayBlock); err != nil {
			return 0, err
		}
	}
	n := copy(p, c.bb)
	c.bb = c.bb[n:]

	return n, nil
}

func (c *pipeConn) readNextByteBuffer(mayBlock bool) error {
	releaseByteBuffer(c.b)
	c.b = nil

	select {
	case c.b = <-c.rCh:
	default:
		if !mayBlock {
			return errWouldBlock
		}
		c.readDeadlineChLock.Lock()
		readDeadlineCh := c.readDeadlineCh
		c.readDeadlineChLock.Unlock()
		select {
		case c.b = <-c.rCh:
		case <-readDeadlineCh:
			c.readDeadlineChLock.Lock()
			c.readDeadlineCh = closedDeadlineCh
			c.readDeadlineChLock.Unlock()
			// rCh may contain data when deadline is reached.
			// Read the data before returning ErrTimeout.
			select {
			case c.b = <-c.rCh:
			default:
				return ErrTimeout
			}
		case <-c.pc.stopCh:
			// rCh may contain data when stopCh is closed.
			// Read the data before returning EOF.
			select {
			case c.b = <-c.rCh:
			default:
				return io.EOF
			}
		}
	}

	c.bb = c.b.b
	return nil
}

var (
	errWouldBlock       = errors.New("would block")
	errConnectionClosed = errors.New("connection closed")
)

type timeoutError struct {
}

func (e *timeoutError) Error() string {
	return "timeout"
}

// Only implement the Timeout() function of the net.Error interface.
// This allows for checks like:
//
//   if x, ok := err.(interface{ Timeout() bool }); ok && x.Timeout() {
func (e *timeoutError) Timeout() bool {
	return true
}

var (
	// ErrTimeout is returned from Read() or Write() on timeout.
	ErrTimeout = &timeoutError{}
)

func (c *pipeConn) Close() error {
	return c.pc.Close()
}

func (c *pipeConn) LocalAddr() net.Addr {
	return pipeAddr(0)
}

func (c *pipeConn) RemoteAddr() net.Addr {
	return pipeAddr(0)
}

func (c *pipeConn) SetDeadline(deadline time.Time) error {
	c.SetReadDeadline(deadline)  //nolint:errcheck
	c.SetWriteDeadline(deadline) //nolint:errcheck
	return nil
}

func (c *pipeConn) SetReadDeadline(deadline time.Time) error {
	if c.readDeadlineTimer == nil {
		c.readDeadlineTimer = time.NewTimer(time.Hour)
	}
	readDeadlineCh := updateTimer(c.readDeadlineTimer, deadline)
	c.readDeadlineChLock.Lock()
	c.readDeadlineCh = readDeadlineCh
	c.readDeadlineChLock.Unlock()
	return nil
}

func (c *pipeConn) SetWriteDeadline(deadline time.Time) error {
	if c.writeDeadlineTimer == nil {
		c.writeDeadlineTimer = time.NewTimer(time.Hour)
	}
	c.writeDeadlineCh = updateTimer(c.writeDeadlineTimer, deadline)
	return nil
}

func updateTimer(t *time.Timer, deadline time.Time) <-chan time.Time {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	if deadline.IsZero() {
		return nil
	}
	d := -time.Since(deadline)
	if d <= 0 {
		return closedDeadlineCh
	}
	t.Reset(d)
	return t.C
}

var closedDeadlineCh = func() <-chan time.Time {
	ch := make(chan time.Time)
	close(ch)
	return ch
}()

type pipeAddr int

func (pipeAddr) Network() string {
	return "pipe"
}

func (pipeAddr) String() string {
	return "pipe"
}

type byteBuffer struct {
	b []byte
}

func acquireByteBuffer() *byteBuffer {
	return byteBufferPool.Get().(*byteBuffer)
}

func releaseByteBuffer(b *byteBuffer) {
	if b != nil {
		byteBufferPool.Put(b)
	}
}

var (
	commonInitialisms = []string{"API", "ASCII", "CPU", "CSS", "DNS", "EOF", "GUID", "HTML", "HTTP", "HTTPS", "ID", "IP", "JSON", "LHS", "QPS", "RAM", "RHS", "RPC", "SLA", "SMTP", "SSH", "TLS", "TTL", "UID", "UI", "UUID", "URI", "URL", "UTF8", "VM", "XML", "XSRF", "XSS"}

	commonInitialismsReplacer *strings.Replacer

	byteBufferPool = &sync.Pool{
		New: func() interface{} {
			return &byteBuffer{
				b: make([]byte, 1024),
			}
		},
	}
)
