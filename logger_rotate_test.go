package core

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseLogRotateConfig(t *testing.T) {
	cfg, err := ParseLogRotateConfig("size: 300M", "daily", "rotate: 30", "compress", "copytruncate", "delaycompress: 2h", "missingok", "notifempty")
	if err != nil {
		t.Fatalf("parse log rotate config: %v", err)
	}
	if cfg.MaxSize != 300*1024*1024 {
		t.Fatalf("max size = %d, want %d", cfg.MaxSize, 300*1024*1024)
	}
	if !cfg.Daily || cfg.RetainDays != 30 || !cfg.Compress || !cfg.CopyTruncate || cfg.DelayCompress != 2*time.Hour || !cfg.MissingOK || !cfg.NotifEmpty {
		t.Fatalf("parsed config = %+v", cfg)
	}
}

func TestDefaultLogRotateOptions(t *testing.T) {
	cfg, err := ParseLogRotateConfig(DefaultLogRotateOptions...)
	if err != nil {
		t.Fatalf("parse default log rotate options: %v", err)
	}
	if cfg.MaxSize != 300*1024*1024 {
		t.Fatalf("default max size = %d, want %d", cfg.MaxSize, 300*1024*1024)
	}
	if !cfg.Daily || cfg.RetainDays != 30 || !cfg.Compress || cfg.DelayCompress != 24*time.Hour || !cfg.MissingOK || !cfg.NotifEmpty || !cfg.CopyTruncate {
		t.Fatalf("default config = %+v", cfg)
	}
}

func TestLogRotateOptionsFromConfigDefaults(t *testing.T) {
	tests := []struct {
		name string
		conf Options
	}{
		{name: "missing", conf: Options{}},
		{name: "nil", conf: Options{"log_rotate": nil}},
		{name: "empty string", conf: Options{"log_rotate": ""}},
		{name: "blank string", conf: Options{"log_rotate": "   "}},
		{name: "empty slice", conf: Options{"log_rotate": []string{}}},
		{name: "blank slice", conf: Options{"log_rotate": []string{"", "   "}}},
		{name: "empty any slice", conf: Options{"log_rotate": []any{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := LogRotateOptionsFromConfig(tt.conf)
			if strings.Join(opts, "|") != strings.Join(DefaultLogRotateOptions, "|") {
				t.Fatalf("opts = %v, want defaults %v", opts, DefaultLogRotateOptions)
			}
		})
	}
}

func TestLogRotateOptionsFromConfigExplicit(t *testing.T) {
	opts := LogRotateOptionsFromConfig(Options{"log_rotate": []any{
		map[string]any{"size": "1M"},
		"",
		"daily",
		map[string]any{"rotate": 7},
		map[string]string{"delaycompress": "2h"},
	}})
	if strings.Join(opts, "|") != "size: 1M|daily|rotate: 7|delaycompress: 2h" {
		t.Fatalf("explicit opts = %v", opts)
	}
}

func TestNewWithInvalidLogRotateConfigPanicsBeforeNilWriter(t *testing.T) {
	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("New did not panic for invalid log_rotate config")
		}
	}()

	New(Options{
		"debug":      false,
		"log":        filepath.Join(t.TempDir(), "app.log"),
		"log_rotate": []any{map[string]any{"delaycompress": "24h"}},
	})
}

func TestParseLogRotateConfigRejectsOldSpaceSyntax(t *testing.T) {
	if _, err := ParseLogRotateConfig("size 300M"); err == nil {
		t.Fatal("old size syntax accepted, want error")
	}
	if _, err := ParseLogRotateConfig("rotate 30"); err == nil {
		t.Fatal("old rotate syntax accepted, want error")
	}
	if _, err := ParseLogRotateConfig("delaycompress 24h"); err == nil {
		t.Fatal("old delaycompress syntax accepted, want error")
	}
}

func TestRotatingLogWriterSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "size: 10")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	w.nowFunc = fixedTime(time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC))

	if _, err := w.Write([]byte("12345678")); err != nil {
		t.Fatalf("write first log: %v", err)
	}
	before := mustStat(t, path)
	if _, err := w.Write([]byte("abcde")); err != nil {
		t.Fatalf("write second log: %v", err)
	}
	after := mustStat(t, path)
	if os.SameFile(before, after) {
		t.Fatal("non-redirected rename rotation kept the active inode")
	}

	active := mustReadFile(t, path)
	if active != "abcde" {
		t.Fatalf("active log = %q, want second write", active)
	}
	rotated := listRotatedLogs(t, path)
	if len(rotated) != 1 {
		t.Fatalf("rotated logs = %v, want 1", rotated)
	}
	if got := mustReadFile(t, rotated[0]); got != "12345678" {
		t.Fatalf("rotated log = %q, want first write", got)
	}
}

func TestRotatingLogWriterDaily(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "daily")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()

	t1 := time.Date(2026, 5, 18, 23, 59, 0, 0, time.UTC)
	w.nowFunc = fixedTime(t1)
	w.day = logDay(t1)
	if _, err := w.Write([]byte("day1")); err != nil {
		t.Fatalf("write day1: %v", err)
	}

	w.nowFunc = fixedTime(t1.Add(2 * time.Minute))
	if _, err := w.Write([]byte("day2")); err != nil {
		t.Fatalf("write day2: %v", err)
	}

	if got := mustReadFile(t, path); got != "day2" {
		t.Fatalf("active daily log = %q, want day2", got)
	}
	rotated := listRotatedLogs(t, path)
	if len(rotated) != 1 {
		t.Fatalf("rotated logs = %v, want 1", rotated)
	}
	if got := mustReadFile(t, rotated[0]); got != "day1" {
		t.Fatalf("rotated daily log = %q, want day1", got)
	}
}

func TestRotatingLogWriterCompress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "size: 4", "compress")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	w.nowFunc = fixedTime(time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC))

	if _, err := w.Write([]byte("old")); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if _, err := w.Write([]byte("next")); err != nil {
		t.Fatalf("write next: %v", err)
	}

	files := listRotatedLogs(t, path)
	if len(files) != 1 || !strings.HasSuffix(files[0], ".gz") {
		t.Fatalf("compressed rotated logs = %v, want one .gz", files)
	}
	if got := mustReadGzip(t, files[0]); got != "old" {
		t.Fatalf("compressed content = %q, want old", got)
	}
}

func TestRotatingLogWriterCopyTruncate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "size: 4", "copytruncate")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	w.nowFunc = fixedTime(time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC))

	if _, err := w.Write([]byte("old")); err != nil {
		t.Fatalf("write old: %v", err)
	}
	before := mustStat(t, path)
	if _, err := w.Write([]byte("next")); err != nil {
		t.Fatalf("write next: %v", err)
	}
	after := mustStat(t, path)
	if !os.SameFile(before, after) {
		t.Fatal("copytruncate replaced active log file, want same file")
	}
	if got := mustReadFile(t, path); got != "next" {
		t.Fatalf("active copytruncate log = %q, want next", got)
	}
	rotated := listRotatedLogs(t, path)
	if len(rotated) != 1 {
		t.Fatalf("rotated logs = %v, want 1", rotated)
	}
	if got := mustReadFile(t, rotated[0]); got != "old" {
		t.Fatalf("rotated copytruncate log = %q, want old", got)
	}
}

func TestRotatingLogWriterCopyTruncateKeepsStdoutStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "size: 4", "copytruncate")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	w.nowFunc = fixedTime(time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC))

	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()

	if err := w.RedirectStdout(); err != nil {
		t.Fatalf("redirect stdout: %v", err)
	}
	if _, err := fmt.Fprint(os.Stdout, "old"); err != nil {
		t.Fatalf("direct stdout write: %v", err)
	}
	before := os.Stdout
	if _, err := w.Write([]byte("next")); err != nil {
		t.Fatalf("write through rotating writer: %v", err)
	}
	if os.Stdout != before {
		t.Fatal("copytruncate replaced os.Stdout and introduced a concurrent global pointer race")
	}
	if _, err := fmt.Fprint(os.Stdout, "!"); err != nil {
		t.Fatalf("direct stdout write after copytruncate: %v", err)
	}
	if got := mustReadFile(t, path); got != "next!" {
		t.Fatalf("active copytruncate stdout log = %q, want next!", got)
	}
}

func TestRotatingLogWriterRedirectStdoutUsesStableCopyTruncate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "size: 4")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	w.nowFunc = fixedTime(time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC))

	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()
	if err := w.RedirectStdout(); err != nil {
		t.Fatalf("redirect stdout: %v", err)
	}
	stdout := os.Stdout
	before := mustStat(t, path)
	if _, err := fmt.Fprint(stdout, "old"); err != nil {
		t.Fatalf("direct stdout write: %v", err)
	}
	if _, err := w.Write([]byte("next")); err != nil {
		t.Fatalf("write through rotating writer: %v", err)
	}
	after := mustStat(t, path)
	if os.Stdout != stdout {
		t.Fatal("stdout pointer changed during rotation")
	}
	if !os.SameFile(before, after) {
		t.Fatal("stdout rotation replaced the active file instead of preserving its descriptor")
	}
	if got := mustReadFile(t, path); got != "next" {
		t.Fatalf("active stdout log = %q, want next", got)
	}
	rotated := listRotatedLogs(t, path)
	if len(rotated) != 1 || mustReadFile(t, rotated[0]) != "old" {
		t.Fatalf("rotated stdout logs = %v", rotated)
	}
}

func TestRotatingLogWriterMissingOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "size: 1", "missingok")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	w.nowFunc = fixedTime(time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC))

	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("write before remove: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove active log: %v", err)
	}
	if _, err := w.Write([]byte("y")); err != nil {
		t.Fatalf("write after missing active log: %v", err)
	}
	if got := mustReadFile(t, path); got != "y" {
		t.Fatalf("active log after missingok = %q, want y", got)
	}
}

func TestRotatingLogWriterNotifEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "daily", "notifempty")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()

	t1 := time.Date(2026, 5, 18, 23, 59, 0, 0, time.UTC)
	w.nowFunc = fixedTime(t1)
	w.day = logDay(t1)
	w.nowFunc = fixedTime(t1.Add(2 * time.Minute))
	if _, err := w.Write([]byte("day2")); err != nil {
		t.Fatalf("write after empty daily boundary: %v", err)
	}

	if got := mustReadFile(t, path); got != "day2" {
		t.Fatalf("active log = %q, want day2", got)
	}
	if rotated := listRotatedLogs(t, path); len(rotated) != 0 {
		t.Fatalf("rotated empty logs = %v, want none", rotated)
	}
}

func TestRotatingLogWriterDelayCompress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	oldFile := filepath.Join(dir, "app.log.20260518-080000.000000000")
	recentFile := filepath.Join(dir, "app.log.20260518-113000.000000000")
	if err := os.WriteFile(oldFile, []byte("old"), 0640); err != nil {
		t.Fatalf("write old rotated file: %v", err)
	}
	if err := os.WriteFile(recentFile, []byte("recent"), 0640); err != nil {
		t.Fatalf("write recent rotated file: %v", err)
	}
	if err := os.Chtimes(oldFile, now.Add(-4*time.Hour), now.Add(-4*time.Hour)); err != nil {
		t.Fatalf("chtime old file: %v", err)
	}
	if err := os.Chtimes(recentFile, now.Add(-30*time.Minute), now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("chtime recent file: %v", err)
	}

	w, err := NewRotatingLogWriter(path, "compress", "delaycompress: 1h")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	w.nowFunc = fixedTime(now)
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("write with delayed compression: %v", err)
	}

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("old uncompressed file still exists, stat err=%v", err)
	}
	if got := mustReadGzip(t, oldFile+".gz"); got != "old" {
		t.Fatalf("old delayed compressed content = %q, want old", got)
	}
	if got := mustReadFile(t, recentFile); got != "recent" {
		t.Fatalf("recent file = %q, want recent", got)
	}
}

func TestRotatingLogWriterRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	oldFile := filepath.Join(dir, "app.log.20260501-000000.000000000")
	recentFile := filepath.Join(dir, "app.log.20260517-000000.000000000")
	if err := os.WriteFile(oldFile, []byte("old"), 0640); err != nil {
		t.Fatalf("write old rotated file: %v", err)
	}
	if err := os.WriteFile(recentFile, []byte("recent"), 0640); err != nil {
		t.Fatalf("write recent rotated file: %v", err)
	}
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(oldFile, now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour)); err != nil {
		t.Fatalf("chtime old file: %v", err)
	}
	if err := os.Chtimes(recentFile, now.Add(-24*time.Hour), now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("chtime recent file: %v", err)
	}

	w, err := NewRotatingLogWriter(path, "rotate: 7")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	if err := w.clean(now); err != nil {
		t.Fatalf("clean rotated logs: %v", err)
	}

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("old file still exists, stat err=%v", err)
	}
	if _, err := os.Stat(recentFile); err != nil {
		t.Fatalf("recent file removed: %v", err)
	}
}

func TestRotatingLogWriterRedirectStdout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "size: 4")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	w.nowFunc = fixedTime(time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC))

	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()

	if err := w.RedirectStdout(); err != nil {
		t.Fatalf("redirect stdout: %v", err)
	}
	if _, err := fmt.Fprint(os.Stdout, "old"); err != nil {
		t.Fatalf("direct stdout write: %v", err)
	}
	if got := mustReadFile(t, path); got != "old" {
		t.Fatalf("direct stdout target = %q, want old", got)
	}

	if _, err := w.Write([]byte("next")); err != nil {
		t.Fatalf("write through rotating writer: %v", err)
	}
	if _, err := fmt.Fprint(os.Stdout, "!"); err != nil {
		t.Fatalf("direct stdout write after rotate: %v", err)
	}

	if got := mustReadFile(t, path); got != "next!" {
		t.Fatalf("active log after rotate = %q, want next!", got)
	}
	rotated := listRotatedLogs(t, path)
	if len(rotated) != 1 {
		t.Fatalf("rotated logs = %v, want 1", rotated)
	}
	if got := mustReadFile(t, rotated[0]); got != "old" {
		t.Fatalf("rotated stdout log = %q, want old", got)
	}
	if os.Stdout == oldStdout {
		t.Fatal("os.Stdout still points to original stdout")
	}
}

func TestRotatingLogWriterStdoutSurvivesMultipleRotations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "size: 4")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	w.nowFunc = fixedTime(time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC))

	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()

	if err := w.RedirectStdout(); err != nil {
		t.Fatalf("redirect stdout: %v", err)
	}
	for i := range 5 {
		if _, err := fmt.Fprintf(os.Stdout, "s%d", i); err != nil {
			t.Fatalf("stdout write before rotation %d: %v", i, err)
		}
		if _, err := w.Write([]byte("roll")); err != nil {
			t.Fatalf("rotation write %d: %v", i, err)
		}
		if _, err := fmt.Fprintf(os.Stdout, "x%d", i); err != nil {
			t.Fatalf("stdout write after rotation %d: %v", i, err)
		}
	}
	if got := mustReadFile(t, path); !strings.Contains(got, "x4") {
		t.Fatalf("active log after repeated rotations = %q, want final stdout write", got)
	}
	if rotated := listRotatedLogs(t, path); len(rotated) < 5 {
		t.Fatalf("rotated logs = %v, want at least 5", rotated)
	}
}

func TestRotatingLogWriterConcurrentWritesDuringRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "size: 20")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	w.nowFunc = fixedTime(time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC))

	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()
	if err := w.RedirectStdout(); err != nil {
		t.Fatalf("redirect stdout: %v", err)
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 20 {
				if j%2 == 0 {
					_, _ = w.Write([]byte("writer-line\n"))
					continue
				}
				_, _ = fmt.Fprintf(os.Stdout, "stdout-%d-%d\n", i, j)
			}
		}(i)
	}
	wg.Wait()

	if _, err := fmt.Fprint(os.Stdout, "final\n"); err != nil {
		t.Fatalf("stdout write after concurrent rotations: %v", err)
	}
	if got := mustReadFile(t, path); !strings.Contains(got, "final\n") {
		t.Fatalf("active log missing final stdout write: %q", got)
	}
}

func TestRotatingLogWriterOpenFailureKeepsStdoutUsable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := NewRotatingLogWriter(path, "size: 4")
	if err != nil {
		t.Fatalf("new rotating writer: %v", err)
	}
	defer w.Close()
	w.nowFunc = fixedTime(time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC))

	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()
	if err := w.RedirectStdout(); err != nil {
		t.Fatalf("redirect stdout: %v", err)
	}
	if _, err := fmt.Fprint(os.Stdout, "old"); err != nil {
		t.Fatalf("stdout write before failure: %v", err)
	}

	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0640); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	w.path = filepath.Join(blocker, "app.log")
	if _, err := w.Write([]byte("roll")); err == nil {
		t.Fatal("rotation error = nil, want open failure")
	}
	if _, err := fmt.Fprint(os.Stdout, "!"); err != nil {
		t.Fatalf("stdout write after failed rotation: %v", err)
	}
	if got := mustReadFile(t, path); got != "old!" {
		t.Fatalf("original stdout log = %q, want old!", got)
	}
}

func fixedTime(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func mustReadGzip(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open gzip %s: %v", path, err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("new gzip reader: %v", err)
	}
	defer gz.Close()
	data, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read gzip %s: %v", path, err)
	}
	return string(data)
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st
}

func listRotatedLogs(t *testing.T, path string) []string {
	t.Helper()
	files, err := rotatedLogFiles(path)
	if err != nil {
		t.Fatalf("list rotated logs: %v", err)
	}
	return files
}
