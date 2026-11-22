package pool

import "github.com/tadjov/sengi/internal/bench/artifact"

func ReadPoolFile(path string) (*PoolFile, error) {
	return artifact.ReadYAML(path, "pool", func(pf PoolFile) int { return pf.SchemaVersion })
}
