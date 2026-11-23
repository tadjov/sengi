package main

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/ingest"
	"github.com/tadjov/sengi/internal/storage/queryvectors"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/pkg/datapackage"
)

const (
	truncatedTag    = "qwen3-embedding:0.6b@512"
	truncatedTagDir = "qwen3-embedding-0.6b@512"
)

// sourceVector is 1024 wide: head in its first 512 values, then a tail of ones that a cut to 512 drops.
func sourceVector(head ...float32) []float32 {
	v := make([]float32, 1024)
	copy(v, head)
	for i := 512; i < len(v); i++ {
		v[i] = 1
	}
	return v
}

func vector512(head ...float32) []float32 {
	v := make([]float32, 512)
	copy(v, head)
	return v
}

// denseCorpus returns rows whose 512-wide prefix is (3, 4, 0, …) or (0, 0, 5, 12, 0, …) in turn,
// and what each row becomes cut to 512.
func denseCorpus(articles []document.Article) ([]producerRow, [][]float32) {
	rows := make([]producerRow, len(articles))
	want := make([][]float32, len(articles))
	for i, a := range articles {
		if i%2 == 0 {
			rows[i] = producerRow{ID: a.ID.String(), Embedding: sourceVector(3, 4)}
			want[i] = vector512(0.6, 0.8)
		} else {
			rows[i] = producerRow{ID: a.ID.String(), Embedding: sourceVector(0, 0, 5, 12)}
			want[i] = vector512(0, 0, 5.0/13, 12.0/13)
		}
	}
	return rows, want
}

func assertCutVector(t *testing.T, want, got []float32, msgAndArgs ...any) {
	t.Helper()
	require.Len(t, got, len(want), msgAndArgs...)
	var largestGap, sumSquares float64
	for i := range want {
		largestGap = max(largestGap, math.Abs(float64(got[i]-want[i])))
		sumSquares += float64(got[i]) * float64(got[i])
	}
	assert.Less(t, largestGap, 1e-6, msgAndArgs...)
	assert.InDelta(t, 1, math.Sqrt(sumSquares), 1e-6, msgAndArgs...)
}

type corpusTruncation struct {
	root      string
	articles  []document.Article
	canonical fixturePackage
	source    fixturePackage
	want      [][]float32
}

// newCorpusTruncation leaves no embedding API configured, so a run that reaches for one fails.
func newCorpusTruncation(t *testing.T) corpusTruncation {
	t.Helper()
	t.Setenv("EMBEDDING_BASE_URL", "")
	root := t.TempDir()
	articles := loadFixtureArticles(5)
	canonical := writeCanonicalPackage(t, root, articles)
	rows, want := denseCorpus(articles)
	return corpusTruncation{
		root: root, articles: articles, canonical: canonical, want: want,
		source: writeEmbeddingRows(t, root, canonical, rows, nil),
	}
}

func (c corpusTruncation) tagDir() string {
	return filepath.Join(c.root, "datasets", "fixture", "embeddings", truncatedTagDir)
}

const fixtureSuiteID = "fixture-semantic"

const fixtureSuite = `schema_version: 1
id: fixture-semantic
templates:
  - id: knn
    args: [embedding_model, query_vector]
    query: "SELECT article_id FROM article_embeddings WHERE model_name = $1 ORDER BY embedding <=> $2::vector LIMIT 10"
queries:
  - id: q1
    text: "climate change policy"
    description: "Policy responses to climate change"
    engines:
      pg: { template: knn }
  - id: q2
    text: "central bank interest rates"
    description: "Central banks setting interest rates"
    engines:
      pg: { template: knn }
`

type queryVectorsTruncation struct {
	root   string
	suite  string
	source fixturePackage
	rows   []queryvectors.Row
	want   [][]float32
	out    string
}

func newQueryVectorsTruncation(t *testing.T) queryVectorsTruncation {
	t.Helper()
	t.Setenv("EMBEDDING_BASE_URL", "")
	root := t.TempDir()
	track := filepath.Join(root, "track")
	require.NoError(t, os.MkdirAll(track, 0o755))
	suite := filepath.Join(track, "suite.yaml")
	require.NoError(t, os.WriteFile(suite, []byte(fixtureSuite), 0o644))
	rows := []queryvectors.Row{
		{QueryID: "q1", Text: "climate change policy", Embedding: sourceVector(3, 4)},
		{QueryID: "q2", Text: "central bank interest rates", Embedding: sourceVector(0, 0, 5, 12)},
	}
	return queryVectorsTruncation{
		root: root, suite: suite, rows: rows,
		source: writeQueryVectorsSource(t, filepath.Join(track, "trec", "query-vectors"), rows),
		want:   [][]float32{vector512(0.6, 0.8), vector512(0, 0, 5.0/13, 12.0/13)},
		out:    filepath.Join(root, "track-512", "trec", "query-vectors"),
	}
}

func writeQueryVectorsSource(t *testing.T, dir string, rows []queryvectors.Row) fixturePackage {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "query-vectors.parquet")
	f, err := os.Create(path)
	require.NoError(t, err)
	w := parquet.NewGenericWriter[queryvectors.Row](f)
	_, err = w.Write(rows)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	pkg, err := datapackage.Write(context.Background(), dir, datapackage.Package{
		Name:       "fixture-semantic-query-vectors",
		Version:    "4271fbe1",
		ProducedBy: &datapackage.ProducedBy{Tool: "embed_queries.py"},
		Embedding: &datapackage.Embedding{
			Model: "qwen3-embedding:0.6b", HuggingFaceModel: "Qwen/Qwen3-Embedding-0.6B", Revision: "abc123",
			Tag: loadFixtureModelTag, Dim: 1024, Recipe: queryvectors.QueryRecipe, Pooling: "last_token", Normalized: true,
			MaxTokens: 512, BatchSize: 1, DType: "float32",
		},
		Suite:     &datapackage.Suite{ID: fixtureSuiteID},
		Resources: []datapackage.Resource{{Name: datapackage.QueryVectorsResource, Path: "query-vectors.parquet", Format: "parquet"}},
	})
	require.NoError(t, err)
	return fixturePackage{location: filepath.Join(dir, datapackage.DescriptorName), resourcePath: path, resourceHash: pkg.Resources[0].Hash}
}

func readQueryVectorRows(t *testing.T, dir string) []queryvectors.Row {
	t.Helper()
	opened, err := datapackage.Open(context.Background(), dir)
	require.NoError(t, err)
	file, err := opened.Resource(context.Background(), datapackage.QueryVectorsResource)
	require.NoError(t, err)
	defer file.Close()
	reader := parquet.NewGenericReader[queryvectors.Row](file)
	defer reader.Close()
	rows := make([]queryvectors.Row, reader.NumRows())
	n, err := reader.Read(rows)
	if n < len(rows) {
		require.NoError(t, err)
	}
	return rows[:n]
}

// tree lists every path under dir; nil when dir does not exist.
func tree(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && path == dir {
			return nil
		}
		if err != nil {
			return err
		}
		if path != dir {
			paths = append(paths, path)
		}
		return nil
	})
	require.NoError(t, err)
	return paths
}

func TestEmbedTruncate_CutsEachCorpusVectorToItsRenormalisedPrefix(t *testing.T) {
	c := newCorpusTruncation(t)

	require.NoError(t, execDatapipe(t, "", "embed", "--embeddings", c.source.location, "--truncate-to", "512"))

	rows := readEmbeddingRows(t, filepath.Join(c.tagDir(), "v1"))
	require.Len(t, rows, len(c.articles))
	for i, row := range rows {
		assert.Equal(t, c.articles[i].ID.String(), row.ID, "row %d", i)
		assertCutVector(t, c.want[i], row.Embedding, "row %d", i)
	}
}

func TestEmbedTruncate_DescribesTheCutCorpusPackageAndItsSource(t *testing.T) {
	c := newCorpusTruncation(t)
	source := openEmbeddings(t, c.source.location)

	require.NoError(t, execDatapipe(t, "", "embed", "--embeddings", c.source.location, "--truncate-to", "512"))

	dir := filepath.Join(c.tagDir(), "v1")
	opened, err := datapackage.Open(context.Background(), dir)
	require.NoError(t, err)
	pkg := opened.Package
	assert.Equal(t, "v1", pkg.Version)
	assert.Equal(t, &datapackage.ProducedBy{Tool: "datapipe embed"}, pkg.ProducedBy)
	assert.Equal(t, source.Canonical, pkg.Canonical)
	assert.Equal(t, &datapackage.DerivedFrom{
		Name: "fixture-embeddings", Version: source.Version, Tag: "qwen3-embedding:0.6b@1024", ResourceHash: c.source.resourceHash,
	}, pkg.DerivedFrom)
	want := *source.Embedding
	want.Dim, want.Tag = 512, truncatedTag
	assert.Equal(t, &want, pkg.Embedding)
	assert.NoError(t, validateAgainstDescriptorSchema(t, filepath.Join(dir, datapackage.DescriptorName)))
	resource, err := opened.Resource(context.Background(), datapackage.EmbeddingsResource)
	require.NoError(t, err)
	require.NoError(t, resource.Close())
}

func TestEmbedTruncate_PublishesUnderTheVersionGiven(t *testing.T) {
	c := newCorpusTruncation(t)

	require.NoError(t, execDatapipe(t, "", "embed", "--embeddings", c.source.location, "--truncate-to", "512", "--version", "go-test"))

	assert.Equal(t, "go-test", openEmbeddings(t, filepath.Join(c.tagDir(), "go-test")).Version)
}

// The engines' vector columns are 1024 wide, so this stops where load hands the corpus to one.
func TestEmbedTruncate_LoadTakesTheCutPackageWithItsCanonical(t *testing.T) {
	c := newCorpusTruncation(t)
	require.NoError(t, execDatapipe(t, "", "embed", "--embeddings", c.source.location, "--truncate-to", "512"))

	packaged, err := ingest.OpenCorpus(context.Background(), ingest.CorpusOptions{
		Canonical:  c.canonical.location,
		Embeddings: filepath.Join(c.tagDir(), "v1", datapackage.DescriptorName),
	})
	require.NoError(t, err)
	defer packaged.Close()

	corpus := packaged.Corpus(context.Background())
	assert.Equal(t, truncatedTag, corpus.ModelTag)
	var ids []uuid.UUID
	for vec, err := range corpus.Vectors {
		require.NoError(t, err)
		assert.Len(t, vec.Embedding, 512, "vector %s", vec.ID)
		ids = append(ids, vec.ID)
	}
	want := make([]uuid.UUID, len(c.articles))
	for i, a := range c.articles {
		want[i] = a.ID
	}
	assert.Equal(t, want, ids)
}

func TestEmbedTruncate_CutsATracksQueryVectorsIntoOut(t *testing.T) {
	q := newQueryVectorsTruncation(t)

	require.NoError(t, execDatapipe(t, "", "embed", "--embeddings", q.source.location, "--truncate-to", "512", "--out", q.out))

	pkg := openEmbeddings(t, q.out)
	assert.Equal(t, &datapackage.Suite{ID: fixtureSuiteID}, pkg.Suite)
	assert.Empty(t, pkg.Version)
	assert.Equal(t, &datapackage.ProducedBy{Tool: "datapipe embed"}, pkg.ProducedBy)
	assert.Equal(t, &datapackage.DerivedFrom{
		Name: "fixture-semantic-query-vectors", Version: "4271fbe1", Tag: "qwen3-embedding:0.6b@1024", ResourceHash: q.source.resourceHash,
	}, pkg.DerivedFrom)
	assert.Equal(t, truncatedTag, pkg.Embedding.Tag)
	assert.Equal(t, 512, pkg.Embedding.Dim)
	assert.NoError(t, validateAgainstDescriptorSchema(t, filepath.Join(q.out, datapackage.DescriptorName)))

	rows := readQueryVectorRows(t, q.out)
	require.Len(t, rows, len(q.rows))
	for i, row := range rows {
		assert.Equal(t, q.rows[i].QueryID, row.QueryID, "row %d", i)
		assert.Equal(t, q.rows[i].Text, row.Text, "row %d", i)
		assertCutVector(t, q.want[i], row.Embedding, "row %d", i)
	}

	store, err := queryvectors.Open(context.Background(), queryvectors.Options{Package: q.out, Suite: q.suite, Model: truncatedTag})
	require.NoError(t, err)
	for i, row := range q.rows {
		got, err := store.QueryVector(context.Background(), row.Text)
		require.NoError(t, err)
		assertCutVector(t, q.want[i], got, "query %s", row.QueryID)
	}
}

func TestEmbedTruncate_RefusesBeforeReadingTheSource(t *testing.T) {
	tests := []struct {
		name string
		args func(t *testing.T, c corpusTruncation, q queryVectorsTruncation) []string
	}{
		{"both inputs", func(_ *testing.T, c corpusTruncation, _ queryVectorsTruncation) []string {
			return []string{c.canonical.location, "--embeddings", c.source.location, "--truncate-to", "512"}
		}},
		{"neither input", func(*testing.T, corpusTruncation, queryVectorsTruncation) []string {
			return []string{"--truncate-to", "512"}
		}},
		{"--truncate-to with a canonical package", func(_ *testing.T, c corpusTruncation, _ queryVectorsTruncation) []string {
			return []string{c.canonical.location, "--truncate-to", "512"}
		}},
		{"--out with a canonical package", func(_ *testing.T, c corpusTruncation, q queryVectorsTruncation) []string {
			return []string{c.canonical.location, "--out", q.out}
		}},
		{"--embeddings without --truncate-to", func(_ *testing.T, c corpusTruncation, _ queryVectorsTruncation) []string {
			return []string{"--embeddings", c.source.location}
		}},
		{"a negative width", func(_ *testing.T, c corpusTruncation, _ queryVectorsTruncation) []string {
			return []string{"--embeddings", c.source.location, "--truncate-to", "-1"}
		}},
		{"--limit with --embeddings", func(_ *testing.T, c corpusTruncation, _ queryVectorsTruncation) []string {
			return []string{"--embeddings", c.source.location, "--truncate-to", "512", "--limit", "3"}
		}},
		{"--hf-model with --embeddings", func(_ *testing.T, c corpusTruncation, _ queryVectorsTruncation) []string {
			return []string{"--embeddings", c.source.location, "--truncate-to", "512", "--hf-model", "Qwen/Qwen3-Embedding-0.6B"}
		}},
		{"the source's own width", func(_ *testing.T, c corpusTruncation, _ queryVectorsTruncation) []string {
			return []string{"--embeddings", c.source.location, "--truncate-to", "1024"}
		}},
		{"wider than the source", func(_ *testing.T, c corpusTruncation, _ queryVectorsTruncation) []string {
			return []string{"--embeddings", c.source.location, "--truncate-to", "2048"}
		}},
		{"--out with a corpus source", func(_ *testing.T, c corpusTruncation, q queryVectorsTruncation) []string {
			return []string{"--embeddings", c.source.location, "--truncate-to", "512", "--out", q.out}
		}},
		{"query vectors without --out", func(_ *testing.T, _ corpusTruncation, q queryVectorsTruncation) []string {
			return []string{"--embeddings", q.source.location, "--truncate-to", "512"}
		}},
		{"a corpus source outside datasets/<ds>/embeddings/<tag-dir>/<version>/", func(t *testing.T, c corpusTruncation, _ queryVectorsTruncation) []string {
			moved := filepath.Join(t.TempDir(), "elsewhere")
			require.NoError(t, os.Rename(filepath.Dir(c.source.location), moved))
			return []string{"--embeddings", filepath.Join(moved, datapackage.DescriptorName), "--truncate-to", "512"}
		}},
		{"an existing output version", func(t *testing.T, c corpusTruncation, _ queryVectorsTruncation) []string {
			require.NoError(t, os.MkdirAll(filepath.Join(c.tagDir(), "v1"), 0o755))
			return []string{"--embeddings", c.source.location, "--truncate-to", "512"}
		}},
		{"an existing --out", func(t *testing.T, _ corpusTruncation, q queryVectorsTruncation) []string {
			require.NoError(t, os.MkdirAll(q.out, 0o755))
			return []string{"--embeddings", q.source.location, "--truncate-to", "512", "--out", q.out}
		}},
		{"a source in a language no engine analyses", func(t *testing.T, c corpusTruncation, _ queryVectorsTruncation) []string {
			setDescriptorLanguage(t, c.source.location, "en")
			return []string{"--embeddings", c.source.location, "--truncate-to", "512"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCorpusTruncation(t)
			q := newQueryVectorsTruncation(t)
			// A run that reads either source before refusing fails on its hash instead.
			corruptByte(t, c.source.resourcePath)
			corruptByte(t, q.source.resourcePath)
			args := tt.args(t, c, q)
			corpusBefore, queriesBefore := tree(t, c.root), tree(t, q.root)

			err := execDatapipe(t, "", append([]string{"embed"}, args...)...)

			require.Error(t, err)
			var mismatch *datapackage.HashMismatchError
			assert.False(t, errors.As(err, &mismatch), "refused only after reading the source: %v", err)
			assert.Equal(t, corpusBefore, tree(t, c.root))
			assert.Equal(t, queriesBefore, tree(t, q.root))
		})
	}
}

func TestEmbedTruncate_RefusesASourceWhoseBytesChanged(t *testing.T) {
	c := newCorpusTruncation(t)
	corruptByte(t, c.source.resourcePath)

	err := execDatapipe(t, "", "embed", "--embeddings", c.source.location, "--truncate-to", "512")

	var mismatch *datapackage.HashMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Empty(t, tree(t, c.tagDir()))
}

func TestEmbedTruncate_AFailedRunPublishesNothing(t *testing.T) {
	t.Run("corpus", func(t *testing.T) {
		c := newCorpusTruncation(t)
		rows, _ := denseCorpus(c.articles)
		rows[3].Embedding = sourceVector()
		c.source = writeEmbeddingRows(t, c.root, c.canonical, rows, nil)

		err := execDatapipe(t, "", "embed", "--embeddings", c.source.location, "--truncate-to", "512")

		require.Error(t, err)
		assert.Empty(t, tree(t, c.tagDir()))
	})
	t.Run("query vectors", func(t *testing.T) {
		q := newQueryVectorsTruncation(t)
		rows := []queryvectors.Row{q.rows[0], {QueryID: "q2", Text: q.rows[1].Text, Embedding: sourceVector()}}
		q.source = writeQueryVectorsSource(t, filepath.Dir(q.source.location), rows)

		err := execDatapipe(t, "", "embed", "--embeddings", q.source.location, "--truncate-to", "512", "--out", q.out)

		require.Error(t, err)
		assert.Empty(t, tree(t, filepath.Dir(q.out)))
	})
}
