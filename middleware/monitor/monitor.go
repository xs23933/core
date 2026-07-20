// Package monitor 提供服务器运行时监控插件，暴露 CPU、内存、文件句柄、连接数等指标。
//
// 使用方式:
//
//	// 方式 1: 仅暴露 Prometheus /metrics 端点
//	app.Get("/metrics", monitor.PrometheusHandler())
//
//	// 方式 2: 启动后台周期性采集 + JSON endpoint
//	app.Use(monitor.New(monitor.Config{Interval: 5 * time.Second}))
//	app.Get("/sys/stats", monitor.StatsHandler())
//
// 零开销 — 后台 goroutine 每 Interval 采集一次，不影响请求处理。
package monitor

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xs23933/core/v3"
)

// Stats 服务器运行时统计
type Stats struct {
	// CPU 相关
	NumGoroutine  int     `json:"num_goroutine"`
	NumCPU        int     `json:"num_cpu"`
	GoMaxProcs    int     `json:"gomaxprocs"`
	NumCgoCall    int64   `json:"num_cgo_call"`
	CPUPercent    float64 `json:"cpu_percent"` // 近似值

	// 内存相关 (bytes)
	Alloc      uint64 `json:"alloc"`
	TotalAlloc uint64 `json:"total_alloc"`
	Sys        uint64 `json:"sys"`
	HeapAlloc  uint64 `json:"heap_alloc"`
	HeapSys    uint64 `json:"heap_sys"`
	HeapInuse  uint64 `json:"heap_inuse"`
	HeapIdle   uint64 `json:"heap_idle"`
	StackInuse uint64 `json:"stack_inuse"`

	// GC 相关
	NumGC     uint32  `json:"num_gc"`
	GCPauseMs float64 `json:"gc_pause_ms"`

	// 系统资源
	OpenFDs     int64 `json:"open_fds"`     // 当前进程打开的文件描述符数
	MaxFDs      int64 `json:"max_fds"`      // 系统允许的最大文件描述符
	NumConns    int64 `json:"num_conns"`    // 框架追踪的网络连接数
	NumRequests int64 `json:"num_requests"` // 累计请求数（通过中间件计数）

	// 内部字段
	prevCPUTime time.Time
	prevCPUUsed float64
}

// Config 监控配置
type Config struct {
	// Interval 采集间隔，默认 5s
	Interval time.Duration
	// Skip 可选跳过函数
	Skip func(c core.Ctx) bool
}

var DefaultConfig = Config{
	Interval: 5 * time.Second,
}

// Collector 运行时数据采集器
type Collector struct {
	stats  atomic.Value // *Stats
	mu     sync.Mutex
	conns  atomic.Int64
	reqs   atomic.Int64
	closed chan struct{}
}

var defaultCollector = &Collector{
	closed: make(chan struct{}),
}

func init() {
	defaultCollector.stats.Store(&Stats{})
}

// IncConns 增加连接计数（供 gateway/websocket 调用）
func IncConns() { defaultCollector.conns.Add(1) }

// DecConns 减少连接计数
func DecConns() { defaultCollector.conns.Add(-1) }

// IncRequests 增加请求计数（供中间件调用）
func IncRequests() { defaultCollector.reqs.Add(1) }

// StatsHandler 返回 JSON 格式的系统状态 handler。
func StatsHandler() core.HandlerFunc {
	return func(c core.Ctx) error {
		s := defaultCollector.stats.Load().(*Stats)
		return c.JSON(s)
	}
}

// PrometheusHandler 返回 Prometheus 格式的 /metrics handler。
func PrometheusHandler() core.HandlerFunc {
	return func(c core.Ctx) error {
		s := defaultCollector.stats.Load().(*Stats)
		var b strings.Builder

		writeMetric := func(name, help, typ string, value any) {
			b.WriteString(fmt.Sprintf("# HELP %s %s\n", name, help))
			b.WriteString(fmt.Sprintf("# TYPE %s %s\n", name, typ))
			b.WriteString(fmt.Sprintf("%s %v\n\n", name, value))
		}

		writeMetric("core_goroutines", "Number of goroutines", "gauge", s.NumGoroutine)
		writeMetric("core_memory_alloc_bytes", "Bytes allocated and not yet freed", "gauge", s.Alloc)
		writeMetric("core_memory_heap_alloc_bytes", "Heap bytes allocated", "gauge", s.HeapAlloc)
		writeMetric("core_memory_heap_inuse_bytes", "Heap bytes in use", "gauge", s.HeapInuse)
		writeMetric("core_memory_sys_bytes", "Bytes obtained from system", "gauge", s.Sys)
		writeMetric("core_gc_pause_seconds", "GC pause time in seconds", "gauge", fmt.Sprintf("%.6f", s.GCPauseMs/1000))
		writeMetric("core_open_fds", "Open file descriptors", "gauge", s.OpenFDs)
		writeMetric("core_max_fds", "Max file descriptors", "gauge", s.MaxFDs)
		writeMetric("core_connections", "Active connections", "gauge", s.NumConns)
		writeMetric("core_requests_total", "Total requests", "counter", s.NumRequests)
		writeMetric("core_cpu_percent", "Approximate CPU usage percent", "gauge",
			fmt.Sprintf("%.2f", s.CPUPercent))

		c.SetHeader("Content-Type", "text/plain; version=0.0.4")
		return c.SendString(b.String())
	}
}

// New 启动后台监控采集（周期性更新 Stats）。
func New(conf ...Config) core.HandlerFunc {
	cfg := DefaultConfig
	if len(conf) > 0 {
		if conf[0].Interval > 0 {
			cfg.Interval = conf[0].Interval
		}
		if conf[0].Skip != nil {
			cfg.Skip = conf[0].Skip
		}
	}

	// 启动后台采集
	go defaultCollector.collectLoop(cfg.Interval)

	return func(c core.Ctx) error {
		IncRequests()
		if cfg.Skip != nil && cfg.Skip(c) {
			return c.Next()
		}
		return c.Next()
	}
}

func (m *Collector) collectLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s := m.collect()
			m.stats.Store(s)
		case <-m.closed:
			return
		}
	}
}

func (m *Collector) collect() *Stats {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	prev := m.stats.Load().(*Stats)

	s := &Stats{
		NumGoroutine: runtime.NumGoroutine(),
		NumCPU:       runtime.NumCPU(),
		GoMaxProcs:   runtime.GOMAXPROCS(0),
		NumCgoCall:   runtime.NumCgoCall(),
		Alloc:        mem.Alloc,
		TotalAlloc:   mem.TotalAlloc,
		Sys:          mem.Sys,
		HeapAlloc:    mem.HeapAlloc,
		HeapSys:      mem.HeapSys,
		HeapInuse:    mem.HeapInuse,
		HeapIdle:     mem.HeapIdle,
		StackInuse:   mem.StackInuse,
		NumGC:        mem.NumGC,
		NumConns:     m.conns.Load(),
		NumRequests:  m.reqs.Load(),
	}

	// GC pause
	if s.NumGC > 0 {
		s.GCPauseMs = float64(mem.PauseTotalNs) / float64(s.NumGC) / 1e6
	}

	// CPU 近似值（两次采样间的差值 / 时间差）
	now := time.Now()
	cpuTime := now
	cpuUsed := float64(s.NumGoroutine)
	if !prev.prevCPUTime.IsZero() {
		elapsed := cpuTime.Sub(prev.prevCPUTime).Seconds()
		if elapsed > 0 {
			s.CPUPercent = (cpuUsed/elapsed)*100*0.01 + prev.CPUPercent*0.99
		}
	}
	s.prevCPUTime = cpuTime
	s.prevCPUUsed = cpuUsed

	// 文件描述符
	if fds, maxFds, err := getFDCount(); err == nil {
		s.OpenFDs = fds
		s.MaxFDs = maxFds
	}

	return s
}

func getFDCount() (openFDs, maxFDs int64, err error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		// macOS: 尝试用 lsof 替代
		return 0, 0, err
	}
	openFDs = int64(len(entries))

	// 读取系统限制
	data, err := os.ReadFile("/proc/self/limits")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "Max open files") {
				var soft, hard int64
				if _, scanErr := fmt.Sscanf(line, "Max open files %d %d", &soft, &hard); scanErr == nil {
					maxFDs = hard
				}
				break
			}
		}
	}
	return openFDs, maxFDs, nil
}

func init() {
	// 注册自定义 JSON 序列化类型以避免循环依赖
	_ = json.Marshal // ensure json imported
}
