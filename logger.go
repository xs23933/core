package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
)

type LoggerConfig struct {
	ForceColor bool
	Output     io.Writer
}

func Logger(conf ...LoggerConfig) HandlerFunc {
	if len(conf) > 0 {
		forceColor = conf[0].ForceColor
		if conf[0].Output != nil {
			logout = conf[0].Output
		}
	}
	golog = log.New(logout, "", log.Ltime)
	if w, ok := logout.(*os.File); !ok || os.Getenv("TERM") == "dumb" ||
		(!isatty.IsTerminal(w.Fd()) && !isatty.IsCygwinTerminal(w.Fd())) {
		isTerm = false
	}
	return HandlerFunc(func(c *Ctx) {
		st := time.Now()
		c.Next()
		requestLog(c.GetStatus(), c.Method(), c.Path(), time.Since(st).String())
	})
}

func requestLog(code int, method, path, ts string) {
	var color, mcolor, tcolor, rst string
	tp := info
	if !Conf.GetBool("debug") {
		return
	}
	if isTerm || forceColor {
		rst = reset
		mcolor = yellow
		tcolor = magenta
		switch {
		case code >= http.StatusOK && code < http.StatusMultipleChoices:
			color = green
		case code >= http.StatusMultipleChoices && code < http.StatusBadRequest:
			color = yellow
			tp = warn
			rst = reset
		case code >= http.StatusBadRequest && code < http.StatusInternalServerError:
			color = red
			tp = erro
			rst = reset
		}
	}

	golog.Printf("%s%s%s %d %s%s%s %s %s%s%s\n", color, tp, rst, code, mcolor, method, rst, path, tcolor, ts, rst)
}

type Writers struct{}

func (Writers) Printf(f string, args ...interface{}) {
	Log(f, args...)
}

func D(f string, args ...any) {
	var color, rst string
	if isTerm || forceColor {
		color = yellow
		rst = reset
	}
	if Conf.GetBool("debug") {
		if !strings.HasSuffix(f, "\n") {
			f += "\n"
		}
		golog.Printf("%s%s%s %s", color, dbug, rst, fmt.Sprintf(f, args...))
	}
}

func Log(f string, args ...interface{}) {
	golog.Printf(f, args...)
}

func Warn(f string, args ...interface{}) {
	var color, rst string
	if isTerm || forceColor {
		color = yellow
		rst = reset
	}
	if !strings.HasSuffix(f, "\n") {
		f += "\n"
	}
	golog.Printf("%s%s%s %s", color, warn, rst, fmt.Sprintf(f, args...))
}

func Erro(f string, args ...interface{}) {
	var color, rst string
	if isTerm || forceColor {
		color = red
		rst = reset
	}
	if !strings.HasSuffix(f, "\n") {
		f += "\n"
	}
	golog.Printf("%s%s%s %s", color, erro, rst, fmt.Sprintf(f, args...))
}

var (
	DefaultOutput           = io.Discard
	logout        io.Writer = DefaultOutput
	golog                   = log.New(logout, "", log.Ltime)
	isTerm                  = true
	forceColor              = true
)

const (
	green   = "\033[97;32m"
	yellow  = "\033[90;43m"
	red     = "\033[97;41m"
	blue    = "\033[97;44m"
	magenta = "\033[97;45m"
	cyan    = "\033[97;41m"
	reset   = "\033[0m"

	info = "[INFO]"
	dbug = "[DBUG]"
	trac = "[TRAC]"
	erro = "[ERRO]"
	warn = "[WARN]"
)

// CustomRecoveryWithWriter returns a middleware for a given writer that recovers from any panics and calls the provided handle func to handle it.
func CustomRecoveryWithWriter(out io.Writer, handle RecoveryFunc) HandlerFunc {
	var logger *log.Logger
	if out != nil {
		logger = log.New(out, "\n\n\x1b[31m", log.LstdFlags)
	}
	return func(c *Ctx) {
		defer func() {
			if err := recover(); err != nil {
				// Check for a broken connection, as it is not really a
				// condition that warrants a panic stack trace.
				var brokenPipe bool
				if ne, ok := err.(*net.OpError); ok {
					var se *os.SyscallError
					if errors.As(ne, &se) {
						if strings.Contains(strings.ToLower(se.Error()), "broken pipe") || strings.Contains(strings.ToLower(se.Error()), "connection reset by peer") {
							brokenPipe = true
						}
					}
				}
				if logger != nil {
					stack := stack(3)
					httpRequest, _ := httputil.DumpRequest(c.R, false)
					headers := strings.Split(string(httpRequest), "\r\n")
					for idx, header := range headers {
						current := strings.Split(header, ":")
						if current[0] == "Authorization" {
							headers[idx] = current[0] + ": *"
						}
					}
					headersToStr := strings.Join(headers, "\r\n")
					if brokenPipe {
						logger.Printf("%s\n%s%s", err, headersToStr, reset)
					} else if c.Core().Debug {
						logger.Printf("[Recovery] %s panic recovered:\n%s\n%s\n%s%s",
							timeFormat(time.Now()), headersToStr, err, stack, reset)
					} else {
						logger.Printf("[Recovery] %s panic recovered:\n%s\n%s%s",
							timeFormat(time.Now()), err, stack, reset)
					}
				}
				if brokenPipe {
					// If the connection is dead, we can't write a status to it.
					c.Abort()
				} else {
					handle(c, err)
				}
			}
		}()
		c.Next()
	}
}

// timeFormat returns a customized time string for logger.
func timeFormat(t time.Time) string {
	return t.Format("2006/01/02 - 15:04:05")
}

// stack returns a nicely formatted stack frame, skipping skip frames.
func stack(skip int) []byte {
	buf := new(bytes.Buffer) // the returned data
	// As we loop, we open files and read them. These variables record the currently
	// loaded file.
	var lines [][]byte
	var lastFile string
	for i := skip; ; i++ { // Skip the expected number of frames
		pc, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		// Print this much at least.  If we can't find the source, it won't show.
		fmt.Fprintf(buf, "%s:%d (0x%x)\n", file, line, pc)
		if file != lastFile {
			data, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			lines = bytes.Split(data, []byte{'\n'})
			lastFile = file
		}
		fmt.Fprintf(buf, "\t%s: %s\n", function(pc), source(lines, line))
	}
	return buf.Bytes()
}

// source returns a space-trimmed slice of the n'th line.
func source(lines [][]byte, n int) []byte {
	n-- // in stack trace, lines are 1-indexed but our array is 0-indexed
	if n < 0 || n >= len(lines) {
		return dunno
	}
	return bytes.TrimSpace(lines[n])
}

// function returns, if possible, the name of the function containing the PC.
func function(pc uintptr) []byte {
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return dunno
	}
	name := []byte(fn.Name())
	// The name includes the path name to the package, which is unnecessary
	// since the file name is already included.  Plus, it has center dots.
	// That is, we see
	//	runtime/debug.*T·ptrmethod
	// and want
	//	*T.ptrmethod
	// Also the package path might contains dot (e.g. code.google.com/...),
	// so first eliminate the path prefix
	if lastSlash := bytes.LastIndex(name, slash); lastSlash >= 0 {
		name = name[lastSlash+1:]
	}
	if period := bytes.Index(name, dot); period >= 0 {
		name = name[period+1:]
	}
	name = bytes.Replace(name, centerDot, dot, -1)
	return name
}

var (
	dunno     = []byte("???")
	centerDot = []byte("·")
	dot       = []byte(".")
	slash     = []byte("/")
)

// RecoveryFunc defines the function passable to CustomRecovery.
type RecoveryFunc func(c *Ctx, err interface{})

// Recovery returns a middleware that recovers from any panics and writes a 500 if there was one.
func Recovery() HandlerFunc {
	return RecoveryWithWriter(DefaultErrorWriter)
}

// RecoveryWithWriter returns a middleware for a given writer that recovers from any panics and writes a 500 if there was one.
func RecoveryWithWriter(out io.Writer, recovery ...RecoveryFunc) HandlerFunc {
	if len(recovery) > 0 {
		return CustomRecoveryWithWriter(out, recovery[0])
	}
	return CustomRecoveryWithWriter(out, defaultHandleRecovery)
}
func defaultHandleRecovery(c *Ctx, err interface{}) {
	c.Abort(http.StatusInternalServerError)
}

// DefaultErrorWriter is the default io.Writer used by Gin to debug errors
var DefaultErrorWriter io.Writer = os.Stderr
