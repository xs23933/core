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
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/davecgh/go-spew/spew"
	"github.com/mattn/go-isatty"
)

type LoggerConfig struct {
	ForceColor bool
	Debug      bool
	Output     io.Writer
	App        *Core
}

func Logger(conf ...LoggerConfig) HandlerFunc {
	debug := false
	if len(conf) > 0 {
		forceColor = conf[0].ForceColor
		if conf[0].Output != nil {
			// logout = conf[0].Output
			logout = &HookWriter{
				Writer:  conf[0].Output,
				Monitor: LogHub,
			}
			debug = conf[0].Debug
			conf[0].App.ErrorHandler = ErrorHandler(func(c Ctx, err error) error {
				st := c.StartAt()
				code := StatusInternalServerError
				var e *Error
				if errors.As(err, &e) {
					code = int(e.status.Code())
				}
				requestLog(debug, code, c.Method(), c.Path(), time.Since(st).String())
				c.SetHeader(HeaderContentType, MIMETextPlainCharsetUTF8)
				return c.SendStatus(code, err.Error())
			})
		}
	}
	golog = log.New(logout, "", log.Ltime)
	if w, ok := logout.(*os.File); !ok || os.Getenv("TERM") == "dumb" ||
		(!isatty.IsTerminal(w.Fd()) && !isatty.IsCygwinTerminal(w.Fd())) {
		isTerm = false
	}
	return HandlerFunc(func(c Ctx) error {
		c.StartAt(time.Now())
		err := c.Next()
		if err == nil {
			requestLog(debug, c.GetStatus(), c.Method(), c.Path(), time.Since(c.StartAt()).String())
		}
		return err
	})
}

func requestLog(debug bool, code int, method, path, ts string) {
	var color, mcolor, tcolor, rst string
	tp := info
	if !debug {
		return
	}
	if isTerm || forceColor {
		rst = reset
		mcolor = gray
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

func (Writers) Printf(f string, args ...any) {
	Log(f, args...)
}

func D(f string, args ...any) {
	var color, rst string
	if isTerm || forceColor {
		color = gray
		rst = reset
	}
	if Conf.GetBool("debug") {
		if !strings.HasSuffix(f, "\n") {
			f += "\n"
		}
		golog.Printf("%s%s%s %s", color, dbug, rst, fmt.Sprintf(f, args...))
	}
}

// Dump 打印数据信息
func Dump(v ...any) {
	spew.Dump(v...)
}

func Log(f string, args ...any) {
	golog.Printf(f, args...)
}

// info logger
func Info(f string, args ...any) {
	var color, rst string
	if isTerm || forceColor {
		color = green
		rst = reset
	}
	if !strings.HasSuffix(f, "\n") {
		f += "\n"
	}
	golog.Printf("%s%s%s %s", color, info, rst, fmt.Sprintf(f, args...))
}

// warning logger
func Warn(f string, args ...any) {
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

// error logger
func Erro(f string, args ...any) {
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
	magenta = "\033[97;45m"
	reset   = "\033[0m"
	gray    = "\033[0;90m"

	info = "[I]"
	dbug = "[D]"
	erro = "[E]"
	warn = "[W]"
)

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
type RecoveryFunc func(c Ctx, err any)

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

// CustomRecoveryWithWriter returns a middleware for a given writer that recovers from any panics and calls the provided handle func to handle it.
func CustomRecoveryWithWriter(out io.Writer, handle RecoveryFunc) HandlerFunc {
	var logger *log.Logger
	if out != nil {
		logger = log.New(out, "\n\n\x1b[31m", log.LstdFlags)
	}
	return func(c Ctx) error {
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
					httpRequest, _ := httputil.DumpRequest(c.Request(), false)
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
		return c.Next()
	}
}

func defaultHandleRecovery(c Ctx, err any) {
	c.Abort(http.StatusInternalServerError)
}

// DefaultErrorWriter is the default io.Writer used by Gin to debug errors
var (
	DefaultErrorWriter io.Writer = os.Stderr
	LogHub                       = NewLogMonitor()
)

type HookWriter struct {
	Writer  io.Writer
	Monitor *LogMonitor
}

func (h *HookWriter) Write(p []byte) (n int, err error) {
	if h.Monitor != nil {
		h.Monitor.Broadcast(string(p))
	}

	n, err = h.Writer.Write(p)
	if err != nil {
		return n, err
	}
	return n, nil
}

type LogMonitor struct {
	mu      sync.RWMutex
	clients map[chan string]struct{}
}

func NewLogMonitor() *LogMonitor {
	return &LogMonitor{
		clients: make(map[chan string]struct{}),
	}
}

func (m *LogMonitor) Register(c chan string) {
	m.mu.Lock()
	m.clients[c] = struct{}{}
	m.mu.Unlock()
}

func (m *LogMonitor) UnRegister(c chan string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.clients[c]; ok {
		delete(m.clients, c)
		close(c)
	}
}

func (m *LogMonitor) Broadcast(msg string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.clients) == 0 {
		return
	}
	for c := range m.clients {
		select {
		case c <- msg:
			continue
		default:
			// 丢弃阻塞客户端,避免影响整体
		}
	}
}

type EventData struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

func (h EventData) String() string {
	if h.Event == "" {
		return fmt.Sprintf("data: %v\n\n", h.ToString())
	}
	return fmt.Sprintf("event: %s\ndata: %v\n\n", h.Event, h.ToString())
}

func (h *EventData) ToString() string {
	switch v := h.Data.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int, int8, int16, int32, int64, Int:
		return fmt.Sprint(v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(v)
	case float32, float64:
		return fmt.Sprint(v)
	default:
		dat, _ := sonic.MarshalString(v)
		return dat
	}
}

type EventHub struct {
	mu       sync.RWMutex
	clients  map[chan EventData]struct{}
	queue    chan EventData
	interval time.Duration
}

func NewEventHub(interval ...time.Duration) *EventHub {
	itv := time.Millisecond * 1000
	if len(interval) > 0 {
		itv = interval[0]
	}
	h := &EventHub{
		clients:  make(map[chan EventData]struct{}),
		queue:    make(chan EventData, 1000), // 缓存,避免阻塞
		interval: itv,
	}

	go h.start()
	return h
}

func (h *EventHub) start() {
	Info("event hub run with %ds batch interval", h.interval.Seconds())
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	var buffer []EventData
	for {
		select {
		case data, ok := <-h.queue:
			if !ok {
				return
			}
			buffer = append(buffer, data)
		case <-ticker.C:
			if len(buffer) == 0 {
				continue
			}

			if len(buffer) == 1 {
				h.broadcast(buffer[0])
			} else {
				// 打包成一个批次事件
				batch := EventData{
					Event: "batch",
					Data:  buffer,
				}
				h.broadcast(batch)
			}
			buffer = nil
		}
	}
}

func (h *EventHub) Register(c chan EventData) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *EventHub) UnRegister(c chan EventData) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c)
	}
}

func (h *EventHub) broadcast(data EventData) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c <- data:
			continue
		default:
		}
	}
}

func (h *EventHub) Broadcast(data EventData) {
	select {
	case h.queue <- data:
		return
	default:
	}
}

func (h *EventHub) Get(c Ctx) {
	c.SetHeader("Content-Type", "text/event-stream;charset=utf-8")
	c.SetHeader("Cache-Control", "no-cache")
	c.SetHeader("Connection", "keep-alive")

	ch := make(chan EventData, 100)
	h.Register(ch)
	defer h.UnRegister(ch)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	c.Stream(func(w io.Writer) bool {
		_, _ = fmt.Fprint(w, "event: touch\ndata: hi\n\n")
		return false
	})

	c.Stream(func(w io.Writer) bool {
		select {
		case msg, ok := <-ch:
			if !ok {
				return false
			}
			if _, err := fmt.Fprint(w, msg.String()); err != nil {
				return false
			}
		case <-ticker.C:
			if _, err := fmt.Fprintf(w, ":\n\n"); err != nil { // SSE 心跳标识 ping
				return false
			}
		}
		return true
	})
}

func (h *EventHub) PostData(c Ctx) {
	data := EventData{}
	if err := c.ReadBody(&data); err != nil {
		c.ToJSON(nil, err)
		return
	}
	go h.Broadcast(data)
	c.ToJSON(data, nil)
}
