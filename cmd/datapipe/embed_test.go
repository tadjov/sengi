package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/parquet-go/parquet-go"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/datapackage"
	"github.com/tadjov/tusker/internal/storage"
	pkgtesting "github.com/tadjov/tusker/pkg/testing"
)

const (
	fixtureTagDir     = "qwen3-embedding-0.6b@1024"
	qwenOllamaDigest  = "a80c4f17acd55265feec403c7aef86be0c25983ab279d83f3bcd3abbcb5b8b72"
	gemmaOllamaDigest = "85462619ee721b466c5927b9f8b2a1d2b4c5d6e7f8091a2b3c4d5e6f708192a3"
)

type embedCall struct {
	Input   []string       `json:"input"`
	Options map[string]any `json:"options"`
}

// fakeEmbeddingAPI points EMBEDDING_* at an Ollama that serves qwen3-embedding:0.6b and
// embeddinggemma:300m. Its /api/embed answers each lead with vectorOf(lead), refuses a lead
// vectorOf doesn't know, and records every request.
func fakeEmbeddingAPI(t *testing.T, vectorOf func(lead string) ([]float32, bool)) *[]embedCall {
	t.Helper()
	var calls []embedCall
	quantization := map[string]string{"qwen3-embedding:0.6b": "Q8_0", "embeddinggemma:300m": "BF16"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = fmt.Fprintf(w, `{"models": [
				{"name": "qwen3-embedding:0.6b", "model": "qwen3-embedding:0.6b", "digest": %q, "details": {"quantization_level": "Q8_0"}},
				{"name": "embeddinggemma:300m", "model": "embeddinggemma:300m", "digest": %q, "details": {"quantization_level": "BF16"}}
			]}`, qwenOllamaDigest, gemmaOllamaDigest)
		case "/api/show":
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			level, ok := quantization[body.Model]
			if !ok {
				http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"details": map[string]any{"format": "gguf", "quantization_level": level}})
		case "/api/embed":
			var call embedCall
			if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			calls = append(calls, call)
			embeddings := make([][]float32, len(call.Input))
			for i, lead := range call.Input {
				vector, ok := vectorOf(lead)
				if !ok {
					http.Error(w, "unexpected lead "+lead, http.StatusBadRequest)
					return
				}
				embeddings[i] = vector
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("EMBEDDING_BASE_URL", server.URL)
	t.Setenv("EMBEDDING_MODEL", "qwen3-embedding:0.6b")
	t.Setenv("EMBEDDING_DIM", "1024")
	return &calls
}

func unitVector(position int) []float32 {
	v := make([]float32, 1024)
	v[position] = 1
	return v
}

// fixtureLeadVectors gives loadFixtureArticles' article i the unit vector at position i+offset.
func fixtureLeadVectors(n, offset int) func(lead string) ([]float32, bool) {
	byLead := make(map[string][]float32, n)
	for i := range n {
		byLead[fmt.Sprintf("Storm number %d hits the coast Thousands without power", i)] = unitVector((i + offset) % 1024)
	}
	return func(lead string) ([]float32, bool) {
		v, ok := byLead[lead]
		return v, ok
	}
}

func embeddingsVersionDir(root, version string) string {
	return filepath.Join(root, "datasets", "fixture", "embeddings", fixtureTagDir, version)
}

func openEmbeddings(t *testing.T, dir string) datapackage.Package {
	t.Helper()
	opened, err := datapackage.Open(context.Background(), dir)
	require.NoError(t, err)
	return opened.Package
}

func readEmbeddingRows(t *testing.T, dir string) []producerRow {
	t.Helper()
	opened, err := datapackage.Open(context.Background(), dir)
	require.NoError(t, err)
	file, err := opened.Resource(context.Background(), datapackage.EmbeddingsResource)
	require.NoError(t, err)
	defer file.Close()
	reader := parquet.NewGenericReader[producerRow](file)
	defer reader.Close()
	rows := make([]producerRow, reader.NumRows())
	n, err := reader.Read(rows)
	if n < len(rows) {
		require.NoError(t, err)
	}
	return rows[:n]
}

func TestEmbed_PublishesEachCanonicalRowsVectorInCanonicalOrder(t *testing.T) {
	root := t.TempDir()
	articles := loadFixtureArticles(5)
	canonical := writeCanonicalPackage(t, root, articles)
	fakeEmbeddingAPI(t, fixtureLeadVectors(5, 0))

	require.NoError(t, execDatapipe(t, "", "embed", "--canonical", canonical.location, "--version", "go-test"))

	dir := embeddingsVersionDir(root, "go-test")
	pkg := openEmbeddings(t, dir)
	assert.Equal(t, &datapackage.Ref{Name: "fixture-canonical", Version: "article-v2-test", ResourceHash: canonical.resourceHash}, pkg.Canonical)
	assert.Equal(t, &datapackage.ProducedBy{Tool: "datapipe embed"}, pkg.ProducedBy)
	assert.Equal(t, &datapackage.Embedding{
		Model: "qwen3-embedding:0.6b", HuggingFaceModel: "Qwen/Qwen3-Embedding-0.6B", Revision: qwenOllamaDigest,
		Tag: "qwen3-embedding:0.6b@1024", Dim: 1024, Recipe: "lead-v1", Pooling: "last_token", Normalized: true,
		MaxTokens: 512, BatchSize: 64, DType: "q8_0",
	}, pkg.Embedding)
	assert.NoError(t, validateAgainstDescriptorSchema(t, filepath.Join(dir, datapackage.DescriptorName)))

	rows := readEmbeddingRows(t, dir)
	require.Len(t, rows, len(articles))
	for i, row := range rows {
		assert.Equal(t, articles[i].ID.String(), row.ID, "row %d", i)
		assert.Equal(t, unitVector(i), row.Embedding, "row %d", i)
	}
}

func TestEmbed_RefusesAnExistingVersionBeforeAnyRequest(t *testing.T) {
	root := t.TempDir()
	canonical := writeCanonicalPackage(t, root, loadFixtureArticles(5))
	args := []string{"embed", "--canonical", canonical.location, "--version", "go-test"}
	fakeEmbeddingAPI(t, fixtureLeadVectors(5, 0))
	require.NoError(t, execDatapipe(t, "", args...))
	dir := embeddingsVersionDir(root, "go-test")
	resource := filepath.Join(dir, "embeddings.parquet")
	before, err := os.Stat(resource)
	require.NoError(t, err)
	published := readEmbeddingRows(t, dir)

	rerun := fakeEmbeddingAPI(t, fixtureLeadVectors(5, 1))
	err = execDatapipe(t, "", args...)

	require.ErrorContains(t, err, dir)
	require.ErrorContains(t, err, "--version")
	assert.Empty(t, *rerun)
	after, err := os.Stat(resource)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "the published resource was replaced")
	assert.Equal(t, published, readEmbeddingRows(t, dir))
}

func TestEmbed_AFailedRunPublishesNothing(t *testing.T) {
	root := t.TempDir()
	canonical := writeCanonicalPackage(t, root, loadFixtureArticles(5))
	known := fixtureLeadVectors(5, 0)
	fakeEmbeddingAPI(t, func(lead string) ([]float32, bool) {
		if lead == "Storm number 3 hits the coast Thousands without power" {
			return nil, false
		}
		return known(lead)
	})

	err := execDatapipe(t, "", "embed", "--canonical", canonical.location, "--version", "go-test", "--batch-size", "2")

	require.Error(t, err)
	tagDir := filepath.Join(root, "datasets", "fixture", "embeddings", fixtureTagDir)
	left, err := os.ReadDir(tagDir)
	require.NoError(t, err)
	assert.Empty(t, left, "a failed run left %v in %s", left, tagDir)
}

func TestEmbed_SendsEveryRequestButTheLastAtTheBatchSize(t *testing.T) {
	root := t.TempDir()
	const articleCount = 500
	canonical := writeCanonicalPackage(t, root, loadFixtureArticles(articleCount))
	calls := fakeEmbeddingAPI(t, fixtureLeadVectors(articleCount, 0))

	require.NoError(t, execDatapipe(t, "", "embed", "--canonical", canonical.location, "--version", "go-test", "--batch-size", "3"))

	require.NotEmpty(t, *calls)
	embedded := 0
	for i, call := range *calls {
		embedded += len(call.Input)
		if i < len(*calls)-1 {
			assert.Len(t, call.Input, 3, "request %d", i)
		}
	}
	assert.Equal(t, articleCount, embedded)
	assert.Equal(t, 3, openEmbeddings(t, embeddingsVersionDir(root, "go-test")).Embedding.BatchSize)
}

func TestEmbed_SendsMaxTokensAsTheContextLengthAndRecordsIt(t *testing.T) {
	root := t.TempDir()
	canonical := writeCanonicalPackage(t, root, loadFixtureArticles(5))
	calls := fakeEmbeddingAPI(t, fixtureLeadVectors(5, 0))

	require.NoError(t, execDatapipe(t, "", "embed", "--canonical", canonical.location, "--version", "go-test", "--batch-size", "2", "--max-tokens", "256"))

	require.NotEmpty(t, *calls)
	for i, call := range *calls {
		assert.Equal(t, map[string]any{"num_ctx": float64(256)}, call.Options, "request %d", i)
	}
	assert.Equal(t, 256, openEmbeddings(t, embeddingsVersionDir(root, "go-test")).Embedding.MaxTokens)
}

func TestEmbed_RecordsTheSourceRepoGivenForAnotherModel(t *testing.T) {
	root := t.TempDir()
	canonical := writeCanonicalPackage(t, root, loadFixtureArticles(5))
	fakeEmbeddingAPI(t, fixtureLeadVectors(5, 0))
	t.Setenv("EMBEDDING_MODEL", "embeddinggemma:300m")

	require.NoError(t, execDatapipe(t, "", "embed", "--canonical", canonical.location, "--version", "go-test", "--hf-model", "google/embeddinggemma-300m"))

	pkg := openEmbeddings(t, filepath.Join(root, "datasets", "fixture", "embeddings", "embeddinggemma-300m@1024", "go-test"))
	assert.Equal(t, "embeddinggemma:300m", pkg.Embedding.Model)
	assert.Equal(t, "google/embeddinggemma-300m", pkg.Embedding.HuggingFaceModel)
	assert.Equal(t, "embeddinggemma:300m@1024", pkg.Embedding.Tag)
	assert.Equal(t, gemmaOllamaDigest, pkg.Embedding.Revision)
	assert.Equal(t, "bf16", pkg.Embedding.DType)
}

func TestEmbed_LimitEmbedsOnlyTheFirstRowsAndMarksATrial(t *testing.T) {
	root := t.TempDir()
	articles := loadFixtureArticles(5)
	canonical := writeCanonicalPackage(t, root, articles)
	fakeEmbeddingAPI(t, fixtureLeadVectors(5, 0))

	require.NoError(t, execDatapipe(t, "", "embed", "--canonical", canonical.location, "--version", "go-test-trial", "--limit", "3"))

	dir := embeddingsVersionDir(root, "go-test-trial")
	rows := readEmbeddingRows(t, dir)
	require.Len(t, rows, 3)
	for i, row := range rows {
		assert.Equal(t, articles[i].ID.String(), row.ID, "row %d", i)
	}
	limit := openEmbeddings(t, dir).Embedding.Limit
	require.NotNil(t, limit)
	assert.Equal(t, 3, *limit)
}

func TestEmbed_LoadRefusesALimitedRunWithoutTrial(t *testing.T) {
	t.Setenv("ES_ADDRESSES", "http://127.0.0.1:1")
	t.Setenv("ES_INDEX_NAME", "articles")
	root := t.TempDir()
	articles := loadFixtureArticles(5)
	canonical := writeCanonicalPackage(t, root, articles)
	fakeEmbeddingAPI(t, fixtureLeadVectors(5, 0))
	// A limit covering every row leaves the trial mark as the only reason to refuse.
	require.NoError(t, execDatapipe(t, "", "embed", "--canonical", canonical.location, "--version", "go-test-trial", "--limit", fmt.Sprint(len(articles))))

	err := execDatapipe(t, "", "load", "--engine", "es", "--canonical", canonical.location,
		"--embeddings", filepath.Join(embeddingsVersionDir(root, "go-test-trial"), datapackage.DescriptorName))

	require.ErrorContains(t, err, "--trial")
}

func TestEmbed_RefusesBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, canonical fixturePackage) []string
	}{
		{
			name: "no embedding API configured",
			setup: func(t *testing.T, canonical fixturePackage) []string {
				t.Setenv("EMBEDDING_BASE_URL", "")
				return []string{"--canonical", canonical.location}
			},
		},
		{
			name: "a canonical package outside datasets/<ds>/canonical/<version>/",
			setup: func(t *testing.T, canonical fixturePackage) []string {
				moved := filepath.Join(t.TempDir(), "elsewhere")
				require.NoError(t, os.Rename(filepath.Dir(canonical.location), moved))
				return []string{"--canonical", filepath.Join(moved, datapackage.DescriptorName)}
			},
		},
		{
			name: "a batch size below one",
			setup: func(t *testing.T, canonical fixturePackage) []string {
				return []string{"--canonical", canonical.location, "--batch-size", "0"}
			},
		},
		{
			name: "max tokens below one",
			setup: func(t *testing.T, canonical fixturePackage) []string {
				return []string{"--canonical", canonical.location, "--max-tokens", "0"}
			},
		},
		{
			name: "another model without its source repo",
			setup: func(t *testing.T, canonical fixturePackage) []string {
				t.Setenv("EMBEDDING_MODEL", "embeddinggemma:300m")
				return []string{"--canonical", canonical.location}
			},
		},
		{
			name: "a model the API does not list",
			setup: func(t *testing.T, canonical fixturePackage) []string {
				t.Setenv("EMBEDDING_MODEL", "qwen3-embedding:4b")
				return []string{"--canonical", canonical.location, "--hf-model", "Qwen/Qwen3-Embedding-4B"}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			canonical := writeCanonicalPackage(t, root, loadFixtureArticles(5))
			calls := fakeEmbeddingAPI(t, fixtureLeadVectors(5, 0))
			args := tt.setup(t, canonical)

			err := execDatapipe(t, "", append([]string{"embed"}, args...)...)

			require.Error(t, err)
			assert.Empty(t, *calls)
			assert.NoDirExists(t, filepath.Join(root, "datasets", "fixture", "embeddings"))
		})
	}
}

type storedEmbedding struct {
	Model     string
	Embedding []float32
}

func storedEmbeddings(t *testing.T, connString string) map[uuid.UUID]storedEmbedding {
	t.Helper()
	stored := map[uuid.UUID]storedEmbedding{}
	withLoadConn(t, connString, func(conn *pgx.Conn) error {
		if err := pgxvec.RegisterTypes(context.Background(), conn); err != nil {
			return err
		}
		rows, err := conn.Query(context.Background(), "SELECT article_id, model_name, embedding FROM article_embeddings")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var model string
			var vector pgvector.Vector
			if err := rows.Scan(&id, &model, &vector); err != nil {
				return err
			}
			stored[id] = storedEmbedding{Model: model, Embedding: vector.Slice()}
		}
		return rows.Err()
	})
	return stored
}

func TestEmbed_LoadStoresTheVectorTheAPIReturnedForEachArticlesLead(t *testing.T) {
	server := pkgtesting.NewPGContainerWithConfig(context.Background(), t, pkgtesting.PGConfig{
		Engine:     storage.PostgresNative,
		Unmigrated: true,
	})
	db := newLoadDatabase(t, server.ConnString)
	root := t.TempDir()
	articles := loadFixtureArticles(20)
	canonical := writeCanonicalPackage(t, root, articles)
	vectorOf := fixtureLeadVectors(len(articles), 0)
	fakeEmbeddingAPI(t, vectorOf)
	require.NoError(t, execDatapipe(t, "", "embed", "--canonical", canonical.location, "--version", "go-test", "--batch-size", "8"))

	err := execDatapipe(t, db, "load", "--engine", string(storage.PostgresNative), "--canonical", canonical.location,
		"--embeddings", filepath.Join(embeddingsVersionDir(root, "go-test"), datapackage.DescriptorName))
	require.NoError(t, err)

	stored := storedEmbeddings(t, db)
	for i, article := range articles {
		returned, ok := vectorOf(fmt.Sprintf("Storm number %d hits the coast Thousands without power", i))
		require.True(t, ok)
		assert.Equal(t, storedEmbedding{Model: "qwen3-embedding:0.6b@1024", Embedding: returned}, stored[article.ID], "article %d", i)
	}
}
