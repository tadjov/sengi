package pg

import (
	"strings"
	"testing"

	"github.com/tadjov/sengi/internal/storage"
)

func TestDescribeIndexes_ReadsTheMigratedServer(t *testing.T) {
	specSettings := map[string]string{
		"hnsw.ef_search":  "123",
		"plan_cache_mode": "force_custom_plan",
		"work_mem":        "8192kB",
	}
	// SHOW reads a size in the largest unit it divides into.
	readBack := map[string]string{
		"hnsw.ef_search":  "123",
		"plan_cache_mode": "force_custom_plan",
		"work_mem":        "8MB",
	}
	pool, err := NewConnectionPool(testCtx, PoolConfig{
		ConnStr:            testPool.Pool().Config().ConnString(),
		ConnectionSettings: specSettings,
	})
	if err != nil {
		t.Fatalf("open pool with connection settings: %v", err)
	}
	t.Cleanup(pool.Close)

	description, err := DescribeIndexes(testCtx, pool)
	if err != nil {
		t.Fatalf("DescribeIndexes: %v", err)
	}

	t.Run("server version with its build", func(t *testing.T) {
		if !strings.HasPrefix(description.ServerVersion, "PostgreSQL 18") || !strings.Contains(description.ServerVersion, " on ") {
			t.Errorf("server_version = %q, want version() of the pg18 image", description.ServerVersion)
		}
	})

	t.Run("every extension the migrations create", func(t *testing.T) {
		versions := make(map[string]string)
		for _, ext := range description.Extensions {
			versions[ext.Name] = ext.Version
		}
		for _, name := range []string{"vector", "pg_trgm", "fuzzystrmatch", "uuid-ossp"} {
			if versions[name] == "" {
				t.Errorf("extension %s missing or without a version; got %v", name, description.Extensions)
			}
		}
	})

	t.Run("server settings off their default, connection settings once", func(t *testing.T) {
		bySource := make(map[string]ServerSetting)
		for _, setting := range description.ServerSettings {
			bySource[setting.Name] = setting
			if setting.Source == "default" {
				t.Errorf("%s recorded at its default", setting.Name)
			}
		}
		want := ServerSetting{Name: "maintenance_work_mem", Setting: "4194304", Unit: "kB", Source: "command line"}
		if got := bySource["maintenance_work_mem"]; got != want {
			t.Errorf("maintenance_work_mem = %+v, want the container's -c flag %+v", got, want)
		}
		for name := range specSettings {
			if setting, ok := bySource[name]; ok {
				t.Errorf("connection setting %s recorded again as a server setting: %+v", name, setting)
			}
		}
	})

	t.Run("cgroup limits a superuser reads", func(t *testing.T) {
		if description.Limits == nil || description.Limits.CPUMax == "" || description.Limits.MemoryMax == "" {
			t.Errorf("limits = %+v, unreadable %v, want cpu.max and memory.max", description.Limits, description.Unreadable)
		}
		if len(description.Unreadable) != 0 {
			t.Errorf("unreadable = %v, want every read to succeed", description.Unreadable)
		}
	})

	t.Run("hnsw index with its build parameters", func(t *testing.T) {
		index, ok := findIndex(description.Indexes, "idx_article_embedding")
		if !ok {
			t.Fatalf("idx_article_embedding not described; got %v", description.Indexes)
		}
		if index.Table != "article_embeddings" {
			t.Errorf("table = %q, want article_embeddings", index.Table)
		}
		if !strings.Contains(index.Definition, "USING hnsw") {
			t.Errorf("definition = %q, want an hnsw index", index.Definition)
		}
		declared := map[string]int{"m": storage.HNSWM, "ef_construction": storage.HNSWEfConstruction}
		for name, want := range declared {
			if got, ok := reloption(index.Options, name); !ok || got != want {
				t.Errorf("reloption %s = %d (present %v), want %d; reloptions = %v", name, got, ok, want, index.Options)
			}
		}
	})

	t.Run("articles indexes", func(t *testing.T) {
		if _, ok := findIndex(description.Indexes, "articles_pkey"); !ok {
			t.Errorf("articles primary key not described; got %v", description.Indexes)
		}
	})

	t.Run("connection settings the pool applied, as the session reads them", func(t *testing.T) {
		for name, want := range readBack {
			if got := description.Settings[name]; got != want {
				t.Errorf("setting %s = %q, want %q", name, got, want)
			}
		}
	})
}

func findIndex(indexes []IndexDefinition, name string) (IndexDefinition, bool) {
	for _, index := range indexes {
		if index.Name == name {
			return index, true
		}
	}
	return IndexDefinition{}, false
}
