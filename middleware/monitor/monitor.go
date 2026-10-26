// Package monitor 提供服务器运行时监控插件，暴露 CPU、内存、文件句柄、连接数等指标。
//
// 使用方式:
//
//	// 启动后台周期性采集，再暴露 JSON 或 Prometheus 端点
//	app.Use(monitor.New(monitor.Config{Interval: 5 * time.Second}))
//	app.Get("/sys/stats", monitor.StatsHandler())
//	app.Get("/metrics", monitor.PrometheusHandler())
//
// 后台固定间隔采集并缓存；请求只读取快照，不执行系统探测。
package monitor

import (
	"fmt"
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
	NumGoroutine int     `json:"num_goroutine"`
	NumCPU       int     `json:"num_cpu"`
	GoMaxProcs   int     `json:"gomaxprocs"`
	NumCgoCall   int64   `json:"num_cgo_call"`
	CPUPercent   float64 `json:"cpu_percent"` // 进程 CPU，单核满载为 100；以 CPUAvailable 判断有效性

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
	MaxFDs      int64 `json:"max_fds"`      // 当前进程文件描述符软限制；-1 表示无限制
	NumConns    int64 `json:"num_conns"`    // 框架追踪的网络连接数
	NumRequests int64 `json:"num_requests"` // 累计请求数（通过中间件计数）

	// 不支持、读取失败及首次 CPU 差分采样均显式标记为不可用。
	SampledAt                  time.Time  `json:"sampled_at"`
	HostID                     string     `json:"host_id"`
	ResourceSampledAt          *time.Time `json:"resource_sampled_at"`
	CPUAvailable               bool       `json:"cpu_available"`
	FDAvailable                bool       `json:"fd_available"`
	MaxFDsHard                 int64      `json:"max_fds_hard"`
	ProcessRSSBytes            *uint64    `json:"process_rss_bytes"`
	ProcessThreads             *uint64    `json:"process_threads"`
	HostCPUPercent             *float64   `json:"host_cpu_percent"`
	HostCPUCount               *int       `json:"host_cpu_count"`
	HostMemoryTotalBytes       *uint64    `json:"host_memory_total_bytes"`
	HostMemoryAvailableBytes   *uint64    `json:"host_memory_available_bytes"`
	HostSwapTotalBytes         *uint64    `json:"host_swap_total_bytes"`
	HostSwapFreeBytes          *uint64    `json:"host_swap_free_bytes"`
	HostLoad1                  *float64   `json:"host_load1"`
	HostLoad5                  *float64   `json:"host_load5"`
	HostLoad15                 *float64   `json:"host_load15"`
	DiskID                     string     `json:"disk_id"`
	DiskPath                   string     `json:"disk_path"`
	DiskTotalBytes             *uint64    `json:"disk_total_bytes"`
	DiskAvailableBytes         *uint64    `json:"disk_available_bytes"`
	DiskInodesTotal            *uint64    `json:"disk_inodes_total"`
	DiskInodesAvailable        *uint64    `json:"disk_inodes_available"`
	DiskSampledAt              *time.Time `json:"disk_sampled_at"`
	PressureCPUSomeAvg10       *float64   `json:"pressure_cpu_some_avg10"`
	PressureMemorySomeAvg10    *float64   `json:"pressure_memory_some_avg10"`
	PressureIOSomeAvg10        *float64   `json:"pressure_io_some_avg10"`
	CgroupMemoryAvailableBytes *uint64    `json:"cgroup_memory_available_bytes"`
	CgroupPidsAvailable        *uint64    `json:"cgroup_pids_available"`
	CgroupMemoryCurrentBytes   *uint64    `json:"cgroup_memory_current_bytes"`
	CgroupMemoryLimitBytes     *uint64    `json:"cgroup_memory_limit_bytes"`
	CgroupPidsCurrent          *uint64    `json:"cgroup_pids_current"`
	CgroupPidsLimit            *uint64    `json:"cgroup_pids_limit"`
	CgroupCPULimitCores        *float64   `json:"cgroup_cpu_limit_cores"`
}

// Config 监控配置
type Config struct {
	// Interval 采集间隔，默认 5s
	Interval time.Duration
	// ResourceInterval 为文件描述符、磁盘与 cgroup 的采集间隔，默认 30s。
	ResourceInterval time.Duration
	// DiskPath 指定本机数据目录，默认当前目录；只执行该目录所在文件系统的 statfs。
	DiskPath string
	// Skip 可选跳过函数
	Skip func(c core.Ctx) bool
}

var DefaultConfig = Config{
	Interval:         5 * time.Second,
	ResourceInterval: 30 * time.Second,
	DiskPath:         ".",
}

// Collector 运行时数据采集器
type Collector struct {
	stats              atomic.Value // *Stats
	mu                 sync.Mutex
	conns              atomic.Int64
	reqs               atomic.Int64
	closed             chan struct{}
	startOnce          sync.Once
	resourceInterval   time.Duration
	diskPath           string
	previousCPU        cpuSnapshot
	slowStats          *Stats
	lastResourceSample time.Time
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

// Snapshot 返回进程级采集器的缓存快照；调用者应只读使用其中的可空指标。
func Snapshot() Stats {
	return *defaultCollector.stats.Load().(*Stats)
}

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
		if s.FDAvailable {
			writeMetric("core_open_fds", "Open file descriptors", "gauge", s.OpenFDs)
			writeMetric("core_max_fds", "Process soft file descriptor limit (-1 means unlimited)", "gauge", s.MaxFDs)
		}
		writeMetric("core_connections", "Active connections", "gauge", s.NumConns)
		writeMetric("core_requests_total", "Total requests", "counter", s.NumRequests)
		if s.FDAvailable {
			writeMetric("core_max_fds_hard", "Process hard file descriptor limit (-1 means unlimited)", "gauge", s.MaxFDsHard)
		}
		if s.CPUAvailable {
			writeMetric("core_cpu_percent", "Process CPU usage percent (one core = 100)", "gauge", fmt.Sprintf("%.2f", s.CPUPercent))
		}
		writeMetric("core_cpu_available", "Process CPU sample availability", "gauge", boolMetric(s.CPUAvailable))
		writeMetric("core_fd_available", "Process file descriptor sample availability", "gauge", boolMetric(s.FDAvailable))
		writeResourceMetrics(writeMetric, s)

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
		if conf[0].ResourceInterval > 0 {
			cfg.ResourceInterval = conf[0].ResourceInterval
		}
		if conf[0].DiskPath != "" {
			cfg.DiskPath = conf[0].DiskPath
		}
		if conf[0].Skip != nil {
			cfg.Skip = conf[0].Skip
		}
	}

	// 进程级采集器只启动一次，避免重复安装中间件产生多个采集循环。
	defaultCollector.start(cfg)

	return func(c core.Ctx) error {
		IncRequests()
		if cfg.Skip != nil && cfg.Skip(c) {
			return c.Next()
		}
		return c.Next()
	}
}

func (m *Collector) start(cfg Config) {
	m.startOnce.Do(func() {
		if cfg.Interval < time.Second {
			cfg.Interval = time.Second
		}
		if cfg.ResourceInterval < cfg.Interval {
			cfg.ResourceInterval = cfg.Interval
		}
		m.resourceInterval = cfg.ResourceInterval
		m.diskPath = cfg.DiskPath
		m.stats.Store(m.collect())
		go m.collectLoop(cfg.Interval)
	})

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
	m.mu.Lock()
	defer m.mu.Unlock()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

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

	collectResources(m, s, time.Now())
	return s
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}

func writeResourceMetrics(write func(string, string, string, any), s *Stats) {
	metrics := []struct {
		name  string
		value *uint64
	}{
		{"core_process_rss_bytes", s.ProcessRSSBytes},
		{"core_process_threads", s.ProcessThreads},
		{"core_host_memory_total_bytes", s.HostMemoryTotalBytes},
		{"core_host_memory_available_bytes", s.HostMemoryAvailableBytes},
		{"core_host_swap_total_bytes", s.HostSwapTotalBytes},
		{"core_host_swap_free_bytes", s.HostSwapFreeBytes},
		{"core_disk_total_bytes", s.DiskTotalBytes},
		{"core_disk_available_bytes", s.DiskAvailableBytes},
		{"core_disk_inodes_total", s.DiskInodesTotal},
		{"core_disk_inodes_available", s.DiskInodesAvailable},
		{"core_cgroup_memory_current_bytes", s.CgroupMemoryCurrentBytes},
		{"core_cgroup_memory_limit_bytes", s.CgroupMemoryLimitBytes},
		{"core_cgroup_memory_available_bytes", s.CgroupMemoryAvailableBytes},
		{"core_cgroup_pids_available", s.CgroupPidsAvailable},
		{"core_cgroup_pids_current", s.CgroupPidsCurrent},
		{"core_cgroup_pids_limit", s.CgroupPidsLimit},
	}
	for _, metric := range metrics {
		if metric.value != nil {
			write(metric.name, metric.name, "gauge", *metric.value)
		}
	}
	floatMetrics := []struct {
		name  string
		value *float64
	}{
		{"core_host_cpu_percent", s.HostCPUPercent},
		{"core_host_load1", s.HostLoad1},
		{"core_host_load5", s.HostLoad5},
		{"core_host_load15", s.HostLoad15},
		{"core_pressure_cpu_some_avg10", s.PressureCPUSomeAvg10},
		{"core_pressure_memory_some_avg10", s.PressureMemorySomeAvg10},
		{"core_pressure_io_some_avg10", s.PressureIOSomeAvg10},
		{"core_cgroup_cpu_limit_cores", s.CgroupCPULimitCores},
	}
	for _, metric := range floatMetrics {
		if metric.value != nil {
			write(metric.name, metric.name, "gauge", *metric.value)
		}
	}
	if s.HostCPUCount != nil {
		write("core_host_cpu_count", "Visible host CPU count", "gauge", *s.HostCPUCount)
	}
	if !s.SampledAt.IsZero() {
		write("core_stats_sample_timestamp_seconds", "Last regular sample timestamp", "gauge", s.SampledAt.Unix())
	}
	if s.ResourceSampledAt != nil {
		write("core_resource_sample_timestamp_seconds", "Last resource sample timestamp", "gauge", s.ResourceSampledAt.Unix())
	}
}
