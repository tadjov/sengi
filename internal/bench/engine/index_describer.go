package engine

import (
	"context"
	"fmt"

	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/es"
	"github.com/tadjov/sengi/internal/storage/pg"
)

// IndexDescriber is an optional capability for executors that can read, from
// the live engine, how the corpus they search was indexed.
type IndexDescriber interface {
	DescribeIndex(ctx context.Context) (*IndexDescription, error)
}

// IndexDescription holds exactly one engine family's description.
type IndexDescription struct {
	Postgres      *pg.IndexDescription `json:"postgres,omitempty"`
	Elasticsearch *es.IndexDescription `json:"elasticsearch,omitempty"`
	Fusion        *FusionDescription   `json:"fusion,omitempty"`
}

// FusionDescription lists a fusion engine's legs, since it has no index of its own.
type FusionDescription struct {
	Legs []string `json:"legs"`
}

func (d IndexDescription) Version() string {
	switch {
	case d.Postgres != nil:
		return d.Postgres.ServerVersion
	case d.Elasticsearch != nil:
		return d.Elasticsearch.Version
	default:
		return ""
	}
}

// Load is the engine's latest corpus load record, nil when it has none.
func (d IndexDescription) Load() *storage.LoadRecord {
	switch {
	case d.Postgres != nil:
		return d.Postgres.Load
	case d.Elasticsearch != nil:
		return d.Elasticsearch.Load
	default:
		return nil
	}
}

func (e *PgExecutor) DescribeIndex(ctx context.Context) (*IndexDescription, error) {
	description, err := pg.DescribeIndexes(ctx, e.pool)
	if err != nil {
		return nil, fmt.Errorf("pg describe index: %w", err)
	}
	return &IndexDescription{Postgres: description}, nil
}

func (e *EsExecutor) DescribeIndex(ctx context.Context) (*IndexDescription, error) {
	description, err := es.DescribeIndex(ctx, es.ClientConfig{Addresses: []string{e.baseURL}, IndexName: e.index})
	if err != nil {
		return nil, fmt.Errorf("es describe index: %w", err)
	}
	return &IndexDescription{Elasticsearch: description}, nil
}

func (f *FusionExecutor) DescribeIndex(context.Context) (*IndexDescription, error) {
	return &IndexDescription{Fusion: &FusionDescription{Legs: f.legs}}, nil
}
