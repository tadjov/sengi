package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/tadjov/sengi/internal/api/router"
	"github.com/tadjov/sengi/internal/api/server"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/es"
	"github.com/tadjov/sengi/internal/storage/factory"
	"github.com/tadjov/sengi/internal/storage/pg"
	"github.com/tadjov/sengi/internal/storage/pg/pgtest"
	"github.com/tadjov/sengi/internal/storage/storagetest"
	"github.com/tadjov/sengi/internal/testcontainer"
	dquery "github.com/tadjov/sengi/internal/types/query"
)

func pgSearchConfig(connStr string) *NewsSearchConfig {
	return &NewsSearchConfig{
		StorageConfig: factory.StorageConfig{
			Type: storage.PG,
			Pg:   &pg.PoolConfig{ConnStr: connStr},
		},
		EmbeddingConfig: embedding.Config{Enabled: true, BaseURL: "http://127.0.0.1:1"},
	}
}

func healthStatus(t *testing.T, backend *searchBackend) int {
	t.Helper()
	s, err := server.New(&server.Config{Port: "0"}, backend.health)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	s.SetupHealthChecks("/health")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec.Code
}

func TestOpenSearchBackend_PostgresSearchersShareOnePool(t *testing.T) {
	ctx := context.Background()
	container := testcontainer.NewPGContainerWithCleanup(ctx, t)

	backend, err := openSearchBackend(ctx, pgSearchConfig(container.ConnString+"&pool_max_conns=1"))
	if err != nil {
		t.Fatalf("openSearchBackend: %v", err)
	}
	t.Cleanup(backend.close)
	if backend.semantic == nil || backend.hybrid == nil {
		t.Fatalf("embeddings enabled but semantic=%v hybrid=%v", backend.semantic, backend.hybrid)
	}

	probe, err := pgx.Connect(ctx, container.ConnString)
	if err != nil {
		t.Fatalf("connect probe: %v", err)
	}
	t.Cleanup(func() { _ = probe.Close(ctx) })

	var connections int
	err = probe.QueryRow(ctx, `
		SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database()
		  AND backend_type = 'client backend'
		  AND pid <> pg_backend_pid()`).Scan(&connections)
	if err != nil {
		t.Fatalf("count connections: %v", err)
	}

	if connections > 1 {
		t.Errorf("API holds %d connections with pool_max_conns=1; searchers do not share one pool", connections)
	}
}

func TestOpenSearchBackend_HealthFollowsPostgres(t *testing.T) {
	ctx := context.Background()
	// The test stops its server.
	container := testcontainer.NewPGContainerWithConfig(ctx, t, testcontainer.PGConfig{Isolated: true})

	running, err := openSearchBackend(ctx, pgSearchConfig(container.ConnString))
	if err != nil {
		t.Fatalf("openSearchBackend: %v", err)
	}
	t.Cleanup(running.close)
	closed, err := openSearchBackend(ctx, pgSearchConfig(container.ConnString))
	if err != nil {
		t.Fatalf("openSearchBackend: %v", err)
	}

	if got := healthStatus(t, running); got != http.StatusOK {
		t.Fatalf("/health with Postgres up = %d, want %d", got, http.StatusOK)
	}

	closed.close()
	if got := healthStatus(t, closed); got == http.StatusOK {
		t.Errorf("/health after the backend closed = %d, want non-200", got)
	}

	stopTimeout := 5 * time.Second
	if err := container.Container.Stop(ctx, &stopTimeout); err != nil {
		t.Fatalf("stop postgres: %v", err)
	}
	if got := healthStatus(t, running); got == http.StatusOK {
		t.Errorf("/health with Postgres stopped = %d, want non-200", got)
	}
}

func TestOpenSearchBackend_HealthFollowsElasticsearch(t *testing.T) {
	reachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(reachable.Close)

	tests := []struct {
		name    string
		address string
		healthy bool
	}{
		{name: "cluster answers", address: reachable.URL, healthy: true},
		{name: "nothing listening", address: unusedAddress(t), healthy: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &NewsSearchConfig{StorageConfig: factory.StorageConfig{
				Type: storage.ES,
				Es:   &es.ClientConfig{Addresses: []string{tt.address}, IndexName: "articles"},
			}}
			backend, err := openSearchBackend(context.Background(), cfg)
			if err != nil {
				t.Fatalf("openSearchBackend: %v", err)
			}
			t.Cleanup(backend.close)

			got := healthStatus(t, backend)
			if tt.healthy && got != http.StatusOK {
				t.Errorf("/health = %d, want %d", got, http.StatusOK)
			}
			if !tt.healthy && got == http.StatusOK {
				t.Errorf("/health = %d, want non-200", got)
			}
		})
	}
}

func unusedAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := "http://" + ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return address
}

// fakeCluster answers as Elasticsearch with no hits, its articles index loaded under load.
type fakeCluster struct {
	url      string
	accepted atomic.Int64
	mu       sync.Mutex
	searches []json.RawMessage
}

func fakeElasticsearch(t *testing.T, load *storage.LoadRecord) *fakeCluster {
	t.Helper()
	meta := map[string]any{}
	if load != nil {
		meta["load"] = load
	}
	mapping, err := json.Marshal(map[string]any{"articles": map[string]any{"mappings": map[string]any{"_meta": meta}}})
	if err != nil {
		t.Fatalf("encode mapping: %v", err)
	}
	cluster := &fakeCluster{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/_mapping"):
			_, _ = w.Write(mapping)
		default:
			body, _ := io.ReadAll(r.Body)
			cluster.mu.Lock()
			cluster.searches = append(cluster.searches, body)
			cluster.mu.Unlock()
			_, _ = io.WriteString(w, `{"took":1,"timed_out":false,"_shards":{"total":1,"successful":1,"skipped":0,"failed":0},"hits":{"total":{"value":0,"relation":"eq"},"max_score":null,"hits":[]}}`)
		}
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			cluster.accepted.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	cluster.url = srv.URL
	return cluster
}

func (c *fakeCluster) knnSearches(t *testing.T) []knnSearch {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var searches []knnSearch
	for _, body := range c.searches {
		var request struct {
			Knn []knnSearch `json:"knn"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("decode search %s: %v", body, err)
		}
		searches = append(searches, request.Knn...)
	}
	return searches
}

type knnSearch struct {
	QueryVector []float32 `json:"query_vector"`
	Filter      []struct {
		Term map[string]struct {
			Value string `json:"value"`
		} `json:"term"`
	} `json:"filter"`
}

func (k knnSearch) modelFilter() string {
	for _, filter := range k.Filter {
		if term, ok := filter.Term["embedding_model"]; ok {
			return term.Value
		}
	}
	return ""
}

// fakeOllama answers every embedding request with a 1024-wide vector and records the model each one names.
func fakeOllama(t *testing.T) (url string, models func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request embedding.OllamaRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		mu.Lock()
		asked = append(asked, request.Model)
		mu.Unlock()
		unit := make([]float32, 1024)
		unit[0] = 1
		_ = json.NewEncoder(w).Encode(embedding.Response{Embedding: unit})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}

func esSearchConfig(cluster *fakeCluster, ollama string, configured embedding.Config) *NewsSearchConfig {
	configured.Enabled, configured.BaseURL = true, ollama
	return &NewsSearchConfig{
		StorageConfig: factory.StorageConfig{
			Type: storage.ES,
			Es:   &es.ClientConfig{Addresses: []string{cluster.url}, IndexName: "articles"},
		},
		EmbeddingConfig: configured,
	}
}

func loadedWith(tag string) *storage.LoadRecord {
	return &storage.LoadRecord{Engine: string(storage.ES), ModelTag: tag}
}

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestOpenSearchBackend_EmbedsQueriesWithTheLoadedModel(t *testing.T) {
	defaultTag := embedding.Tag(embedding.DefaultModel, embedding.DefaultDim)
	tests := []struct {
		name       string
		load       *storage.LoadRecord
		configured embedding.Config
		wantModel  string
		wantTag    string
		wantWidth  int
		wantWarn   bool
	}{
		{
			name:      "nothing configured takes the load record's model and width",
			load:      loadedWith("bge-m3@256"),
			wantModel: "bge-m3", wantTag: "bge-m3@256", wantWidth: 256,
		},
		{
			name:       "the example env's plain model agrees with its load at 512",
			load:       loadedWith(defaultTag),
			configured: embedding.Config{Model: embedding.DefaultModel},
			wantModel:  embedding.DefaultModel, wantTag: defaultTag, wantWidth: embedding.DefaultDim,
		},
		{
			name:       "a configured width the load agrees with",
			load:       loadedWith("bge-m3@256"),
			configured: embedding.Config{Dim: 256},
			wantModel:  "bge-m3", wantTag: "bge-m3@256", wantWidth: 256,
		},
		{
			name:       "no load record keeps the configured model and width, and warns",
			configured: embedding.Config{Model: "bge-m3", Dim: 256},
			wantModel:  "bge-m3", wantTag: "bge-m3@256", wantWidth: 256, wantWarn: true,
		},
		{
			name:      "no load record and nothing configured takes the defaults, and warns",
			wantModel: embedding.DefaultModel, wantTag: defaultTag, wantWidth: embedding.DefaultDim, wantWarn: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := captureWarnings(t)
			cluster := fakeElasticsearch(t, tt.load)
			ollama, models := fakeOllama(t)
			ctx := context.Background()

			backend, err := openSearchBackend(ctx, esSearchConfig(cluster, ollama, tt.configured))
			if err != nil {
				t.Fatalf("openSearchBackend: %v", err)
			}
			t.Cleanup(backend.close)
			if _, err := backend.semantic.SearchSemantic(ctx, dquery.NewSemantic("climate"), &dquery.BaseOptions{Size: 10}); err != nil {
				t.Fatalf("semantic search: %v", err)
			}

			if got := models(); !slices.Equal(got, []string{tt.wantModel}) {
				t.Errorf("Ollama asked for %v, want [%s]", got, tt.wantModel)
			}
			searches := cluster.knnSearches(t)
			if len(searches) != 1 {
				t.Fatalf("cluster received %d kNN searches, want 1", len(searches))
			}
			if got := searches[0].modelFilter(); got != tt.wantTag {
				t.Errorf("kNN filtered on model %q, want %q", got, tt.wantTag)
			}
			if got := len(searches[0].QueryVector); got != tt.wantWidth {
				t.Errorf("query vector has %d dimensions, want %d", got, tt.wantWidth)
			}
			if warned := strings.Contains(warnings.String(), "level=WARN"); warned != tt.wantWarn {
				t.Errorf("warned = %v, want %v; log: %s", warned, tt.wantWarn, warnings)
			}
		})
	}
}

func TestOpenSearchBackend_RefusesToStartAgainstALoadItCannotServe(t *testing.T) {
	defaultTag := embedding.Tag(embedding.DefaultModel, embedding.DefaultDim)
	tests := []struct {
		name       string
		load       *storage.LoadRecord
		configured embedding.Config
		want       []string
	}{
		{
			name:       "EMBEDDING_MODEL names another model",
			load:       loadedWith(defaultTag),
			configured: embedding.Config{Model: "bge-m3"},
			want:       []string{"EMBEDDING_MODEL", "bge-m3", embedding.DefaultModel},
		},
		{
			name:       "EMBEDDING_DIM names another width",
			load:       loadedWith(defaultTag),
			configured: embedding.Config{Dim: 1024},
			want:       []string{"EMBEDDING_DIM", "1024", fmt.Sprint(embedding.DefaultDim)},
		},
		{
			name: "the corpus was loaded without embeddings",
			load: loadedWith(""),
			want: []string{"without embeddings"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ollama, _ := fakeOllama(t)

			_, err := openSearchBackend(context.Background(), esSearchConfig(fakeElasticsearch(t, tt.load), ollama, tt.configured))

			if err == nil {
				t.Fatal("openSearchBackend started")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
	}
}

func TestOpenSearchBackend_PostgresRefusesAModelItsLoadDisagreesWith(t *testing.T) {
	ctx := context.Background()
	container := testcontainer.NewPGContainerWithCleanup(ctx, t)
	articles := storagetest.ParityCorpus()
	vectors := make([]embedding.Vec, len(articles))
	for i, a := range articles {
		values := make([]float32, embedding.DefaultDim)
		values[i%embedding.DefaultDim] = 1
		vectors[i] = embedding.Vec{ID: a.ID, Model: "bge-m3@512", Embedding: values}
	}
	connString := pgtest.Load(t, container.ConnString, pgtest.Corpus{Articles: articles, Vectors: vectors, ModelTag: "bge-m3@512"})
	cfg := pgSearchConfig(connString)
	cfg.EmbeddingConfig.Model = embedding.DefaultModel

	_, err := openSearchBackend(ctx, cfg)

	if err == nil {
		t.Fatal("openSearchBackend started against a corpus loaded with another model")
	}
	for _, want := range []string{"bge-m3", embedding.DefaultModel} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestOpenSearchBackend_ElasticsearchSearchersShareOneClient(t *testing.T) {
	cluster := fakeElasticsearch(t, loadedWith(embedding.Tag(embedding.DefaultModel, embedding.DefaultDim)))
	ollama, _ := fakeOllama(t)
	backend, err := openSearchBackend(context.Background(), esSearchConfig(cluster, ollama, embedding.Config{}))
	if err != nil {
		t.Fatalf("openSearchBackend: %v", err)
	}
	t.Cleanup(backend.close)
	if backend.semantic == nil || backend.hybrid == nil {
		t.Fatalf("embeddings enabled but semantic=%v hybrid=%v", backend.semantic, backend.hybrid)
	}

	ctx := context.Background()
	opts := &dquery.BaseOptions{Size: 10}
	if !backend.health.Healthy(ctx) {
		t.Fatal("health check failed against the fake cluster")
	}
	if _, err := backend.fts.(storage.StringQuerySearcher).SearchStringQuery(ctx, dquery.NewString("climate", dquery.StringOptions{}), opts); err != nil {
		t.Fatalf("full-text search: %v", err)
	}
	if _, err := backend.semantic.SearchSemantic(ctx, dquery.NewSemantic("climate"), opts); err != nil {
		t.Fatalf("semantic search: %v", err)
	}
	if _, err := backend.hybrid.SearchHybrid(ctx, dquery.NewHybrid("climate", dquery.HybridOptions{}), opts); err != nil {
		t.Fatalf("hybrid search: %v", err)
	}

	if got := cluster.accepted.Load(); got > 1 {
		t.Errorf("serial requests opened %d connections to Elasticsearch; searchers do not share one client", got)
	}
}

func TestServer_SearchPastRequestDeadlineReleasesItsConnection(t *testing.T) {
	ctx := context.Background()
	container := testcontainer.NewPGContainerWithCleanup(ctx, t)
	backend, err := openSearchBackend(ctx, pgSearchConfig(container.ConnString+"&pool_max_conns=1"))
	if err != nil {
		t.Fatalf("openSearchBackend: %v", err)
	}
	t.Cleanup(backend.close)

	s, err := server.New(&server.Config{Port: "0", RequestTimeout: 200 * time.Millisecond}, backend.health)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	s.SetupMiddlewares().SetupErrorHandler().SetupHealthChecks("/health")
	router.NewSearchRouter(s.Echo, router.Searchers{FullText: backend.fts}).Bind()

	locker, err := pgx.Connect(ctx, container.ConnString)
	if err != nil {
		t.Fatalf("connect locker: %v", err)
	}
	t.Cleanup(func() { _ = locker.Close(ctx) })
	lock, err := locker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = lock.Rollback(ctx) })
	if _, err := lock.Exec(ctx, "LOCK TABLE articles IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatalf("lock articles: %v", err)
	}

	search := httptest.NewRecorder()
	answered := make(chan struct{})
	go func() {
		s.ServeHTTP(search, httptest.NewRequest(http.MethodGet, "/v1/articles/search?q=climate", nil))
		close(answered)
	}()
	// The guard only stops a search that ignores its deadline from hanging the test.
	select {
	case <-answered:
	case <-time.After(30 * time.Second):
		t.Fatal("search blocked on a locked table outlived its request deadline")
	}
	if search.Code == http.StatusOK {
		t.Fatalf("search on a locked table = %d, want an error", search.Code)
	}

	health := httptest.NewRecorder()
	s.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Errorf("/health after the cancelled search = %d, want %d: its connection was not released", health.Code, http.StatusOK)
	}
}
