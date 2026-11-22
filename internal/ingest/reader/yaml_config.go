package reader

import (
	"fmt"
	"io"

	"github.com/tadjov/sengi/internal/ingest/datamapping"
	"gopkg.in/yaml.v3"
)

// LoadMapping fails on any key the schema does not define, and on a mapping that is not valid.
func LoadMapping(r io.Reader) (*datamapping.DataMapper, error) {
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	var mapping datamapping.DataMapper
	if err := decoder.Decode(&mapping); err != nil {
		return nil, fmt.Errorf("decode mapping: %w", err)
	}
	if err := mapping.Validate(); err != nil {
		return nil, err
	}
	return &mapping, nil
}
