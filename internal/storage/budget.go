package storage

import "strconv"

// The load record keys under which every engine reports the limits of its own container.
const (
	ContainerCPUsSetting   = "container.cpus"
	ContainerMemorySetting = "container.memory_bytes"
)

// ContainerCPUs renders a cgroup CPU quota as the CPUs a container may use, or "max" without a quota.
func ContainerCPUs(quotaMicros, periodMicros int64) string {
	if quotaMicros < 0 || periodMicros <= 0 {
		return "max"
	}
	return strconv.FormatFloat(float64(quotaMicros)/float64(periodMicros), 'f', -1, 64)
}
