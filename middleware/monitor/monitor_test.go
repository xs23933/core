package monitor

import (
	"encoding/json"
	"github.com/xs23933/core/v3"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestColdSampleDoesNotInventCPUUsage(t *testing.T) {
	m := &Collector{}
	m.stats.Store(&Stats{})
	s := m.collect()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if available, exists := got["cpu_available"]; !exists || available != false {
		t.Fatalf("首次采样必须显式标记 CPU 未就绪，得到 %s", data)
	}
	if _, exists := got["process_rss_bytes"]; !exists {
		t.Fatal("必须提供 RSS 指标或显式 null")
	}
	if _, exists := got["max_fds_hard"]; !exists {
		t.Fatal("必须区分文件描述符软限制与硬限制")
	}
}

func TestCollectorStartsOnlyOnceAndStopsLoop(t *testing.T) {
	m := &Collector{closed: make(chan struct{})}
	m.start(Config{Interval: time.Hour, ResourceInterval: time.Hour, DiskPath: "first"})
	first := m.stats.Load().(*Stats)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.start(Config{Interval: time.Second, DiskPath: "second"}) }()
	}
	wg.Wait()
	if got := m.stats.Load().(*Stats); got != first || m.diskPath != "first" {
		t.Fatal("重复安装不得重复采集或覆盖首次配置")
	}
	close(m.closed)
	done := make(chan struct{})
	go func() { m.collectLoop(time.Hour); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("关闭后采集循环必须退出")
	}
}

func TestSnapshotReadsCachedCounters(t *testing.T) {
	first := Snapshot()
	IncRequests()
	second := Snapshot()
	if first.NumRequests != second.NumRequests {
		t.Fatal("读取 Snapshot 不应主动触发采样")
	}
}

var snapshotBenchmarkSink Stats

func BenchmarkSnapshot(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		snapshotBenchmarkSink = Snapshot()
	}
}

func TestPrometheusOmitsUnavailableOSValues(t *testing.T) {
	app := core.New()
	app.GET("/metrics", PrometheusHandler())
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if strings.Contains(body, "core_cpu_percent ") || strings.Contains(body, "core_open_fds ") || strings.Contains(body, "core_max_fds ") || strings.Contains(body, "core_process_rss_bytes ") {
		t.Fatalf("未采集系统值不得作为健康零值输出：%s", body)
	}
	if !strings.Contains(body, "core_cpu_available 0\n") || !strings.Contains(body, "core_fd_available 0\n") {
		t.Fatal("必须暴露显式不可用标记")
	}
}
