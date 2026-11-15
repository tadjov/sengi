package storage

import "strconv"

// The load record keys under which every engine reports the limits of its own container.
const (
	ContainerCPUsSetting   = "container.cpus"
	ContainerMemorySetting = "container.memory_bytes"
)

// CPUQuota is a cgroup CPU limit: QuotaMicros of every PeriodMicros, none when QuotaMicros is negative.
type CPUQuota struct {
	QuotaMicros  int64
	PeriodMicros int64
}

// CPUs renders the quota as the CPUs a container may use, or "max" without a quota.
func (q CPUQuota) CPUs() string {
	if q.QuotaMicros < 0 || q.PeriodMicros <= 0 {
		return "max"
	}
	return strconv.FormatFloat(float64(q.QuotaMicros)/float64(q.PeriodMicros), 'f', -1, 64)
}
