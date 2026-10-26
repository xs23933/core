//go:build !linux

package monitor

import "time"

func collectResources(m *Collector, s *Stats, now time.Time) {
	// 非 Linux 平台保持 Go 运行时指标；操作系统指标明确为不可用。
	s.SampledAt = now
}
