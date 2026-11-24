package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tadjov/sengi/internal/bench/engine"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/es"
	"github.com/tadjov/sengi/internal/storage/pg"
)

func TestLoadRecords_MapsEachEngineHoldingAnIndex(t *testing.T) {
	record := &storage.LoadRecord{ModelTag: "fixture-model@4"}

	loads := loadRecords(map[string]indexReading{
		"pg":       {description: &engine.IndexDescription{Postgres: &pg.IndexDescription{Load: record}}},
		"es":       {description: &engine.IndexDescription{Elasticsearch: &es.IndexDescription{}}},
		"unread":   {err: errors.New("connection refused")},
		"pg-fused": {description: &engine.IndexDescription{Fusion: &engine.FusionDescription{Legs: []string{"pg", "es"}}}},
	})

	assert.Equal(t, map[string]*storage.LoadRecord{"pg": record, "es": nil, "unread": nil}, loads)
}
