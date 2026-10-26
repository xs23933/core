package monitor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type cpuSnapshot struct {
	total, idle, process    uint64
	cores                   int
	hostValid, processValid bool
}

func parseHostCPU(data string) cpuSnapshot {
	var s cpuSnapshot
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if f[0] == "cpu" {
			if len(f) < 5 {
				return cpuSnapshot{}
			}
			// guest 已包含在 user/nice 中，不得重复累计；旧内核缺少的尾字段按零处理。
			for i := 1; i < len(f) && i <= 8; i++ {
				n, err := strconv.ParseUint(f[i], 10, 64)
				if err != nil || math.MaxUint64-s.total < n {
					return cpuSnapshot{}
				}
				s.total += n
				if i == 4 || i == 5 {
					s.idle += n
				}
			}
		} else if strings.HasPrefix(f[0], "cpu") {
			if _, err := strconv.ParseUint(strings.TrimPrefix(f[0], "cpu"), 10, 32); err == nil {
				s.cores++
			}
		}
	}
	s.hostValid = s.total > 0 && s.cores > 0 && s.idle <= s.total
	return s
}

func parseProcessCPU(data string) (uint64, bool) {
	// comm 可以包含空格与右括号，以最后一个右括号定位后续固定字段。
	end := strings.LastIndexByte(data, ')')
	if end < 0 {
		return 0, false
	}
	f := strings.Fields(data[end+1:])
	if len(f) < 13 {
		return 0, false
	}
	user, err := strconv.ParseUint(f[11], 10, 64)
	if err != nil {
		return 0, false
	}
	system, err := strconv.ParseUint(f[12], 10, 64)
	return user + system, err == nil && math.MaxUint64-user >= system
}

func applyCPU(s *Stats, prev, next cpuSnapshot) {
	s.CPUAvailable = false
	s.CPUPercent = 0
	s.HostCPUPercent = nil
	s.HostCPUCount = nil
	if next.hostValid {
		s.HostCPUCount = &next.cores
	}
	if !prev.hostValid || !next.hostValid || prev.cores != next.cores || next.total <= prev.total || next.idle < prev.idle {
		return
	}
	total := next.total - prev.total
	idle := next.idle - prev.idle
	if idle > total {
		return
	}
	host := 100 * float64(total-idle) / float64(total)
	s.HostCPUPercent = &host
	if prev.processValid && next.processValid && next.process >= prev.process {
		s.CPUPercent = 100 * float64(next.process-prev.process) * float64(next.cores) / float64(total)
		s.CPUAvailable = true
	}
}

func parseMemory(s *Stats, data string) {
	values := parseKBValues(data)
	s.HostMemoryTotalBytes = values["MemTotal"]
	s.HostMemoryAvailableBytes = values["MemAvailable"]
	s.HostSwapTotalBytes = values["SwapTotal"]
	s.HostSwapFreeBytes = values["SwapFree"]
}

func parseKBValues(data string) map[string]*uint64 {
	values := make(map[string]*uint64)
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[2] != "kB" {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil || n > math.MaxUint64/1024 {
			continue
		}
		n *= 1024
		values[strings.TrimSuffix(f[0], ":")] = &n
	}
	return values
}

func pressureAvg10(data string) *float64 {
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "some" {
			continue
		}
		for _, field := range f[1:] {
			if strings.HasPrefix(field, "avg10=") {
				return parseFinite(strings.TrimPrefix(field, "avg10="))
			}
		}
	}
	return nil
}

func parseFinite(data string) *float64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(data), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return nil
	}
	return &n
}

func parseFDLimits(data string) (int64, int64, bool) {
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || strings.Join(f[:3], " ") != "Max open files" {
			continue
		}
		parse := func(value string) (int64, bool) {
			if value == "unlimited" {
				return -1, true
			}
			n, err := strconv.ParseInt(value, 10, 64)
			return n, err == nil && n >= 0
		}
		soft, a := parse(f[3])
		hard, b := parse(f[4])
		return soft, hard, a && b
	}
	return 0, 0, false
}

func countFDs(path string, limit int) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var count int
	for {
		names, err := f.Readdirnames(256)
		count += len(names)
		if count > limit {
			return 0, errors.New("文件描述符计数超过采集预算")
		}
		if err == io.EOF {
			return int64(count), nil
		}
		if err != nil {
			return 0, err
		}
	}
}

func sampleSlow(m *Collector, s *Stats, now time.Time, collect func(*Stats)) {
	interval := m.resourceInterval
	if interval < time.Second {
		interval = 30 * time.Second
	}
	if m.slowStats == nil || now.Sub(m.lastResourceSample) >= interval {
		// 每次重新建立快照，读取失败不能沿用上一轮的健康值。
		m.slowStats = &Stats{}
		collect(m.slowStats)
		m.lastResourceSample = now
	}
	slow := m.slowStats
	sampledAt := m.lastResourceSample
	s.ResourceSampledAt = &sampledAt
	s.FDAvailable, s.OpenFDs, s.MaxFDs, s.MaxFDsHard = slow.FDAvailable, slow.OpenFDs, slow.MaxFDs, slow.MaxFDsHard
	s.HostID = slow.HostID
	s.DiskID = slow.DiskID
	s.DiskPath, s.DiskTotalBytes, s.DiskAvailableBytes = slow.DiskPath, slow.DiskTotalBytes, slow.DiskAvailableBytes
	s.DiskInodesTotal, s.DiskInodesAvailable, s.DiskSampledAt = slow.DiskInodesTotal, slow.DiskInodesAvailable, slow.DiskSampledAt
	s.CgroupMemoryCurrentBytes, s.CgroupMemoryLimitBytes = slow.CgroupMemoryCurrentBytes, slow.CgroupMemoryLimitBytes
	s.CgroupPidsCurrent, s.CgroupPidsLimit = slow.CgroupPidsCurrent, slow.CgroupPidsLimit
	s.CgroupCPULimitCores = slow.CgroupCPULimitCores
	s.CgroupMemoryAvailableBytes, s.CgroupPidsAvailable = slow.CgroupMemoryAvailableBytes, slow.CgroupPidsAvailable
}

func cgroupV2Dir(cgroup, mountinfo string) (string, string, bool) {
	var group string
	for _, line := range strings.Split(cgroup, "\n") {
		if strings.HasPrefix(line, "0::") {
			group = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if group == "" || !filepath.IsAbs(group) {
		return "", "", false
	}
	unescape := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	for _, line := range strings.Split(mountinfo, "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok || !strings.HasPrefix(after, "cgroup2 ") {
			continue
		}
		f := strings.Fields(before)
		if len(f) < 5 {
			continue
		}
		root, mount := unescape.Replace(f[3]), unescape.Replace(f[4])
		if group != root && !strings.HasPrefix(group, strings.TrimSuffix(root, "/")+"/") {
			continue
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(group, root), "/")
		return filepath.Join(mount, relative), mount, true
	}
	return "", "", false
}

func readCgroupV2(s *Stats, dir, mount string) {
	s.CgroupMemoryCurrentBytes = readUintFile(filepath.Join(dir, "memory.current"))
	s.CgroupPidsCurrent = readUintFile(filepath.Join(dir, "pids.current"))
	memoryUnknown, pidsUnknown := false, false
	// 父 cgroup 也可能施加限制；只沿当前层级向上读取，深度有界。
	for depth := 0; depth < 64; depth++ {
		if !collectCgroupHeadroom(dir, mount, "memory", &s.CgroupMemoryLimitBytes, &s.CgroupMemoryAvailableBytes) {
			memoryUnknown = true
		}
		if !collectCgroupHeadroom(dir, mount, "pids", &s.CgroupPidsLimit, &s.CgroupPidsAvailable) {
			pidsUnknown = true
		}
		if data, err := os.ReadFile(filepath.Join(dir, "cpu.max")); err == nil {
			f := strings.Fields(string(data))
			if len(f) == 2 {
				quota, period := parseFinite(f[0]), parseFinite(f[1])
				if quota != nil && period != nil && *period > 0 {
					cores := *quota / *period
					if s.CgroupCPULimitCores == nil || cores < *s.CgroupCPULimitCores {
						s.CgroupCPULimitCores = &cores
					}
				}
			}
		}
		if dir == mount {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir || !strings.HasPrefix(parent+string(filepath.Separator), mount+string(filepath.Separator)) {
			break
		}
		dir = parent
		if depth == 63 {
			memoryUnknown = true
			pidsUnknown = true
		}
	}
	if memoryUnknown {
		s.CgroupMemoryAvailableBytes = nil
	}
	if pidsUnknown {
		s.CgroupPidsAvailable = nil
	}
}

// collectCgroupHeadroom 用各层自己的使用量计算余量，避免忽略同父组其它服务的占用。
func collectCgroupHeadroom(dir, mount, controller string, limit, available **uint64) bool {
	data, err := os.ReadFile(filepath.Join(dir, controller+".max"))
	if err != nil {
		return dir == mount && os.IsNotExist(err)
	}
	value := strings.TrimSpace(string(data))
	if value == "max" {
		return true
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return false
	}
	minimumUint(limit, &n)
	used := readUintFile(filepath.Join(dir, controller+".current"))
	if used == nil {
		return false
	}
	headroom := uint64(0)
	if *used < n {
		headroom = n - *used
	}
	minimumUint(available, &headroom)
	return true
}

func minimumUint(dst **uint64, value *uint64) {
	if value != nil && (*dst == nil || *value < **dst) {
		*dst = value
	}
}
func readUintFile(path string) *uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func collectProcMetrics(m *Collector, s *Stats, now time.Time, procRoot string) {
	s.SampledAt = now
	var next cpuSnapshot
	if data, err := os.ReadFile(filepath.Join(procRoot, "stat")); err == nil {
		next = parseHostCPU(string(data))
	}
	if data, err := os.ReadFile(filepath.Join(procRoot, "self/stat")); err == nil {
		next.process, next.processValid = parseProcessCPU(string(data))
	}
	applyCPU(s, m.previousCPU, next)
	m.previousCPU = next
	if data, err := os.ReadFile(filepath.Join(procRoot, "self/status")); err == nil {
		s.ProcessRSSBytes = parseKBValues(string(data))["VmRSS"]
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && f[0] == "Threads:" {
				if n, err := strconv.ParseUint(f[1], 10, 64); err == nil {
					s.ProcessThreads = &n
				}
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(procRoot, "meminfo")); err == nil {
		parseMemory(s, string(data))
	}
	if data, err := os.ReadFile(filepath.Join(procRoot, "loadavg")); err == nil {
		f := strings.Fields(string(data))
		if len(f) >= 3 {
			s.HostLoad1, s.HostLoad5, s.HostLoad15 = parseFinite(f[0]), parseFinite(f[1]), parseFinite(f[2])
		}
	}
	for name, dst := range map[string]**float64{"cpu": &s.PressureCPUSomeAvg10, "memory": &s.PressureMemorySomeAvg10, "io": &s.PressureIOSomeAvg10} {
		if data, err := os.ReadFile(filepath.Join(procRoot, "pressure", name)); err == nil {
			*dst = pressureAvg10(string(data))
		}
	}
}

// diskIdentity 以主机启动身份和文件系统设备号生成不包含路径的稳定标识。
func diskIdentity(host string, device uint64) string {
	if host == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(host + ":" + strconv.FormatUint(device, 10)))
	return hex.EncodeToString(digest[:])
}
