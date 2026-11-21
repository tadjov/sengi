package report

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tadjov/sengi/internal/bench/engine"
	"github.com/tadjov/sengi/internal/bench/metrics"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/es"
	"github.com/tadjov/sengi/internal/storage/pg"
)

func TestGenerate_RecordsIndexProvenanceThroughTheWrittenReport(t *testing.T) {
	br := makeBenchmarkResult([]string{"pg", "es"}, []string{"q1"}, map[string]map[string]metrics.ScoreSet{
		"q1": {"pg": makeJudgedScores(0.7), "es": makeJudgedScores(0.5)},
	})
	rpt := Generate(br, &GenerateOptions{
		Spec: &spec.BenchSpec{Engines: map[string]spec.Engine{
			"pg":  {Type: "postgres"},
			"es":  {Type: "elasticsearch"},
			"api": {Type: "api"},
		}},
		Indexes: map[string]IndexProvenance{
			"pg": {IndexDescription: engine.IndexDescription{Postgres: &pg.IndexDescription{
				ServerVersion: "18.0",
				Settings:      map[string]string{"hnsw.ef_search": "200"},
			}}},
			"es": {IndexDescription: engine.IndexDescription{Elasticsearch: &es.IndexDescription{
				Version: "8.15.0",
				Index:   "news-cc-000001",
			}}},
			"api": {Error: "engine cannot describe its index"},
		},
	})

	path := filepath.Join(t.TempDir(), "report.json")
	if err := WriteJSON(rpt, path); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	got, err := ReadJSON(path)
	if err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}

	engines := got.Environment.Engines
	if v := engines["pg"].Version; v != "18.0" {
		t.Errorf("pg version = %q, want 18.0", v)
	}
	if v := engines["es"].Version; v != "8.15.0" {
		t.Errorf("es version = %q, want 8.15.0", v)
	}
	if idx := engines["pg"].Index; idx == nil || idx.Postgres == nil || idx.Postgres.Settings["hnsw.ef_search"] != "200" {
		t.Errorf("pg index provenance = %+v, want the ef_search it was measured at", idx)
	}
	if idx := engines["es"].Index; idx == nil || idx.Elasticsearch == nil || idx.Elasticsearch.Index != "news-cc-000001" {
		t.Errorf("es index provenance = %+v, want the concrete index", idx)
	}
	if idx := engines["api"].Index; idx == nil || idx.Error == "" {
		t.Errorf("api index provenance = %+v, want a block carrying the error", idx)
	}
}

func reportWithLoads(pgEmbeddings, esEmbeddings string) *Report {
	load := func(engineName, embeddingsVersion string) *storage.LoadRecord {
		return &storage.LoadRecord{
			Engine:     engineName,
			Canonical:  storage.PackageRef{Name: "cc-news", Version: "article-v2-601d5476"},
			Embeddings: &storage.PackageRef{Name: "cc-news-embeddings", Version: embeddingsVersion},
			ModelTag:   "qwen3-embedding:0.6b@1024",
		}
	}
	return &Report{Environment: RunEnvironment{Engines: map[string]EngineInfo{
		"pg": {Index: &IndexProvenance{IndexDescription: engine.IndexDescription{Postgres: &pg.IndexDescription{
			ServerVersion: "18.0", Load: load("pg-native", pgEmbeddings),
		}}}},
		"es": {Index: &IndexProvenance{IndexDescription: engine.IndexDescription{Elasticsearch: &es.IndexDescription{
			Version: "8.12.0", Index: "articles", Load: load("elasticsearch", esEmbeddings),
		}}}},
	}}}
}

func TestDiffProvenance_NamesALoadOfOtherEmbeddings(t *testing.T) {
	before := reportWithLoads("lead-v1-1024-938b2254", "lead-v1-1024-938b2254")
	after := reportWithLoads("lead-v1-1024-0badc0de", "lead-v1-1024-0badc0de")

	changes := DiffProvenance(before, after)

	for _, engineName := range []string{"es", "pg"} {
		found := false
		for _, c := range changes {
			if c.Engine == engineName && c.Before == "lead-v1-1024-938b2254" && c.After == "lead-v1-1024-0badc0de" {
				found = true
			}
		}
		if !found {
			t.Errorf("changes = %+v, want %s's embeddings version to differ", changes, engineName)
		}
	}
	if same := DiffProvenance(before, before); len(same) != 0 {
		t.Errorf("identical loads differ: %+v", same)
	}
}

func TestSummary_NamesTheLoadedPackagesAndTag(t *testing.T) {
	rpt := reportWithLoads("lead-v1-1024-938b2254", "lead-v1-1024-938b2254")

	for name, info := range rpt.Environment.Engines {
		summary := info.Index.Summary()
		for _, want := range []string{"article-v2-601d5476", "lead-v1-1024-938b2254", "qwen3-embedding:0.6b@1024"} {
			if !strings.Contains(summary, want) {
				t.Errorf("%s summary = %q, want it to name %s", name, summary, want)
			}
		}
	}
}

func int64Pointer(v int64) *int64 { return &v }

// recordedEnvironment is an engine provenance carrying one of each fact the
// run records about what a Postgres or Elasticsearch engine ran with.
func recordedEnvironment() (postgres, elasticsearch IndexProvenance) {
	postgres = IndexProvenance{
		IndexDescription: engine.IndexDescription{Postgres: &pg.IndexDescription{
			ServerVersion:  "PostgreSQL 18.1 (Debian 18.1-1.pgdg12+2) on aarch64-unknown-linux-gnu",
			Extensions:     []pg.Extension{{Name: "vector", Version: "0.8.1"}},
			Settings:       map[string]string{"hnsw.ef_search": "200"},
			ServerSettings: []pg.ServerSetting{{Name: "work_mem", Setting: "4096", Unit: "kB", Source: "command line"}},
			Limits:         &pg.CgroupLimits{CPUMax: "max 100000", MemoryMax: "max"},
		}},
		Container: &engine.Container{Name: "pg-native-news", Image: "pgvector/pgvector:pg18", ImageID: "sha256:aaa", RepoDigests: []string{"pgvector/pgvector@sha256:bbb"}},
	}
	elasticsearch = IndexProvenance{IndexDescription: engine.IndexDescription{Elasticsearch: &es.IndexDescription{
		Version:         "8.12.0",
		BuildHash:       "1665f706",
		Index:           "articles",
		Settings:        map[string]any{"index.number_of_replicas": "0"},
		ClusterSettings: &es.ClusterSettings{Persistent: map[string]any{"cluster.routing.allocation.enable": "all"}},
		Nodes:           []es.NodeStats{{Name: "es-1", HeapMaxBytes: 512 << 20, CPUQuotaMicros: int64Pointer(-1), CPUPeriodMicros: int64Pointer(100000), MemoryLimit: "max"}},
	}}}
	return postgres, elasticsearch
}

func TestDiffProvenance_NamesEveryRecordedFactThatChanged(t *testing.T) {
	tests := []struct {
		name   string
		change func(postgres, elasticsearch *IndexProvenance)
		engine string
		field  string
	}{
		{name: "an extension version", engine: "pg", field: "extension.vector",
			change: func(p, _ *IndexProvenance) { p.Postgres.Extensions[0].Version = "0.8.2" }},
		{name: "a server setting", engine: "pg", field: "server_setting.work_mem",
			change: func(p, _ *IndexProvenance) { p.Postgres.ServerSettings[0].Setting = "8192" }},
		{name: "a cgroup limit", engine: "pg", field: "cgroup.memory_max",
			change: func(p, _ *IndexProvenance) { p.Postgres.Limits.MemoryMax = "8589934592" }},
		{name: "a server read that failed", engine: "pg", field: "unreadable",
			change: func(p, _ *IndexProvenance) { p.Postgres.Unreadable = []string{"read cgroup limits: permission denied"} }},
		{name: "the image", engine: "pg", field: "container.image",
			change: func(p, _ *IndexProvenance) { p.Container.Image = "pgvector/pgvector:pg17" }},
		{name: "the image's registry digest", engine: "pg", field: "container.repo_digests",
			change: func(p, _ *IndexProvenance) { p.Container.RepoDigests = []string{"pgvector/pgvector@sha256:ccc"} }},
		{name: "the build", engine: "es", field: "build_hash",
			change: func(_, e *IndexProvenance) { e.Elasticsearch.BuildHash = "0badc0de" }},
		{name: "an index setting", engine: "es", field: "setting.index.number_of_replicas",
			change: func(_, e *IndexProvenance) { e.Elasticsearch.Settings["index.number_of_replicas"] = "1" }},
		{name: "a cluster setting", engine: "es", field: "cluster.persistent.cluster.routing.allocation.enable",
			change: func(_, e *IndexProvenance) {
				e.Elasticsearch.ClusterSettings.Persistent["cluster.routing.allocation.enable"] = "primaries"
			}},
		{name: "a node's heap", engine: "es", field: "node.es-1.heap_max_bytes",
			change: func(_, e *IndexProvenance) { e.Elasticsearch.Nodes[0].HeapMaxBytes = 1 << 30 }},
		{name: "a node's cpu quota", engine: "es", field: "node.es-1.cpu_quota_micros",
			change: func(_, e *IndexProvenance) { e.Elasticsearch.Nodes[0].CPUQuotaMicros = int64Pointer(200000) }},
		{name: "a node's memory limit", engine: "es", field: "node.es-1.memory_limit",
			change: func(_, e *IndexProvenance) { e.Elasticsearch.Nodes[0].MemoryLimit = "2147483648" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pgBefore, esBefore := recordedEnvironment()
			pgAfter, esAfter := recordedEnvironment()
			tt.change(&pgAfter, &esAfter)
			report := func(postgres, elasticsearch IndexProvenance) *Report {
				return &Report{Environment: RunEnvironment{Engines: map[string]EngineInfo{
					"pg": {Index: &postgres}, "es": {Index: &elasticsearch},
				}}}
			}

			changes := DiffProvenance(report(pgBefore, esBefore), report(pgAfter, esAfter))

			if len(changes) != 1 || changes[0].Engine != tt.engine || changes[0].Field != tt.field {
				t.Errorf("changes = %+v, want only %s's %s", changes, tt.engine, tt.field)
			}
		})
	}
}

func TestSummary_NamesWhatTheEngineRanWith(t *testing.T) {
	postgres, elasticsearch := recordedEnvironment()

	tests := []struct {
		name       string
		provenance IndexProvenance
		want       []string
	}{
		{name: "postgres", provenance: postgres, want: []string{"postgres 18.1 (Debian 18.1-1.pgdg12+2)", "server settings: 1", "cpu.max max 100000", "image: pgvector/pgvector:pg18"}},
		{name: "elasticsearch", provenance: elasticsearch, want: []string{"elasticsearch 8.12.0", "node es-1: heap 512 MiB"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := tt.provenance.Summary()
			for _, want := range tt.want {
				if !strings.Contains(summary, want) {
					t.Errorf("summary = %q, want it to name %q", summary, want)
				}
			}
		})
	}
}

func TestSummary_NamesNoServerSettingsAReportNeverRecorded(t *testing.T) {
	older := IndexProvenance{IndexDescription: engine.IndexDescription{Postgres: &pg.IndexDescription{ServerVersion: "18.0"}}}

	if summary := older.Summary(); strings.Contains(summary, "server settings") {
		t.Errorf("summary = %q, want no server settings count for a report that recorded none", summary)
	}
}
