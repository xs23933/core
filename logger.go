package core

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

type LogRotateConfig struct {
	MaxSize       int64
	Daily         bool
	RetainDays    int
	Compress      bool
	CopyTruncate  bool
	DelayCompress time.Duration
	MissingOK     bool
	NotifEmpty    bool
}

var DefaultLogRotateOptions = []string{
	"size: 300M",
	"daily",
	"rotate: 30",
	"compress",
	"delaycompress: 24h",
	"missingok",
	"notifempty",
	"copytruncate",
}

type RotatingLogWriter struct {
	mu             sync.Mutex
	path           string
	file           *os.File
	cfg            LogRotateConfig
	size           int64
	day            string
	redirectStdout bool
	nowFunc        func() time.Time
}

func LogRotateOptionsFromConfig(conf Options) []string {
	raw, ok := conf["log_rotate"]
	if !ok || raw == nil {
		return append([]string(nil), DefaultLogRotateOptions...)
	}

	clean := normalizeLogRotateOptions(raw)
	if len(clean) == 0 {
		return append([]string(nil), DefaultLogRotateOptions...)
	}
	return clean
}

func normalizeLogRotateOptions(raw any) []string {
	switch v := raw.(type) {
	case string:
		return cleanLogRotateStrings([]string{v})
	case []string:
		return cleanLogRotateStrings(v)
	case []any:
		clean := make([]string, 0, len(v))
		for _, item := range v {
			if opt := normalizeLogRotateOption(item); opt != "" {
				clean = append(clean, opt)
			}
		}
		return clean
	default:
		if opt := normalizeLogRotateOption(v); opt != "" {
			return []string{opt}
		}
		return nil
	}
}

func cleanLogRotateStrings(opts []string) []string {
	clean := make([]string, 0, len(opts))
	for _, opt := range opts {
		if strings.TrimSpace(opt) != "" {
			clean = append(clean, strings.TrimSpace(opt))
		}
	}
	return clean
}

func normalizeLogRotateOption(item any) string {
	switch v := item.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		return logRotateMapOption(v)
	case map[string]string:
		m := make(map[string]any, len(v))
		for key, val := range v {
			m[key] = val
		}
		return logRotateMapOption(m)
	case Options:
		return logRotateMapOption(map[string]any(v))
	case map[any]any:
		m := make(map[string]any, len(v))
		for key, val := range v {
			m[fmt.Sprint(key)] = val
		}
		return logRotateMapOption(m)
	default:
		return ""
	}
}

func logRotateMapOption(m map[string]any) string {
	if len(m) != 1 {
		return ""
	}
	for key, val := range m {
		key = strings.TrimSpace(key)
		if key == "" {
			return ""
		}
		if val == nil {
			return key
		}
		value := strings.TrimSpace(fmt.Sprint(val))
		if value == "" {
			return key
		}
		return key + ": " + value
	}
	return ""
}

func splitLogRotateOption(opt string) (string, string, bool, error) {
	opt = strings.TrimSpace(opt)
	if opt == "" {
		return "", "", false, nil
	}
	if strings.Contains(opt, ":") {
		key, value, ok := strings.Cut(opt, ":")
		if !ok {
			return "", "", false, fmt.Errorf("invalid log rotate option %q", opt)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			return "", "", false, fmt.Errorf("invalid log rotate option %q", opt)
		}
		if strings.Contains(key, " ") {
			return "", "", false, fmt.Errorf("invalid log rotate option key %q", key)
		}
		return key, value, true, nil
	}
	fields := strings.Fields(opt)
	if len(fields) == 0 {
		return "", "", false, nil
	}
	if len(fields) != 1 {
		return "", "", false, fmt.Errorf("log rotate option %q must use YAML syntax key: value", opt)
	}
	return fields[0], "", false, nil
}

func ParseLogRotateConfig(opts ...string) (LogRotateConfig, error) {
	var cfg LogRotateConfig
	for _, opt := range opts {
		key, value, hasValue, err := splitLogRotateOption(opt)
		if err != nil {
			return cfg, err
		}
		if key == "" {
			continue
		}
		switch strings.ToLower(key) {
		case "size":
			if !hasValue {
				return cfg, fmt.Errorf("log rotate size expects YAML value")
			}
			size, err := parseLogSize(value)
			if err != nil {
				return cfg, err
			}
			cfg.MaxSize = size
		case "daily":
			if hasValue {
				return cfg, fmt.Errorf("log rotate daily does not accept values")
			}
			cfg.Daily = true
		case "rotate":
			if !hasValue {
				return cfg, fmt.Errorf("log rotate rotate expects YAML day count")
			}
			days, err := strconv.Atoi(value)
			if err != nil || days < 0 {
				return cfg, fmt.Errorf("invalid log rotate retention days %q", value)
			}
			cfg.RetainDays = days
		case "compress":
			if hasValue {
				return cfg, fmt.Errorf("log rotate compress does not accept values")
			}
			cfg.Compress = true
		case "copytruncate":
			if hasValue {
				return cfg, fmt.Errorf("log rotate copytruncate does not accept values")
			}
			cfg.CopyTruncate = true
		case "delaycompress":
			delay := 24 * time.Hour
			if hasValue {
				var err error
				delay, err = time.ParseDuration(value)
				if err != nil || delay <= 0 {
					return cfg, fmt.Errorf("invalid log rotate delaycompress duration %q", value)
				}
			}
			cfg.DelayCompress = delay
		case "missingok":
			if hasValue {
				return cfg, fmt.Errorf("log rotate missingok does not accept values")
			}
			cfg.MissingOK = true
		case "notifempty":
			if hasValue {
				return cfg, fmt.Errorf("log rotate notifempty does not accept values")
			}
			cfg.NotifEmpty = true
		default:
			return cfg, fmt.Errorf("unknown log rotate option %q", key)
		}
	}
	if cfg.DelayCompress > 0 && !cfg.Compress {
		return cfg, fmt.Errorf("log rotate delaycompress requires compress")
	}
	return cfg, nil
}

func NewRotatingLogWriter(path string, opts ...string) (*RotatingLogWriter, error) {
	cfg, err := ParseLogRotateConfig(opts...)
	if err != nil {
		return nil, err
	}
	return NewRotatingLogWriterConfig(path, cfg)
}

func NewRotatingLogWriterConfig(path string, cfg LogRotateConfig) (*RotatingLogWriter, error) {
	if path == "" {
		return nil, fmt.Errorf("log path is required")
	}
	if cfg.MaxSize < 0 {
		return nil, fmt.Errorf("log rotate size cannot be negative")
	}
	if cfg.RetainDays < 0 {
		return nil, fmt.Errorf("log rotate retention days cannot be negative")
	}
	w := &RotatingLogWriter{
		path:    path,
		cfg:     cfg,
		nowFunc: time.Now,
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cfg.Compress && w.cfg.DelayCompress > 0 {
		if err := w.compressDue(w.now()); err != nil {
			return 0, err
		}
	}
	if err := w.rotateIfNeeded(len(p)); err != nil {
		return 0, err
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingLogWriter) RedirectStdout() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		if err := w.open(); err != nil {
			return err
		}
	}
	w.redirectStdout = true
	os.Stdout = w.file
	return nil
}

func (w *RotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *RotatingLogWriter) open() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0640)
	if err != nil {
		return err
	}
	old := w.file
	w.file = file
	w.day = logDay(w.now())
	if st, err := file.Stat(); err == nil {
		w.size = st.Size()
	}
	if w.redirectStdout {
		os.Stdout = file
	}
	if old != nil && old != file {
		if err := old.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (w *RotatingLogWriter) rotateIfNeeded(incoming int) error {
	now := w.now()
	if w.file != nil {
		if st, err := w.file.Stat(); err == nil {
			w.size = st.Size()
		} else if errors.Is(err, os.ErrNotExist) && w.cfg.MissingOK {
			w.size = 0
		}
	}
	if w.cfg.Daily && w.day != "" && logDay(now) != w.day {
		return w.rotate(now)
	}
	if w.cfg.MaxSize > 0 && w.size+int64(incoming) > w.cfg.MaxSize {
		return w.rotate(now)
	}
	return nil
}

func (w *RotatingLogWriter) rotate(now time.Time) error {
	if w.file == nil {
		return w.open()
	}

	if st, err := os.Stat(w.path); err != nil {
		if errors.Is(err, os.ErrNotExist) && w.cfg.MissingOK {
			return w.reopenAfterMissing(now)
		}
		return err
	} else if w.cfg.NotifEmpty && st.Size() == 0 {
		w.size = 0
		w.day = logDay(now)
		return nil
	}

	rotated := nextRotatedLogPath(w.path, now)
	if w.cfg.CopyTruncate {
		if err := copyFile(w.path, rotated); err != nil {
			if errors.Is(err, os.ErrNotExist) && w.cfg.MissingOK {
				return w.reopenAfterMissing(now)
			}
			return err
		}
		if err := w.file.Truncate(0); err != nil {
			return err
		}
		if err := w.open(); err != nil {
			return err
		}
	} else {
		if _, err := os.Stat(w.path); err == nil {
			if err := os.Rename(w.path, rotated); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := w.open(); err != nil {
			if _, statErr := os.Stat(w.path); errors.Is(statErr, os.ErrNotExist) {
				_ = os.Rename(rotated, w.path)
			}
			return err
		}
	}

	if w.cfg.Compress {
		if err := w.compressRotated(rotated, now); err != nil {
			return err
		}
	}
	w.size = 0
	w.day = logDay(now)
	return w.clean(now)
}

func (w *RotatingLogWriter) reopenAfterMissing(now time.Time) error {
	w.size = 0
	w.day = logDay(now)
	return w.open()
}

func (w *RotatingLogWriter) compressRotated(path string, now time.Time) error {
	if w.cfg.DelayCompress > 0 {
		return w.compressDue(now)
	}
	if err := gzipFile(path); err != nil && !(w.cfg.MissingOK && errors.Is(err, os.ErrNotExist)) {
		return err
	}
	return nil
}

func (w *RotatingLogWriter) compressDue(now time.Time) error {
	files, err := rotatedLogFiles(w.path)
	if err != nil {
		return err
	}
	cutoff := now.Add(-w.cfg.DelayCompress)
	for _, file := range files {
		if strings.HasSuffix(file, ".gz") {
			continue
		}
		st, err := os.Stat(file)
		if err != nil {
			if w.cfg.MissingOK && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if st.ModTime().After(cutoff) {
			continue
		}
		if w.cfg.NotifEmpty && st.Size() == 0 {
			continue
		}
		if err := gzipFile(file); err != nil {
			if w.cfg.MissingOK && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
	}
	return nil
}

func (w *RotatingLogWriter) clean(now time.Time) error {
	if w.cfg.RetainDays <= 0 {
		return nil
	}
	cutoff := now.Add(-time.Duration(w.cfg.RetainDays) * 24 * time.Hour)
	files, err := rotatedLogFiles(w.path)
	if err != nil {
		return err
	}
	for _, file := range files {
		st, err := os.Stat(file)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if st.ModTime().Before(cutoff) {
			if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func (w *RotatingLogWriter) now() time.Time {
	if w.nowFunc != nil {
		return w.nowFunc()
	}
	return time.Now()
}

func parseLogSize(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("log rotate size is required")
	}
	unit := int64(1)
	raw := strings.TrimSpace(strings.ToUpper(s))
	for _, suffix := range []struct {
		text string
		mul  int64
	}{
		{"KB", 1024}, {"K", 1024},
		{"MB", 1024 * 1024}, {"M", 1024 * 1024},
		{"GB", 1024 * 1024 * 1024}, {"G", 1024 * 1024 * 1024},
		{"B", 1},
	} {
		if strings.HasSuffix(raw, suffix.text) {
			unit = suffix.mul
			raw = strings.TrimSpace(strings.TrimSuffix(raw, suffix.text))
			break
		}
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid log rotate size %q", s)
	}
	return n * unit, nil
}

func rotatedLogPath(path string, t time.Time) string {
	return fmt.Sprintf("%s.%s", path, t.Format("20060102-150405.000000000"))
}

func nextRotatedLogPath(path string, t time.Time) string {
	base := rotatedLogPath(path, t)
	if !logFileExists(base) && !logFileExists(base+".gz") {
		return base
	}
	for i := 1; ; i++ {
		next := fmt.Sprintf("%s.%d", base, i)
		if !logFileExists(next) && !logFileExists(next+".gz") {
			return next
		}
	}
}

func logFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func rotatedLogFiles(path string) ([]string, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	files := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, base) {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	return files, nil
}

func logDay(t time.Time) string {
	return t.Format("2006-01-02")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func gzipFile(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(path+".gz", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	_, copyErr := io.Copy(gz, in)
	closeGzErr := gz.Close()
	closeOutErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeGzErr != nil {
		return closeGzErr
	}
	if closeOutErr != nil {
		return closeOutErr
	}
	return os.Remove(path)
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
	mu      sync.Mutex
	clients atomic.Value // map[chan string]struct{}, copy-on-write
}

func NewLogMonitor() *LogMonitor {
	m := &LogMonitor{}
	m.storeClients(make(map[chan string]struct{}))
	return m
}

func (m *LogMonitor) loadClients() map[chan string]struct{} {
	if clients, ok := m.clients.Load().(map[chan string]struct{}); ok && clients != nil {
		return clients
	}
	return nil
}

func (m *LogMonitor) storeClients(clients map[chan string]struct{}) {
	m.clients.Store(clients)
}

func (m *LogMonitor) Register(c chan string) {
	m.mu.Lock()
	clients := m.loadClients()
	next := make(map[chan string]struct{}, len(clients)+1)
	for ch := range clients {
		next[ch] = struct{}{}
	}
	next[c] = struct{}{}
	m.storeClients(next)
	m.mu.Unlock()
}

func (m *LogMonitor) UnRegister(c chan string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	clients := m.loadClients()
	if _, ok := clients[c]; !ok {
		return
	}
	next := make(map[chan string]struct{}, len(clients)-1)
	for ch := range clients {
		if ch != c {
			next[ch] = struct{}{}
		}
	}
	m.storeClients(next)
	close(c)
}

func (m *LogMonitor) Broadcast(msg string) {
	clients := m.loadClients()
	if len(clients) == 0 {
		return
	}
	for c := range clients {
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
	mu       sync.Mutex
	clients  atomic.Value // map[chan EventData]struct{}, copy-on-write
	queue    chan EventData
	interval time.Duration
}

func NewEventHub(interval ...time.Duration) *EventHub {
	itv := time.Millisecond * 1000
	if len(interval) > 0 {
		itv = interval[0]
	}
	h := &EventHub{
		queue:    make(chan EventData, 1000), // 缓存,避免阻塞
		interval: itv,
	}
	h.storeClients(make(map[chan EventData]struct{}))

	go h.start()
	return h
}

func (h *EventHub) loadClients() map[chan EventData]struct{} {
	if clients, ok := h.clients.Load().(map[chan EventData]struct{}); ok && clients != nil {
		return clients
	}
	return nil
}

func (h *EventHub) storeClients(clients map[chan EventData]struct{}) {
	h.clients.Store(clients)
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
	clients := h.loadClients()
	next := make(map[chan EventData]struct{}, len(clients)+1)
	for ch := range clients {
		next[ch] = struct{}{}
	}
	next[c] = struct{}{}
	h.storeClients(next)
	h.mu.Unlock()
}

func (h *EventHub) UnRegister(c chan EventData) {
	h.mu.Lock()
	defer h.mu.Unlock()
	clients := h.loadClients()
	if _, ok := clients[c]; !ok {
		return
	}
	next := make(map[chan EventData]struct{}, len(clients)-1)
	for ch := range clients {
		if ch != c {
			next[ch] = struct{}{}
		}
	}
	h.storeClients(next)
	close(c)
}

func (h *EventHub) broadcast(data EventData) {
	for c := range h.loadClients() {
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
