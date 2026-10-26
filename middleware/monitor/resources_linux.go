//go:build linux

package monitor

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func collectResources(m *Collector, s *Stats, now time.Time) {
	collectProcMetrics(m, s, now, "/proc")
	sampleSlow(m, s, now, func(slow *Stats) { collectLinuxSlow(m, slow, now) })
}

func collectLinuxSlow(m *Collector, s *Stats, now time.Time) {
	if data, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil && strings.TrimSpace(string(data)) != "" {
		digest := sha256.Sum256([]byte(strings.TrimSpace(string(data))))
		s.HostID = hex.EncodeToString(digest[:])
	}
	// 目录枚举限制总量和频率，超预算时返回不可用，避免巨大 FD 表阻塞采集。
	if count, err := countFDs("/proc/self/fd", 131072); err == nil {
		if data, err := os.ReadFile("/proc/self/limits"); err == nil {
			if soft, hard, ok := parseFDLimits(string(data)); ok {
				s.FDAvailable = true
				s.OpenFDs = count
				s.MaxFDs = soft
				s.MaxFDsHard = hard
			}
		}
	}
	path := m.diskPath
	if path == "" {
		path = "."
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	s.DiskPath = path
	var file syscall.Stat_t
	if err := syscall.Stat(path, &file); err == nil {
		s.DiskID = diskIdentity(s.HostID, uint64(file.Dev))
	}
	var disk syscall.Statfs_t
	if err := syscall.Statfs(path, &disk); err == nil && disk.Bsize > 0 {
		total, available := disk.Blocks*uint64(disk.Bsize), disk.Bavail*uint64(disk.Bsize)
		s.DiskTotalBytes, s.DiskAvailableBytes = &total, &available
		// 某些文件系统不提供 inode 容量，零总量应视为不可用。
		if disk.Files > 0 {
			s.DiskInodesTotal, s.DiskInodesAvailable = &disk.Files, &disk.Ffree
		}
		s.DiskSampledAt = &now
	}
	group, groupErr := os.ReadFile("/proc/self/cgroup")
	mounts, mountErr := os.ReadFile("/proc/self/mountinfo")
	if groupErr == nil && mountErr == nil {
		if dir, mount, ok := cgroupV2Dir(string(group), string(mounts)); ok {
			readCgroupV2(s, dir, mount)
		}
	}
}
