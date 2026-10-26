package monitor

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCPUCounterDeltasUseActualProcessTime(t *testing.T) {
	before := cpuSnapshot{total: 1000, idle: 700, process: 10, cores: 4, hostValid: true, processValid: true}
	after := cpuSnapshot{total: 1400, idle: 900, process: 60, cores: 4, hostValid: true, processValid: true}
	s := &Stats{}
	applyCPU(s, before, after)
	if !s.CPUAvailable || s.CPUPercent != 50 {
		t.Fatalf("进程 CPU = %#v，期望 50%%", s)
	}
	if s.HostCPUPercent == nil || *s.HostCPUPercent != 50 {
		t.Fatalf("主机 CPU = %v", s.HostCPUPercent)
	}
	applyCPU(s, after, cpuSnapshot{total: 1, process: 1, cores: 4, hostValid: true, processValid: true})
	if s.CPUAvailable || s.HostCPUPercent != nil {
		t.Fatal("计数器回退必须标记不可用")
	}
}

func TestProcStatHandlesParenthesesAndGuestTime(t *testing.T) {
	fields := make([]string, 22)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[11], fields[12] = "17", "23"
	ticks, ok := parseProcessCPU("123 (worker ) with spaces) " + strings.Join(fields, " "))
	if !ok || ticks != 40 {
		t.Fatalf("进程 CPU 解析错误：%d %v", ticks, ok)
	}
	sample := parseHostCPU("cpu 100 0 50 800 20 10 10 10 100 0\ncpu0 0\ncpu1 0\n")
	if !sample.hostValid || sample.total != 1000 || sample.idle != 820 || sample.cores != 2 {
		t.Fatalf("不得重复累计 guest：%+v", sample)
	}
}

func TestLinuxMemoryAndUnavailableSemantics(t *testing.T) {
	s := &Stats{}
	parseMemory(s, "MemTotal: 2000 kB\nMemAvailable: 500 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	if s.HostMemoryTotalBytes == nil || *s.HostMemoryTotalBytes != 2048000 || *s.HostMemoryAvailableBytes != 512000 {
		t.Fatalf("内存单位错误：%+v", s)
	}
	if s.HostSwapTotalBytes == nil || *s.HostSwapTotalBytes != 0 {
		t.Fatal("确知无 swap 必须为 0")
	}
	s = &Stats{}
	parseMemory(s, "MemTotal: invalid kB\nMemAvailable: 1 frogs\n")
	if s.HostMemoryTotalBytes != nil || s.HostMemoryAvailableBytes != nil {
		t.Fatal("未知内存不得变成健康零值")
	}
	if pressureAvg10("some avg10=2.50 avg60=0 avg300=0 total=42") == nil || pressureAvg10("some avg10=NaN") != nil {
		t.Fatal("PSI 必须拒绝非有限值")
	}
}

func TestFDSoftAndHardLimitsAndBoundedCount(t *testing.T) {
	soft, hard, ok := parseFDLimits("Max open files            1024                 1048576              files\n")
	if !ok || soft != 1024 || hard != 1048576 {
		t.Fatalf("软硬限制颠倒：%d %d %v", soft, hard, ok)
	}
	soft, hard, ok = parseFDLimits("Max open files unlimited unlimited files")
	if !ok || soft != -1 || hard != -1 {
		t.Fatal("无限制必须显式表示为 -1")
	}
	if _, _, ok = parseFDLimits("Max open files 1024 corrupt files"); ok {
		t.Fatal("损坏限制必须无效")
	}
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := countFDs(dir, 2); err == nil || n != 0 {
		t.Fatal("超过上限不得报告部分计数")
	}
	if n, err := countFDs(dir, 4); err != nil || n != 3 {
		t.Fatalf("计数错误：%d %v", n, err)
	}
}

func TestCgroupV2MountAndAncestorLimits(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "service")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"memory.max": "1024", "memory.current": "800", "cpu.max": "50000 100000", "pids.max": "10", "pids.current": "9", "service/memory.max": "max", "service/memory.current": "512", "service/cpu.max": "max 100000", "service/pids.max": "max", "service/pids.current": "3"}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mountinfo := "1 0 0:1 /tenant " + root + " rw - cgroup2 cgroup rw\n"
	dir, mount, ok := cgroupV2Dir("0::/tenant/service\n", mountinfo)
	if !ok || dir != child || mount != root {
		t.Fatalf("cgroup 挂载映射错误：%s %s %v", dir, mount, ok)
	}
	s := &Stats{}
	readCgroupV2(s, dir, mount)
	if s.CgroupMemoryLimitBytes == nil || *s.CgroupMemoryLimitBytes != 1024 || s.CgroupCPULimitCores == nil || math.Abs(*s.CgroupCPULimitCores-.5) > 1e-9 || *s.CgroupPidsLimit != 10 || *s.CgroupMemoryCurrentBytes != 512 {
		t.Fatalf("必须考虑父级限制：%+v", s)
	}
	if s.CgroupMemoryAvailableBytes == nil || *s.CgroupMemoryAvailableBytes != 224 || s.CgroupPidsAvailable == nil || *s.CgroupPidsAvailable != 1 {
		t.Fatal("父组其它进程占用必须减少实际可用额度")
	}
	if err := os.Remove(filepath.Join(root, "memory.current")); err != nil {
		t.Fatal(err)
	}
	failed := &Stats{}
	readCgroupV2(failed, dir, mount)
	if failed.CgroupMemoryAvailableBytes != nil {
		t.Fatal("受限祖先读取失败不能伪造可用内存")
	}
	if _, _, ok := cgroupV2Dir("0::/other\n", mountinfo); ok {
		t.Fatal("不得匹配不同 cgroup 根")
	}
}

func TestSlowResourcesAreCachedUntilDeadline(t *testing.T) {
	m := &Collector{resourceInterval: 30 * time.Second}
	calls := 0
	var first *Stats
	now := time.Unix(100, 0)
	for _, delta := range []time.Duration{0, time.Second, 29 * time.Second, 30 * time.Second} {
		s := &Stats{}
		sampleSlow(m, s, now.Add(delta), func(s *Stats) { calls++; s.OpenFDs = int64(calls); s.FDAvailable = true })
		if first == nil {
			first = s
		}
		expected := int64(1)
		if delta == 30*time.Second {
			expected = 2
		}
		if s.OpenFDs != expected {
			t.Fatalf("缓存未命中：%d", s.OpenFDs)
		}
	}
	if !first.ResourceSampledAt.Equal(now) {
		t.Fatal("已发布快照的时间戳被后续采样篡改")
	}
	if calls != 2 {
		t.Fatalf("慢探测次数=%d", calls)
	}
}

func BenchmarkCachedSlowResources(b *testing.B) {
	m := &Collector{resourceInterval: 30 * time.Second}
	now := time.Now()
	sampleSlow(m, &Stats{}, now, func(s *Stats) { s.FDAvailable = true })
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sampleSlow(m, &Stats{}, now, func(s *Stats) { b.Fatal("不得重新探测") })
	}
}

func procFixture(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"self", "pressure"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	fields := make([]string, 22)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0], fields[11] = "S", "10"
	files := map[string]string{
		"stat":         "cpu 300 0 0 700 0 0 0 0\ncpu0 0\ncpu1 0\ncpu2 0\ncpu3 0\n",
		"self/stat":    "1 (worker) " + strings.Join(fields, " "),
		"self/status":  "VmRSS: 512 kB\nThreads: 4\n",
		"meminfo":      "MemTotal: 2000 kB\nMemAvailable: 500 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n",
		"loadavg":      "1.2 2.3 3.4 1/99 100\n",
		"pressure/cpu": "some avg10=0.25 avg60=0.00 avg300=0.00 total=0\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestProcFixtureSamplesAndResetsFailedResources(t *testing.T) {
	root := procFixture(t)
	m := &Collector{}
	first := &Stats{}
	collectProcMetrics(m, first, time.Unix(100, 0), root)
	if first.CPUAvailable || first.HostCPUPercent != nil || first.ProcessRSSBytes == nil || *first.ProcessRSSBytes != 512*1024 || *first.ProcessThreads != 4 || *first.HostLoad5 != 2.3 || *first.PressureCPUSomeAvg10 != .25 || first.PressureIOSomeAvg10 != nil {
		t.Fatalf("首次快照错误：%+v", first)
	}
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte("cpu 500 0 0 900 0 0 0 0\ncpu0 0\ncpu1 0\ncpu2 0\ncpu3 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "self/stat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "self/stat"), []byte(strings.Replace(string(data), "S 0 0 0 0 0 0 0 0 0 0 10", "S 0 0 0 0 0 0 0 0 0 0 60", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "meminfo")); err != nil {
		t.Fatal(err)
	}
	second := &Stats{}
	collectProcMetrics(m, second, time.Unix(105, 0), root)
	if !second.CPUAvailable || second.CPUPercent != 50 || second.HostCPUPercent == nil || *second.HostCPUPercent != 50 {
		t.Fatalf("差分 CPU 错误：%+v", second)
	}
	if second.HostMemoryTotalBytes != nil {
		t.Fatal("读取失败不得沿用旧内存健康状态")
	}
}

func BenchmarkProcSampling(b *testing.B) {
	root := procFixture(b)
	m := &Collector{}
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		collectProcMetrics(m, &Stats{}, now, root)
	}
}

func TestDiskIdentitySeparatesFilesystemsWithoutExposingPaths(t *testing.T) {
	a := diskIdentity("boot-a", 12)
	if len(a) != 64 || a == diskIdentity("boot-a", 13) || a == diskIdentity("boot-b", 12) || a != diskIdentity("boot-a", 12) {
		t.Fatal("文件系统身份不能稳定隔离主机与磁盘")
	}
	if diskIdentity("", 12) != "" {
		t.Fatal("未知主机不能制造可去重身份")
	}
}
