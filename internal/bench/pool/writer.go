package pool

import (
	"fmt"

	"github.com/tadjov/sengi/internal/bench/artifact"
	"github.com/tadjov/sengi/internal/bench/version"
	"gopkg.in/yaml.v3"
)

// WritePoolFile stamps schema_version and replaces path atomically; the caller sets pf.Meta.
func WritePoolFile(pf *PoolFile, path string) error {
	pf.SchemaVersion = version.SchemaVersion
	data, err := yaml.Marshal(pf)
	if err != nil {
		return fmt.Errorf("marshal pool file: %w", err)
	}
	return artifact.WriteAtomic(path, data)
}
