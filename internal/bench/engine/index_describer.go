package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tadjov/tusker/internal/storage/es"
	"github.com/tadjov/tusker/internal/storage/pg"
)

// IndexDescriber is an optional capability for executors that can read, from
// the live engine, how the corpus they search was indexed and what the engine
// ran with.
type IndexDescriber interface {
	DescribeIndex(ctx context.Context) (*IndexDescription, error)
}

// IndexDescription holds exactly one engine family's description, beside the
// server that family ran on.
type IndexDescription struct {
	Postgres      *pg.IndexDescription `json:"postgres,omitempty"`
	Elasticsearch *es.IndexDescription `json:"elasticsearch,omitempty"`

	PostgresServer       *PostgresServer       `json:"postgres_server,omitempty"`
	ElasticsearchCluster *ElasticsearchCluster `json:"elasticsearch_cluster,omitempty"`
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

// PostgresServer is what a Postgres engine ran with: its build, every
// installed extension, every setting not at its default, and its container's
// limits. The spec's connection settings, read back from the session, are in
// pg.IndexDescription.Settings.
type PostgresServer struct {
	// Version is version(), which names the build and platform as well.
	Version    string            `json:"version"`
	Extensions []pg.Extension    `json:"extensions"`
	Settings   []PostgresSetting `json:"settings"`
	Limits     CgroupLimits      `json:"cgroup_limits"`
}

// PostgresSetting is one pg_settings row; Source is where the value came from,
// such as "command line" or "session".
type PostgresSetting struct {
	Name    string `json:"name"`
	Setting string `json:"setting"`
	Unit    string `json:"unit,omitempty"`
	Source  string `json:"source"`
}

// CgroupLimits are the cgroup v2 cpu.max and memory.max the server reads from
// inside its container. Unreadable says why it could not read them, as when
// the role may not read server files.
type CgroupLimits struct {
	CPUMax     string `json:"cpu_max,omitempty"`
	MemoryMax  string `json:"memory_max,omitempty"`
	Unreadable string `json:"unreadable,omitempty"`
}

// ElasticsearchCluster is what an Elasticsearch engine ran with: its build,
// the index's and the cluster's settings that are not defaults, flat, and each
// node's heap and cgroup limits.
type ElasticsearchCluster struct {
	BuildHash       string              `json:"build_hash"`
	IndexSettings   map[string]any      `json:"index_settings"`
	ClusterSettings ClusterSettings     `json:"cluster_settings"`
	Nodes           []ElasticsearchNode `json:"nodes"`
}

type ClusterSettings struct {
	Persistent map[string]any `json:"persistent,omitempty"`
	Transient  map[string]any `json:"transient,omitempty"`
}

// ElasticsearchNode is a node's heap ceiling and the cgroup limits it reports;
// the cgroup fields are empty when the node runs outside one.
type ElasticsearchNode struct {
	Name         string `json:"name"`
	HeapMaxBytes int64  `json:"heap_max_bytes"`
	// CPUQuotaMicros is -1 when the cgroup sets no quota.
	CPUQuotaMicros  *int64 `json:"cpu_quota_micros,omitempty"`
	CPUPeriodMicros *int64 `json:"cpu_period_micros,omitempty"`
	// MemoryLimit is a byte count, or "max" when the cgroup sets none.
	MemoryLimit string `json:"memory_limit,omitempty"`
}

func (e *PgExecutor) DescribeIndex(ctx context.Context) (*IndexDescription, error) {
	description, err := pg.DescribeIndexes(ctx, e.pool)
	if err != nil {
		return nil, fmt.Errorf("pg describe index: %w", err)
	}
	server, err := describePostgresServer(ctx, e.pool.GetConn())
	if err != nil {
		return nil, fmt.Errorf("pg describe server: %w", err)
	}
	return &IndexDescription{Postgres: description, PostgresServer: server}, nil
}

func describePostgresServer(ctx context.Context, db *pgxpool.Pool) (*PostgresServer, error) {
	var server PostgresServer
	if err := db.QueryRow(ctx, "SELECT version()").Scan(&server.Version); err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}
	rows, err := db.Query(ctx, "SELECT extname, extversion FROM pg_extension ORDER BY extname")
	if err != nil {
		return nil, fmt.Errorf("read extensions: %w", err)
	}
	if server.Extensions, err = pgx.CollectRows(rows, pgx.RowToStructByPos[pg.Extension]); err != nil {
		return nil, fmt.Errorf("read extensions: %w", err)
	}
	rows, err = db.Query(ctx,
		"SELECT name, setting, coalesce(unit, ''), source FROM pg_settings WHERE source <> 'default' ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	if server.Settings, err = pgx.CollectRows(rows, pgx.RowToStructByPos[PostgresSetting]); err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	server.Limits = readCgroupLimits(ctx, db)
	return &server, nil
}

func readCgroupLimits(ctx context.Context, db *pgxpool.Pool) CgroupLimits {
	var limits CgroupLimits
	err := db.QueryRow(ctx,
		"SELECT pg_read_file('/sys/fs/cgroup/cpu.max'), pg_read_file('/sys/fs/cgroup/memory.max')").
		Scan(&limits.CPUMax, &limits.MemoryMax)
	if err != nil {
		return CgroupLimits{Unreadable: err.Error()}
	}
	limits.CPUMax = strings.TrimSpace(limits.CPUMax)
	limits.MemoryMax = strings.TrimSpace(limits.MemoryMax)
	return limits
}

func (e *EsExecutor) DescribeIndex(ctx context.Context) (*IndexDescription, error) {
	description, err := es.DescribeIndex(ctx, es.ClientConfig{Addresses: []string{e.baseURL}, IndexName: e.index})
	if err != nil {
		return nil, fmt.Errorf("es describe index: %w", err)
	}
	cluster, err := e.describeCluster(ctx)
	if err != nil {
		return nil, fmt.Errorf("es describe cluster: %w", err)
	}
	return &IndexDescription{Elasticsearch: description, ElasticsearchCluster: cluster}, nil
}

func (e *EsExecutor) describeCluster(ctx context.Context) (*ElasticsearchCluster, error) {
	var cluster ElasticsearchCluster
	var info struct {
		Version struct {
			BuildHash string `json:"build_hash"`
		} `json:"version"`
	}
	if err := e.getJSON(ctx, "/", &info); err != nil {
		return nil, fmt.Errorf("read build: %w", err)
	}
	cluster.BuildHash = info.Version.BuildHash

	var byIndex map[string]struct {
		Settings map[string]any `json:"settings"`
	}
	if err := e.getJSON(ctx, "/"+neturl.PathEscape(e.index)+"/_settings?flat_settings=true", &byIndex); err != nil {
		return nil, fmt.Errorf("read settings of %s: %w", e.index, err)
	}
	if len(byIndex) != 1 {
		return nil, fmt.Errorf("read settings of %s: resolves to %d indices, want 1", e.index, len(byIndex))
	}
	for _, index := range byIndex {
		cluster.IndexSettings = index.Settings
	}

	if err := e.getJSON(ctx, "/_cluster/settings?flat_settings=true", &cluster.ClusterSettings); err != nil {
		return nil, fmt.Errorf("read cluster settings: %w", err)
	}

	nodes, err := e.describeNodes(ctx)
	if err != nil {
		return nil, err
	}
	cluster.Nodes = nodes
	return &cluster, nil
}

func (e *EsExecutor) describeNodes(ctx context.Context) ([]ElasticsearchNode, error) {
	var stats struct {
		Nodes map[string]struct {
			Name string `json:"name"`
			JVM  struct {
				Mem struct {
					HeapMaxInBytes int64 `json:"heap_max_in_bytes"`
				} `json:"mem"`
			} `json:"jvm"`
			OS struct {
				Cgroup *struct {
					CPU struct {
						CFSQuotaMicros  *int64 `json:"cfs_quota_micros"`
						CFSPeriodMicros *int64 `json:"cfs_period_micros"`
					} `json:"cpu"`
					Memory struct {
						LimitInBytes string `json:"limit_in_bytes"`
					} `json:"memory"`
				} `json:"cgroup"`
			} `json:"os"`
		} `json:"nodes"`
	}
	if err := e.getJSON(ctx, "/_nodes/stats/os,jvm", &stats); err != nil {
		return nil, fmt.Errorf("read node stats: %w", err)
	}
	nodes := make([]ElasticsearchNode, 0, len(stats.Nodes))
	for _, n := range stats.Nodes {
		node := ElasticsearchNode{Name: n.Name, HeapMaxBytes: n.JVM.Mem.HeapMaxInBytes}
		if cgroup := n.OS.Cgroup; cgroup != nil {
			node.CPUQuotaMicros = cgroup.CPU.CFSQuotaMicros
			node.CPUPeriodMicros = cgroup.CPU.CFSPeriodMicros
			node.MemoryLimit = cgroup.Memory.LimitInBytes
		}
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, nil
}

func (e *EsExecutor) getJSON(ctx context.Context, path string, into any) error {
	body, err := e.send(ctx, http.MethodGet, e.baseURL+path, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
