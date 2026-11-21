package es

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
	"github.com/tadjov/sengi/internal/storage"
)

// IndexDescription is how an ES engine was indexed and set up, read from the live cluster.
type IndexDescription struct {
	Version   string `json:"version"`
	BuildHash string `json:"build_hash,omitempty"`
	// Index is the concrete index the configured name resolved to, which
	// differs from it when the configured name is an alias.
	Index string `json:"index"`
	// Analysis is settings.index.analysis with its keys sorted, so two
	// descriptions of the same analysis compare equal byte for byte.
	Analysis json.RawMessage `json:"analysis,omitempty"`
	// Settings are the index's non-default settings, flattened.
	Settings map[string]any `json:"settings,omitempty"`
	// Fields holds the text and dense_vector fields, multi-fields included,
	// keyed by dotted path.
	Fields map[string]FieldMapping `json:"fields,omitempty"`
	// Load is nil when the corpus load left no record.
	Load *storage.LoadRecord `json:"load,omitempty"`
	// ClusterSettings is nil when it could not be read; Unreadable says why.
	ClusterSettings *ClusterSettings `json:"cluster_settings,omitempty"`
	Nodes           []NodeStats      `json:"nodes,omitempty"`
	// Unreadable lists the cluster reads that failed; the rest still stands.
	Unreadable []string `json:"unreadable,omitempty"`
}

// ClusterSettings are the non-default cluster settings, flattened.
type ClusterSettings struct {
	Persistent map[string]any `json:"persistent,omitempty"`
	Transient  map[string]any `json:"transient,omitempty"`
}

// NodeStats is a node's heap ceiling and cgroup limits; the limits are empty outside a cgroup.
type NodeStats struct {
	Name         string `json:"name"`
	HeapMaxBytes int64  `json:"heap_max_bytes"`
	// CPUQuotaMicros is -1 when the cgroup sets no quota.
	CPUQuotaMicros  *int64 `json:"cpu_quota_micros,omitempty"`
	CPUPeriodMicros *int64 `json:"cpu_period_micros,omitempty"`
	// MemoryLimit is a byte count, or "max" when the cgroup sets none.
	MemoryLimit string `json:"memory_limit,omitempty"`
}

type FieldMapping struct {
	Type           string `json:"type"`
	Analyzer       string `json:"analyzer,omitempty"`
	SearchAnalyzer string `json:"search_analyzer,omitempty"`
	// IndexOptions is a text field's postings detail: docs, freqs, positions
	// or offsets.
	IndexOptions string              `json:"index_options,omitempty"`
	Dims         int                 `json:"dims,omitempty"`
	Similarity   string              `json:"similarity,omitempty"`
	VectorIndex  *VectorIndexOptions `json:"vector_index_options,omitempty"`
}

type VectorIndexOptions struct {
	Type           string `json:"type"`
	M              int    `json:"m,omitempty"`
	EfConstruction int    `json:"ef_construction,omitempty"`
}

// DescribeIndex records how the index was set up and loaded, and what the cluster ran with.
// A failed cluster read goes to Unreadable instead of the error.
func DescribeIndex(ctx context.Context, cfg ClientConfig) (*IndexDescription, error) {
	client, err := newTypedClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	info, err := client.Info().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("read cluster version: %w", err)
	}

	index, err := readIndexSettings(ctx, client, cfg.IndexName)
	if err != nil {
		return nil, err
	}

	mappings, err := client.Indices.GetMapping().Index(cfg.IndexName).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("read mapping of %s: %w", cfg.IndexName, err)
	}
	record, ok := mappings[index.name]
	if !ok {
		return nil, fmt.Errorf("read mapping of %s: no mapping for index %s", cfg.IndexName, index.name)
	}

	load, err := decodeLoadRecord(record.Mappings.Meta_)
	if err != nil {
		return nil, fmt.Errorf("read load record of %s: %w", index.name, err)
	}

	description := &IndexDescription{
		Version:   info.Version.Int,
		BuildHash: info.Version.BuildHash,
		Index:     index.name,
		Analysis:  index.analysis,
		Settings:  index.settings,
		Fields:    searchFieldMappings(record.Mappings.Properties),
		Load:      load,
	}
	if description.ClusterSettings, err = readClusterSettings(ctx, client); err != nil {
		description.Unreadable = append(description.Unreadable, err.Error())
	}
	if description.Nodes, err = readNodeStats(ctx, client); err != nil {
		description.Unreadable = append(description.Unreadable, err.Error())
	}
	return description, nil
}

func readClusterSettings(ctx context.Context, client *elasticsearch.TypedClient) (*ClusterSettings, error) {
	res, err := client.Cluster.GetSettings().FlatSettings(true).Perform(ctx)
	if err != nil {
		return nil, fmt.Errorf("read cluster settings: %w", err)
	}
	var settings ClusterSettings
	if err := decodeBody(res, &settings); err != nil {
		return nil, fmt.Errorf("read cluster settings: %w", err)
	}
	return &settings, nil
}

func readNodeStats(ctx context.Context, client *elasticsearch.TypedClient) ([]NodeStats, error) {
	res, err := client.Nodes.Stats().Metric("os,jvm").Perform(ctx)
	if err != nil {
		return nil, fmt.Errorf("read node stats: %w", err)
	}
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
	if err := decodeBody(res, &stats); err != nil {
		return nil, fmt.Errorf("read node stats: %w", err)
	}
	nodes := make([]NodeStats, 0, len(stats.Nodes))
	for _, n := range stats.Nodes {
		node := NodeStats{Name: n.Name, HeapMaxBytes: n.JVM.Mem.HeapMaxInBytes}
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

func decodeBody(res *http.Response, into any) error {
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", res.StatusCode, body)
	}
	return json.Unmarshal(body, into)
}

func decodeLoadRecord(meta types.Metadata) (*storage.LoadRecord, error) {
	raw, ok := meta["load"]
	if !ok {
		return nil, nil
	}
	var record storage.LoadRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// indexSettings is an index's analysis block and its other settings, flattened.
type indexSettings struct {
	name     string
	analysis json.RawMessage
	settings map[string]any
}

// readIndexSettings reads raw settings, so analysis parts the typed client doesn't model are kept.
func readIndexSettings(ctx context.Context, client *elasticsearch.TypedClient, name string) (indexSettings, error) {
	res, err := client.Indices.GetSettings().Index(name).Perform(ctx)
	if err != nil {
		return indexSettings{}, fmt.Errorf("read settings of %s: %w", name, err)
	}
	var byIndex map[string]struct {
		Settings map[string]any `json:"settings"`
	}
	if err := decodeBody(res, &byIndex); err != nil {
		return indexSettings{}, fmt.Errorf("read settings of %s: %w", name, err)
	}
	if len(byIndex) != 1 {
		return indexSettings{}, fmt.Errorf("read settings of %s: resolves to %d indices, want 1", name, len(byIndex))
	}

	for index, body := range byIndex {
		read := indexSettings{name: index, settings: map[string]any{}}
		flattenSettings(read.settings, "", body.Settings)
		indexBlock, _ := body.Settings["index"].(map[string]any)
		if analysis, ok := indexBlock["analysis"]; ok {
			if read.analysis, err = json.Marshal(analysis); err != nil {
				return indexSettings{}, fmt.Errorf("parse analysis of %s: %w", index, err)
			}
		}
		return read, nil
	}
	return indexSettings{}, nil
}

// flattenSettings flattens like ES flat_settings, minus the analysis block Analysis keeps whole.
func flattenSettings(flat map[string]any, prefix string, nested map[string]any) {
	for key, value := range nested {
		path := prefix + key
		if path == "index.analysis" {
			continue
		}
		if child, ok := value.(map[string]any); ok {
			flattenSettings(flat, path+".", child)
			continue
		}
		flat[path] = value
	}
}

func searchFieldMappings(properties map[string]types.Property) map[string]FieldMapping {
	fields := make(map[string]FieldMapping)
	collectSearchFields(fields, "", properties)
	return fields
}

func collectSearchFields(fields map[string]FieldMapping, prefix string, properties map[string]types.Property) {
	for name, property := range properties {
		path := prefix + name
		switch p := property.(type) {
		case *types.TextProperty:
			fields[path] = textFieldMapping(p)
			collectSearchFields(fields, path+".", p.Fields)
		case *types.DenseVectorProperty:
			fields[path] = vectorFieldMapping(p)
		case *types.ObjectProperty:
			collectSearchFields(fields, path+".", p.Properties)
		}
	}
}

func textFieldMapping(p *types.TextProperty) FieldMapping {
	field := FieldMapping{Type: "text"}
	if p.Analyzer != nil {
		field.Analyzer = *p.Analyzer
	}
	if p.SearchAnalyzer != nil {
		field.SearchAnalyzer = *p.SearchAnalyzer
	}
	if p.Similarity != nil {
		field.Similarity = *p.Similarity
	}
	if p.IndexOptions != nil {
		field.IndexOptions = p.IndexOptions.String()
	}
	return field
}

func vectorFieldMapping(p *types.DenseVectorProperty) FieldMapping {
	field := FieldMapping{Type: "dense_vector"}
	if p.Dims != nil {
		field.Dims = *p.Dims
	}
	if p.Similarity != nil {
		field.Similarity = p.Similarity.String()
	}
	if p.IndexOptions != nil {
		options := &VectorIndexOptions{Type: p.IndexOptions.Type.String()}
		if p.IndexOptions.M != nil {
			options.M = *p.IndexOptions.M
		}
		if p.IndexOptions.EfConstruction != nil {
			options.EfConstruction = *p.IndexOptions.EfConstruction
		}
		field.VectorIndex = options
	}
	return field
}
