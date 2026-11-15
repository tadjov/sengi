package testing

import "github.com/moby/moby/api/types/container"

// engineBudget mirrors the cpus and mem_limit every engine gets in docker-compose.yml.
func engineBudget(hostConfig *container.HostConfig) {
	hostConfig.NanoCPUs = 8_000_000_000
	hostConfig.Memory = 7 << 30
}
