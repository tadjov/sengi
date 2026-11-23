package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/parquet-go/parquet-go"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/ingest"
	"github.com/tadjov/sengi/internal/progress/progresstest"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
	"github.com/tadjov/sengi/pkg/apis/preprocess"
	"github.com/tadjov/sengi/pkg/datapackage"
)

const baseMapping = `kind: Mapping
version: v1
name: test-news
constants:
  Language: eng
fields:
  - {source: title, target: Title, required: true}
  - {source: text, target: Content}
  - {source: description, target: Description}
  - {source: url, type: url, target: URL, required: true}
  - {source: date, type: datetime, format: "2006-01-02 15:04:05", target: PublishedAt}
  - {source: authors, target: Metadata.Authors, separator: ";"}
  - {source: words, type: integer, target: Metadata.Extra.wordCount}
`

// urlKeySpec keeps the first record of each url and nothing else.
const urlKeySpec = `kind: Preprocess
version: v1
name: test-news
input: raw
mapping: mapping.yaml
output:
  path: canonical
id:
  kind: urlHash
unique:
  keys:
    - {name: same-url, fields: [URL]}
`

func specWith(t *testing.T, spec, old, replacement string) string {
	t.Helper()
	require.Contains(t, spec, old)
	return strings.Replace(spec, old, replacement, 1)
}

func withKeys(t *testing.T, keys string) string {
	t.Helper()
	return specWith(t, urlKeySpec, "    - {name: same-url, fields: [URL]}\n", keys)
}

const samePageKeys = "    - {name: same-url, fields: [URL]}\n    - {name: same-page, fields: [PublisherDomain, Title, Content]}\n"

// rawShardRow is a raw shard as the Hugging Face mirror lands it.
type rawShardRow struct {
	Title       string `parquet:"title"`
	Text        string `parquet:"text"`
	Description string `parquet:"description,optional"`
	URL         string `parquet:"url"`
	Date        string `parquet:"date,optional"`
	Authors     string `parquet:"authors,optional"`
	Words       string `parquet:"words,optional"`
}

type rawFile struct {
	name    string
	format  string
	content []byte
}

func csvFile(name, content string) rawFile {
	return rawFile{name: name, format: "csv", content: []byte(content)}
}

func jsonlFile(name, content string) rawFile {
	return rawFile{name: name, format: "jsonl", content: []byte(content)}
}

func fixtureFile(t *testing.T, name string) rawFile {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "preprocess", name))
	require.NoError(t, err)
	return rawFile{name: name, format: "csv", content: content}
}

// Two rows per row group expose a reader that walks row groups out of order.
func parquetFile(t *testing.T, name string, rows ...rawShardRow) rawFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	require.NoError(t, err)
	w := parquet.NewGenericWriter[rawShardRow](f, parquet.MaxRowsPerRowGroup(2))
	_, err = w.Write(rows)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return rawFile{name: name, format: "parquet", content: content}
}

const hubSource = "https://huggingface.co/datasets/example/news"

const rawVersion = "0123abcd"

// writeRawPackage omits an empty version, as older fetches did.
func writeRawPackage(t *testing.T, dir, version string, files ...rawFile) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	pkg := datapackage.Package{
		Name:    "test-news-raw",
		Version: version,
		Sources: []datapackage.Source{
			{Title: "Hugging Face example/news", Path: hubSource, Version: "0123abcd"},
			{Title: "fetch notes", Path: "notes.txt"},
		},
	}
	for _, f := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f.name), f.content, 0o644))
		pkg.Resources = append(pkg.Resources, datapackage.Resource{Name: f.name, Path: f.name, Format: f.format})
	}
	_, err := datapackage.Write(context.Background(), dir, pkg)
	require.NoError(t, err)
}

// preprocessRun is a dataset folder: the raw package, the mapping and the spec side by side.
type preprocessRun struct {
	dir         string
	rawDir      string
	mappingPath string
	specPath    string
	outputRoot  string
}

func newPreprocessRun(t *testing.T, spec, mapping string, files ...rawFile) preprocessRun {
	t.Helper()
	dir := t.TempDir()
	run := preprocessRun{
		dir:         dir,
		rawDir:      filepath.Join(dir, "raw"),
		mappingPath: filepath.Join(dir, "mapping.yaml"),
		specPath:    filepath.Join(dir, "preprocess.yaml"),
		outputRoot:  filepath.Join(dir, "canonical"),
	}
	require.NoError(t, os.WriteFile(run.mappingPath, []byte(mapping), 0o644))
	require.NoError(t, os.WriteFile(run.specPath, []byte(spec), 0o644))
	writeRawPackage(t, run.rawDir, rawVersion, files...)
	return run
}

// writingTo is the same run with a copy of its spec that publishes into another folder.
func (r preprocessRun) writingTo(t *testing.T, folder string) preprocessRun {
	t.Helper()
	spec, err := os.ReadFile(r.specPath)
	require.NoError(t, err)
	r.specPath = filepath.Join(r.dir, folder+".yaml")
	r.outputRoot = filepath.Join(r.dir, folder)
	require.NoError(t, os.WriteFile(r.specPath, []byte(specWith(t, string(spec), "  path: canonical\n", "  path: "+folder+"\n")), 0o644))
	return r
}

func (r preprocessRun) rewriteSpec(t *testing.T, old, replacement string) {
	t.Helper()
	spec, err := os.ReadFile(r.specPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(r.specPath, []byte(specWith(t, string(spec), old, replacement)), 0o644))
}

func (r preprocessRun) execute(ctx context.Context, flags ...string) error {
	cmd := newPreprocessCmd()
	cmd.SetArgs(append([]string{r.specPath}, flags...))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.ExecuteContext(ctx)
}

func (r preprocessRun) packageDir(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(r.outputRoot)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the output root holds exactly the package folder")
	return filepath.Join(r.outputRoot, entries[0].Name())
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// canonicalParquetRow reads the contract's columns independently of the writer; nil is NULL.
type canonicalParquetRow struct {
	ID              string  `parquet:"id"`
	URL             string  `parquet:"url"`
	Title           string  `parquet:"title"`
	Description     *string `parquet:"description,optional"`
	Content         *string `parquet:"content,optional"`
	PublishedAt     *string `parquet:"publishedAt,optional"`
	Language        string  `parquet:"language"`
	PublisherDomain *string `parquet:"publisherDomain,optional"`
	Metadata        *string `parquet:"metadata,optional"`
}

type canonicalRows struct {
	urls     []string
	articles []document.Article
	byURL    map[string]document.Article
	parquet  []canonicalParquetRow
}

func readParquet[T any](t *testing.T, r io.ReaderAt) []T {
	t.Helper()
	reader := parquet.NewGenericReader[T](r)
	defer reader.Close()
	rows := make([]T, reader.NumRows())
	n, err := reader.Read(rows)
	if !errors.Is(err, io.EOF) {
		require.NoError(t, err)
	}
	return rows[:n]
}

// readArticles opens the package as a loader would, verifying the resource hashes.
func readArticles(t *testing.T, dir string) (datapackage.Package, canonicalRows) {
	t.Helper()
	ctx := context.Background()
	opened, err := datapackage.Open(ctx, dir)
	require.NoError(t, err)
	file, err := opened.Resource(ctx, datapackage.ArticlesResource)
	require.NoError(t, err)
	defer file.Close()

	rows := canonicalRows{byURL: map[string]document.Article{}}
	add := func(a document.Article) {
		rows.urls = append(rows.urls, a.URL)
		rows.articles = append(rows.articles, a)
		rows.byURL[a.URL] = a
	}
	switch format := resourceNamed(t, opened.Package, datapackage.ArticlesResource).Format; format {
	case "jsonl":
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			var a document.Article
			require.NoError(t, json.Unmarshal(scanner.Bytes(), &a))
			add(a)
		}
		require.NoError(t, scanner.Err())
	case "parquet":
		rows.parquet = readParquet[canonicalParquetRow](t, file)
		for _, row := range rows.parquet {
			add(articleFromParquet(t, row))
		}
	default:
		t.Fatalf("unexpected format %q", format)
	}
	return opened.Package, rows
}

func resourceNamed(t *testing.T, pkg datapackage.Package, name string) datapackage.Resource {
	t.Helper()
	for _, r := range pkg.Resources {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("package %s lists no %s resource", pkg.Name, name)
	return datapackage.Resource{}
}

func articleFromParquet(t *testing.T, row canonicalParquetRow) document.Article {
	t.Helper()
	text := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	doc := map[string]any{
		"id": row.ID, "url": row.URL, "title": row.Title, "description": text(row.Description),
		"content": text(row.Content), "language": row.Language, "publisherDomain": text(row.PublisherDomain),
	}
	if row.Metadata != nil {
		doc["metadata"] = json.RawMessage(*row.Metadata)
	}
	encoded, err := json.Marshal(doc)
	require.NoError(t, err)
	var a document.Article
	require.NoError(t, json.Unmarshal(encoded, &a))
	if row.PublishedAt != nil {
		a.PublishedAt, err = time.Parse(time.RFC3339, *row.PublishedAt)
		require.NoError(t, err)
	}
	return a
}

// rejectParquetRow and changeParquetRow read the audit resources by their contract column names.
type rejectParquetRow struct {
	Resource      string  `parquet:"resource"`
	Row           int64   `parquet:"row"`
	URL           *string `parquet:"url,optional"`
	Stage         string  `parquet:"stage"`
	Rule          *string `parquet:"rule,optional"`
	Field         *string `parquet:"field,optional"`
	Reason        *string `parquet:"reason,optional"`
	ID            *string `parquet:"id,optional"`
	KeptID        *string `parquet:"keptId,optional"`
	CountDistinct *int64  `parquet:"countDistinct,optional"`
}

type changeParquetRow struct {
	ID            string `parquet:"id"`
	Field         string `parquet:"field"`
	Rule          string `parquet:"rule"`
	CountDistinct int64  `parquet:"countDistinct"`
	Value         string `parquet:"value"`
}

func readResource[T any](t *testing.T, dir, name string) []T {
	t.Helper()
	opened, err := datapackage.Open(t.Context(), dir)
	require.NoError(t, err)
	file, err := opened.Resource(t.Context(), name)
	require.NoError(t, err)
	defer file.Close()
	return readParquet[T](t, file)
}

func ptr[T any](v T) *T { return &v }

func TestPreprocess_WritesACanonicalPackageInSourceOrder(t *testing.T) {
	run := newPreprocessRun(t, urlKeySpec, baseMapping,
		parquetFile(t, "train-0.parquet",
			rawShardRow{Title: "Storm hits coast", Text: "Body one", Description: "Lead one", URL: "https://example.com/a/1", Date: "2019-03-04 05:06:07", Authors: "Jane Doe; John Roe", Words: "412"},
			rawShardRow{Title: "Markets fall", Text: "Body two", URL: "https://example.com/a/2"},
			rawShardRow{Title: "Rain returns", Text: "Body three", URL: "https://example.com/a/3"},
		),
		csvFile("train-1.csv", "title,text,url\nLate edition,Body four,https://example.com/b/4\nNight news,Body five,https://example.com/b/5\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	dir := run.packageDir(t)
	pkg, rows := readArticles(t, dir)
	assert.Equal(t, []string{
		"https://example.com/a/1", "https://example.com/a/2", "https://example.com/a/3",
		"https://example.com/b/4", "https://example.com/b/5",
	}, rows.urls)

	first := rows.byURL["https://example.com/a/1"]
	assert.Equal(t, "Storm hits coast", first.Title)
	assert.Equal(t, "Lead one", first.Description)
	assert.Equal(t, "Body one", first.Content)
	assert.Equal(t, time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC), first.PublishedAt.UTC())
	assert.Equal(t, language.English, first.Language)
	assert.Equal(t, []string{"Jane Doe", "John Roe"}, first.Metadata.Authors)
	assert.Equal(t, map[string]any{"wordCount": float64(412)}, first.Metadata.Extra)

	assert.Equal(t, "test-news-canonical", pkg.Name)
	assert.Equal(t, "article/v2", pkg.ArticleSchema)
	assert.Equal(t, string(language.English), pkg.Language)
	for _, want := range []datapackage.Resource{
		{Name: "articles", Path: "articles.parquet", Format: "parquet"},
		{Name: "rejects", Path: "rejects.parquet", Format: "parquet"},
		{Name: "changes", Path: "changes.parquet", Format: "parquet"},
	} {
		got := resourceNamed(t, pkg, want.Name)
		assert.Equal(t, want.Path, got.Path)
		assert.Equal(t, want.Format, got.Format)
		assert.Equal(t, sha256Of(t, filepath.Join(dir, want.Path)), got.Hash)
	}
	assert.Equal(t, datapackage.Counts{Read: 5, Written: 5, Duplicates: map[string]int64{"same-url": 0}}, pkg.Preprocess.Counts)
	assert.Equal(t, []datapackage.Source{{Title: "Hugging Face example/news", Path: hubSource, Version: "0123abcd"}}, pkg.Sources)
	assert.Equal(t, "datapipe preprocess", pkg.ProducedBy.Tool)
}

func readGoldenArticles(t *testing.T, name string) []document.Article {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "preprocess", name))
	require.NoError(t, err)
	defer f.Close()
	var articles []document.Article
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var a document.Article
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &a))
		articles = append(articles, a)
	}
	require.NoError(t, scanner.Err())
	return articles
}

// origin-main-articles.jsonl is what datapipe preprocess on origin/main f0072c9 wrote for
// records.csv with the unique rule same-page, before the spec split.
func TestPreprocess_KeepsTheArticlesAndIDsTheMappingIdentityKept(t *testing.T) {
	run := newPreprocessRun(t, withKeys(t, samePageKeys), baseMapping, fixtureFile(t, "records.csv"))

	require.NoError(t, run.execute(t.Context()))

	_, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, readGoldenArticles(t, "origin-main-articles.jsonl"), rows.articles)
}

// cleaningSpec is the fixture's spec with rules at thresholds its small groups reach.
func cleaningSpec(t *testing.T, rules string) string {
	t.Helper()
	return withKeys(t, samePageKeys) + "rules:\n" + rules
}

const clearSharedText = `  - {name: shared-body, clear: Content, when: {countDistinct: Title, groupBy: [PublisherDomain, Content], atLeast: 3}}
  - {name: shared-description, clear: Description, when: {countDistinct: Title, groupBy: [PublisherDomain, Description], atLeast: 3}}
`

func TestPreprocess_RecordsEveryRemovedRecordWithTheStageThatRemovedIt(t *testing.T) {
	run := newPreprocessRun(t, cleaningSpec(t, clearSharedText), baseMapping, fixtureFile(t, "records.csv"))

	require.NoError(t, run.execute(t.Context()))

	dir := run.packageDir(t)
	_, rows := readArticles(t, dir)
	budget := rows.byURL["https://example.com/budget"].ID.String()
	assert.Equal(t, []rejectParquetRow{
		{Resource: "records.csv", Row: 1, Stage: "duplicates", URL: ptr("https://example.com/budget"), Rule: ptr("same-url"), KeptID: &budget},
		{Resource: "records.csv", Row: 2, Stage: "duplicates", URL: ptr("https://example.com/budget-copy"), Rule: ptr("same-page"), KeptID: &budget},
		{Resource: "records.csv", Row: 3, Stage: "duplicates", URL: ptr("https://example.com/budget-copy"), Rule: ptr("same-url"), KeptID: &budget},
		{Resource: "records.csv", Row: 4, Stage: "duplicates", URL: ptr("https://example.com/budget"), Rule: ptr("same-url"), KeptID: &budget},
		{Resource: "records.csv", Row: 6, Stage: "required", URL: ptr("https://example.com/untitled"), Field: ptr("Title"), Reason: ptr("missing")},
		{Resource: "records.csv", Row: 7, Stage: "required", URL: ptr("not a url"), Field: ptr("URL"), Reason: ptr("invalid")},
	}, readResource[rejectParquetRow](t, dir, datapackage.RejectsResource))
}

func TestPreprocess_ClearsTheSharedFieldOfEveryArticleInAGroupAtTheThreshold(t *testing.T) {
	run := newPreprocessRun(t, cleaningSpec(t, clearSharedText), baseMapping, fixtureFile(t, "records.csv"))

	require.NoError(t, run.execute(t.Context()))

	dir := run.packageDir(t)
	pkg, rows := readArticles(t, dir)
	id := func(url string) string { return rows.byURL[url].ID.String() }
	assert.Equal(t, []changeParquetRow{
		{ID: id("https://junk.net/storm"), Field: "Content", Rule: "shared-body", CountDistinct: 4, Value: "Subscribe to read the full story."},
		{ID: id("https://junk.net/markets"), Field: "Content", Rule: "shared-body", CountDistinct: 4, Value: "Subscribe to read the full story."},
		{ID: id("https://junk.net/election"), Field: "Content", Rule: "shared-body", CountDistinct: 4, Value: "Subscribe to read the full story."},
		{ID: id("https://junk.net/final"), Field: "Content", Rule: "shared-body", CountDistinct: 4, Value: "Subscribe  to read the full story."},
		{ID: id("https://slogan.com/rain"), Field: "Description", Rule: "shared-description", CountDistinct: 3, Value: "News from around the web."},
		{ID: id("https://slogan.com/heat"), Field: "Description", Rule: "shared-description", CountDistinct: 3, Value: "News from around the web."},
		{ID: id("https://slogan.com/snow"), Field: "Description", Rule: "shared-description", CountDistinct: 3, Value: "News from around the web."},
	}, readResource[changeParquetRow](t, dir, datapackage.ChangesResource))

	golden := map[string]document.Article{}
	for _, a := range readGoldenArticles(t, "origin-main-articles.jsonl") {
		golden[a.URL] = a
	}
	require.Equal(t, len(golden), len(rows.articles))
	for _, got := range rows.articles {
		want := golden[got.URL]
		switch got.URL {
		case "https://junk.net/storm", "https://junk.net/markets", "https://junk.net/election", "https://junk.net/final":
			want.Content = ""
		case "https://slogan.com/rain", "https://slogan.com/heat", "https://slogan.com/snow":
			want.Description = ""
		}
		assert.Equal(t, want, got, got.URL)
	}
	assert.Equal(t, "Shared by two headlines.", rows.byURL["https://junk.net/bridge"].Content, "a group under the threshold is untouched")
	assert.Equal(t, datapackage.Counts{
		Read:       20,
		Dropped:    map[string]int64{"required/Title": 1, "required/URL": 1},
		Duplicates: map[string]int64{"same-url": 3, "same-page": 1},
		Cleared:    map[string]int64{"shared-body": 4, "shared-description": 3},
		Written:    14,
	}, pkg.Preprocess.Counts)
}

func TestPreprocess_DescribesEachRulesDistribution(t *testing.T) {
	run := newPreprocessRun(t, cleaningSpec(t, clearSharedText), baseMapping, fixtureFile(t, "records.csv"))

	require.NoError(t, run.execute(t.Context()))

	pkg, _ := readArticles(t, run.packageDir(t))
	body := pkg.Preprocess.Rules["shared-body"]
	assert.Equal(t, "Title", body.CountDistinct)
	assert.Equal(t, []string{"PublisherDomain", "Content"}, body.GroupBy)
	// 13 kept articles have a body: four junk.net headlines share one, two bridge headlines another, seven stand alone.
	assert.Equal(t, int64(13), body.Articles)
	assert.Equal(t, int64(1), body.Median)
	assert.Equal(t, int64(4), body.Max)
	assert.Equal(t, int64(6), body.AtLeast["2"])
	assert.Equal(t, int64(4), body.AtLeast["3"])
	require.Len(t, body.Top, 9)
	assert.Equal(t, datapackage.Group{Values: []string{"junk.net", "Subscribe to read the full story."}, CountDistinct: 4, Articles: 4}, body.Top[0])
	assert.Equal(t, datapackage.Group{Values: []string{"junk.net", "Shared by two headlines."}, CountDistinct: 2, Articles: 2}, body.Top[1])
	assert.Equal(t, []string{"slogan.com", "News from around the web."}, pkg.Preprocess.Rules["shared-description"].Top[0].Values)
}

func TestPreprocess_RulesCountingTheSameGroupsShareTheirCount(t *testing.T) {
	rules := clearSharedText + "  - {name: boilerplate-body, drop: true, when: {countDistinct: Title, groupBy: [PublisherDomain, Content], atLeast: 4}}\n"
	run := newPreprocessRun(t, cleaningSpec(t, rules), baseMapping, fixtureFile(t, "records.csv"))

	require.NoError(t, run.execute(t.Context()))

	pkg, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, pkg.Preprocess.Rules["shared-body"], pkg.Preprocess.Rules["boilerplate-body"])
	assert.NotContains(t, rows.urls, "https://junk.net/storm", "the drop wins over the clear of the same article")
	assert.Equal(t, int64(0), pkg.Preprocess.Counts.Cleared["shared-body"])
	assert.Equal(t, int64(4), pkg.Preprocess.Counts.Dropped["rule/boilerplate-body"])
}

func TestPreprocess_DropsTheArticlesADropRuleFiresOn(t *testing.T) {
	rules := "  - {name: boilerplate-body, drop: true, when: {countDistinct: Title, groupBy: [PublisherDomain, Content], atLeast: 3}}\n"
	run := newPreprocessRun(t, cleaningSpec(t, rules), baseMapping, fixtureFile(t, "records.csv"))

	require.NoError(t, run.execute(t.Context()))

	dir := run.packageDir(t)
	pkg, rows := readArticles(t, dir)
	assert.NotContains(t, rows.urls, "https://junk.net/storm")
	assert.Contains(t, rows.urls, "https://junk.net/bridge")
	assert.Equal(t, int64(10), pkg.Preprocess.Counts.Written)
	assert.Equal(t, int64(4), pkg.Preprocess.Counts.Dropped["rule/boilerplate-body"])

	var dropped []rejectParquetRow
	for _, r := range readResource[rejectParquetRow](t, dir, datapackage.RejectsResource) {
		if r.Stage == "rule" {
			dropped = append(dropped, r)
		}
	}
	storm := document.DeriveArticleID("https://junk.net/storm").String()
	require.Len(t, dropped, 4)
	assert.Equal(t, rejectParquetRow{
		Resource: "records.csv", Row: 10, Stage: "rule", URL: ptr("https://junk.net/storm"),
		Rule: ptr("boilerplate-body"), ID: &storm, CountDistinct: ptr(int64(4)),
	}, dropped[0])
}

func TestPreprocess_WritesEveryMissingOptionalValueAsNull(t *testing.T) {
	mapping := strings.Replace(baseMapping, "name: test-news\n", "name: test-news\nmissingValues: ['', 'N/A']\n", 1)
	run := newPreprocessRun(t, cleaningSpec(t, clearSharedText), mapping,
		csvFile("part.csv", "title,text,description,url,date\n"+
			"Plain,Body,N/A,https://example.com/1,\n"+
			"Spaces,  ,  ,https://example.com/2,N/A\n"),
		fixtureFile(t, "records.csv"),
	)

	require.NoError(t, run.execute(t.Context()))

	_, rows := readArticles(t, run.packageDir(t))
	for _, row := range rows.parquet {
		for column, value := range map[string]*string{
			"description": row.Description, "content": row.Content, "publishedAt": row.PublishedAt,
			"publisherDomain": row.PublisherDomain, "metadata": row.Metadata,
		} {
			if value != nil {
				assert.NotEmpty(t, strings.TrimSpace(*value), "%s of %s is an empty string, not NULL", column, row.URL)
			}
		}
	}
	plain := rows.parquet[0]
	assert.Nil(t, plain.Description, "N/A is a missing value")
	assert.Nil(t, plain.Metadata, "an article without metadata has NULL metadata")
	assert.Nil(t, rows.parquet[1].Content)
	assert.Nil(t, rows.parquet[1].PublishedAt)
}

// parquet-go v0.30 and v0.32 write some runs of zero strings in an optional string column as
// empty strings rather than NULL; this run of descriptions is one of them.
func TestPreprocess_WritesARunOfMissingValuesAsNull(t *testing.T) {
	descriptions := slices.Repeat([]string{strings.Repeat("d", 150)}, 124)
	descriptions = append(descriptions, "", "", "", "", "", strings.Repeat("e", 102))
	descriptions = append(descriptions, slices.Repeat([]string{""}, 62)...)
	var csv strings.Builder
	csv.WriteString("title,text,description,url\n")
	for i, description := range descriptions {
		fmt.Fprintf(&csv, "Title %d,Body %d,%s,https://example.com/%d\n", i, i, description, i)
	}
	run := newPreprocessRun(t, urlKeySpec, baseMapping, csvFile("part.csv", csv.String()))

	require.NoError(t, run.execute(t.Context()))

	_, rows := readArticles(t, run.packageDir(t))
	for _, row := range rows.parquet {
		if row.Description != nil {
			assert.NotEmpty(t, *row.Description, "the description of %s is an empty string, not NULL", row.URL)
		}
	}
}

func TestPreprocess_ChecksEachBudgetAsACountOrAShareOfItsBase(t *testing.T) {
	// The fixture drops 2 of the 20 records read, finds 4 duplicates among them and clears 7 of the 14 articles written.
	tests := []struct {
		check      string
		warn, fail string
		want       datapackage.CheckStatus
	}{
		{check: "droppedOfRead", warn: "20%", fail: "30%", want: datapackage.CheckPass},
		{check: "droppedOfRead", warn: "10%", fail: "10%", want: datapackage.CheckPass},
		{check: "droppedOfRead", warn: "5%", fail: "30%", want: datapackage.CheckWarn},
		{check: "droppedOfRead", warn: "5%", fail: "8%", want: datapackage.CheckError},
		{check: "droppedOfRead", warn: "1", fail: "3", want: datapackage.CheckWarn},
		{check: "duplicatesOfRead", warn: "4", fail: "40%", want: datapackage.CheckPass},
		{check: "duplicatesOfRead", warn: "10%", fail: "40%", want: datapackage.CheckWarn},
		{check: "duplicatesOfRead", warn: "10%", fail: "3", want: datapackage.CheckError},
		{check: "clearedOfWritten", warn: "60%", fail: "70%", want: datapackage.CheckPass},
		{check: "clearedOfWritten", warn: "6", fail: "7", want: datapackage.CheckWarn},
		{check: "clearedOfWritten", warn: "40%", fail: "45%", want: datapackage.CheckError},
	}
	observed := map[string]struct {
		count int64
		share float64
	}{"droppedOfRead": {2, 0.1}, "duplicatesOfRead": {4, 0.2}, "clearedOfWritten": {7, 0.5}}
	for _, tt := range tests {
		t.Run(tt.check+" warn "+tt.warn+" error "+tt.fail, func(t *testing.T) {
			spec := cleaningSpec(t, clearSharedText) + "checks:\n  " + tt.check + ": {warn: " + tt.warn + ", error: " + tt.fail + "}\n"
			run := newPreprocessRun(t, spec, baseMapping, fixtureFile(t, "records.csv"))

			err := run.execute(t.Context())

			if tt.want == datapackage.CheckError {
				var failed *ingest.ChecksFailedError
				require.ErrorAs(t, err, &failed)
				assert.Equal(t, datapackage.CheckError, failed.Preprocess.Checks[tt.check].Result)
				entries, readErr := os.ReadDir(run.outputRoot)
				require.NoError(t, readErr)
				assert.Empty(t, entries, "nothing is published past an error check")
				return
			}
			require.NoError(t, err)
			pkg, _ := readArticles(t, run.packageDir(t))
			check := pkg.Preprocess.Checks[tt.check]
			assert.Equal(t, tt.want, check.Result)
			assert.Equal(t, observed[tt.check].count, check.Count)
			assert.InDelta(t, observed[tt.check].share, check.Share, 1e-9)
			assert.Equal(t, tt.warn, check.Warn.String())
			assert.Equal(t, tt.fail, check.Error.String())
		})
	}
}

func TestPreprocess_CountsAValueOncePerDistinctValueInItsGroup(t *testing.T) {
	spec := urlKeySpec + `rules:
  - {name: shared-body, clear: Content, when: {countDistinct: Title, groupBy: [PublisherDomain, Content], atLeast: 2}}
`
	run := newPreprocessRun(t, spec, baseMapping, csvFile("part.csv", "title,text,url\n"+
		"Same headline,Shared body,https://example.com/1\n"+
		"Same headline,Shared body,https://example.com/2\n"+
		"Lone body,,https://example.com/3\n"+
		"Lone body again,,https://example.com/4\n"+
		"Other site,Shared body,https://other.org/1\n"))

	require.NoError(t, run.execute(t.Context()))

	pkg, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, "Shared body", rows.byURL["https://example.com/1"].Content, "two copies of one headline are one distinct title")
	assert.Equal(t, "Shared body", rows.byURL["https://other.org/1"].Content)
	stats := pkg.Preprocess.Rules["shared-body"]
	assert.Equal(t, int64(3), stats.Articles, "an empty body puts an article in no group")
	assert.Equal(t, int64(1), stats.Max)
}

func TestPreprocess_GroupsByTheListedFieldsOnly(t *testing.T) {
	spec := urlKeySpec + `rules:
  - {name: syndicated-body, clear: Content, when: {countDistinct: PublisherDomain, groupBy: [Content], atLeast: 2}}
`
	run := newPreprocessRun(t, spec, baseMapping, csvFile("part.csv", "title,text,url\n"+
		"One,Wire body,https://example.com/1\n"+
		"Two,Wire body,https://other.org/2\n"+
		"Three,Own body,https://example.com/3\n"))

	require.NoError(t, run.execute(t.Context()))

	_, rows := readArticles(t, run.packageDir(t))
	assert.Empty(t, rows.byURL["https://example.com/1"].Content)
	assert.Empty(t, rows.byURL["https://other.org/2"].Content)
	assert.Equal(t, "Own body", rows.byURL["https://example.com/3"].Content)
}

func TestPreprocess_PublishesUnderTheNamedVersion(t *testing.T) {
	tests := []struct {
		name        string
		flags       []string
		wantVersion string
	}{
		{name: "default", wantVersion: "v1"},
		{name: "named", flags: []string{"--version", "v2"}, wantVersion: "v2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := newPreprocessRun(t, urlKeySpec, baseMapping, csvFile("part.csv", "title,text,url\nOne,Body,https://example.com/1\n"))

			require.NoError(t, run.execute(t.Context(), tt.flags...))

			dir := run.packageDir(t)
			pkg, _ := readArticles(t, dir)
			assert.Equal(t, tt.wantVersion, filepath.Base(dir))
			assert.Equal(t, tt.wantVersion, pkg.Version)
			assert.Equal(t, &datapackage.RawRef{Name: "test-news-raw", Version: rawVersion}, pkg.Raw)
		})
	}
}

func freeze(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NoError(t, os.Chmod(filepath.Join(dir, e.Name()), 0o444))
	}
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o755)
		for _, e := range entries {
			_ = os.Chmod(filepath.Join(dir, e.Name()), 0o644)
		}
	})
}

func TestPreprocess_RefusesAChangedSpecRerunIntoTheSameVersion(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, run preprocessRun)
		want   string
	}{
		{
			name: "a budget that leaves the articles alone",
			change: func(t *testing.T, run preprocessRun) {
				run.rewriteSpec(t, "checks:\n", "checks:\n  duplicatesOfRead: {warn: 90%}\n")
			},
			want: "preprocess spec",
		},
		{
			name: "a mapping that moves a field",
			change: func(t *testing.T, run preprocessRun) {
				require.NoError(t, os.WriteFile(run.mappingPath, []byte(strings.Replace(baseMapping, "target: Content}", "target: Metadata.Extra.body}", 1)), 0o644))
			},
			want: "preprocess mapping",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := newPreprocessRun(t, urlKeySpec+"checks:\n", baseMapping, csvFile("part.csv", "title,text,url\nOne,Body,https://example.com/1\n"))
			require.NoError(t, run.execute(t.Context()))
			dir := run.packageDir(t)
			descriptor, err := os.ReadFile(filepath.Join(dir, datapackage.DescriptorName))
			require.NoError(t, err)
			freeze(t, dir)
			tt.change(t, run)

			err = run.execute(t.Context())

			var conflict *datapackage.VersionConflictError
			require.ErrorAs(t, err, &conflict)
			assert.Equal(t, "v1", conflict.Version)
			assert.Contains(t, strings.Join(conflict.Differences, "; "), tt.want)
			after, err := os.ReadFile(filepath.Join(dir, datapackage.DescriptorName))
			require.NoError(t, err)
			assert.Equal(t, string(descriptor), string(after))
		})
	}
}

func TestPreprocess_RerunOverTheSameSpecsAndInputKeepsTheExistingPackage(t *testing.T) {
	run := newPreprocessRun(t, cleaningSpec(t, clearSharedText), baseMapping, fixtureFile(t, "records.csv"))
	require.NoError(t, run.execute(t.Context()))
	dir := run.packageDir(t)
	resourcePath := filepath.Join(dir, "articles.parquet")
	before, err := os.Stat(resourcePath)
	require.NoError(t, err)
	freeze(t, dir)

	require.NoError(t, run.execute(t.Context()))

	assert.Equal(t, dir, run.packageDir(t))
	after, err := os.Stat(resourcePath)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "the resource file was replaced")
}

func TestPreprocess_RestoresTheResourcesOfAPackageCheckedOutWithoutThem(t *testing.T) {
	run := newPreprocessRun(t, cleaningSpec(t, clearSharedText), baseMapping, fixtureFile(t, "records.csv"))
	require.NoError(t, run.execute(t.Context()))
	dir := run.packageDir(t)
	hashes := map[string]string{}
	for _, name := range []string{"articles.parquet", "rejects.parquet", "changes.parquet"} {
		hashes[name] = sha256Of(t, filepath.Join(dir, name))
		require.NoError(t, os.Remove(filepath.Join(dir, name)))
	}
	descriptor, err := os.ReadFile(filepath.Join(dir, "datapackage.json"))
	require.NoError(t, err)

	require.NoError(t, run.execute(t.Context()))

	for name, hash := range hashes {
		assert.Equal(t, hash, sha256Of(t, filepath.Join(dir, name)), name)
	}
	after, err := os.ReadFile(filepath.Join(dir, "datapackage.json"))
	require.NoError(t, err)
	assert.Equal(t, string(descriptor), string(after))
}

func TestPreprocess_TwoRunsOverOneRawPackageWriteIdenticalBytes(t *testing.T) {
	run := newPreprocessRun(t, cleaningSpec(t, clearSharedText), baseMapping, fixtureFile(t, "records.csv"),
		parquetFile(t, "train-0.parquet",
			rawShardRow{Title: "One", Text: "Body", URL: "https://example.com/1", Date: "2019-03-04 05:06:07", Authors: "A; B", Words: "3"},
			rawShardRow{Title: "Two", Text: "Body", URL: "https://example.com/2"},
			rawShardRow{Title: "Three", Text: "Body", URL: "https://example.com/3"},
		),
	)
	second := run.writingTo(t, "again")

	require.NoError(t, run.execute(t.Context()))
	require.NoError(t, second.execute(t.Context()))

	first, other := run.packageDir(t), second.packageDir(t)
	for _, name := range []string{"articles.parquet", "rejects.parquet", "changes.parquet", "datapackage.json"} {
		assert.Equal(t, sha256Of(t, filepath.Join(first, name)), sha256Of(t, filepath.Join(other, name)), name)
	}
}

func TestPreprocess_LocksTheSpecsAndInputButNotWhereTheyLie(t *testing.T) {
	run := newPreprocessRun(t, cleaningSpec(t, clearSharedText), baseMapping, fixtureFile(t, "records.csv"))
	moved := filepath.Join(t.TempDir(), "moved.yaml")
	spec, err := os.ReadFile(run.specPath)
	require.NoError(t, err)
	absolute := strings.NewReplacer(
		"input: raw\n", "input: "+run.rawDir+"\n",
		"mapping: mapping.yaml\n", "mapping: "+run.mappingPath+"\n",
		"  path: canonical\n", "  path: "+filepath.Join(run.dir, "elsewhere")+"\n",
	).Replace(string(spec))
	require.NoError(t, os.WriteFile(moved, []byte("# the same spec with absolute paths\n"+absolute), 0o644))

	require.NoError(t, run.execute(t.Context()))
	require.NoError(t, preprocessRun{specPath: moved}.execute(t.Context()))

	pkg, _ := readArticles(t, run.packageDir(t))
	elsewhere, _ := readArticles(t, filepath.Join(run.dir, "elsewhere", "v1"))
	assert.Equal(t, sha256Of(t, filepath.Join(run.rawDir, datapackage.DescriptorName)), pkg.Preprocess.Lock.Input)
	assert.True(t, pkg.Preprocess.Lock.SameInputs(elsewhere.Preprocess.Lock), "%+v and %+v", pkg.Preprocess.Lock, elsewhere.Preprocess.Lock)
	assert.NotEmpty(t, pkg.Preprocess.Lock.Tool)
}

var descriptorSchemaPath = filepath.Join("..", "..", "api", "datapackage-spec", "datapackage-v1.json")

func validateAgainstDescriptorSchema(t *testing.T, descriptorPath string) error {
	t.Helper()
	decode := func(path string) any {
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		require.NoError(t, err)
		return doc
	}
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("datapackage-v1.json", decode(descriptorSchemaPath)))
	schema, err := compiler.Compile("datapackage-v1.json")
	require.NoError(t, err)
	return schema.Validate(decode(descriptorPath))
}

func TestPreprocess_WritesADescriptorTheCommittedSchemaAccepts(t *testing.T) {
	tests := []struct {
		name string
		spec string
	}{
		{name: "parquet", spec: urlKeySpec},
		{name: "jsonl", spec: specWith(t, urlKeySpec, "  path: canonical\n", "  path: canonical\n  format: jsonl\n")},
		{name: "random ids", spec: specWith(t, urlKeySpec, "kind: urlHash", "kind: random")},
		{name: "rules and checks", spec: cleaningSpec(t, clearSharedText) + "checks:\n  droppedOfRead: {warn: 5%}\n  clearedOfWritten: {error: 9}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := newPreprocessRun(t, tt.spec, baseMapping, fixtureFile(t, "records.csv"))

			require.NoError(t, run.execute(t.Context()))

			assert.NoError(t, validateAgainstDescriptorSchema(t, filepath.Join(run.packageDir(t), datapackage.DescriptorName)))
		})
	}
}

func TestPreprocess_CountsEveryDroppedRecordByStageAndField(t *testing.T) {
	run := newPreprocessRun(t, urlKeySpec, baseMapping,
		csvFile("part.csv", "title,text,url\n"+
			"Kept,Body,https://example.com/kept\n"+
			"   ,Blank title,https://example.com/blank-title\n"+
			"No url,Body,\n"+
			"Bad url,Body,not a url\n"+
			"Short row,Body\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	pkg, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, []string{"https://example.com/kept"}, rows.urls)
	assert.Equal(t, map[string]int64{"required/Title": 1, "required/URL": 2, "read/malformed": 1}, pkg.Preprocess.Counts.Dropped)
}

func TestPreprocess_WritesJSONLArticlesWhenTheSpecAsksForIt(t *testing.T) {
	run := newPreprocessRun(t, specWith(t, urlKeySpec, "  path: canonical\n", "  path: canonical\n  format: jsonl\n"), baseMapping,
		jsonlFile("part.jsonl", `{"title":"One","text":"Body","url":"https://example.com/1","authors":"A;B"}`+"\n"+
			`{"title":"Two","url":"https://example.com/2"}`+"\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	dir := run.packageDir(t)
	pkg, rows := readArticles(t, dir)
	articles := resourceNamed(t, pkg, datapackage.ArticlesResource)
	assert.Equal(t, "jsonl", articles.Format)
	assert.Equal(t, "articles.jsonl", articles.Path)
	assert.Equal(t, []string{"A", "B"}, rows.byURL["https://example.com/1"].Metadata.Authors)
	written, err := os.ReadFile(filepath.Join(dir, "articles.jsonl"))
	require.NoError(t, err)
	second := strings.Split(strings.TrimSpace(string(written)), "\n")[1]
	assert.NotContains(t, second, `"content"`, "a missing value is absent in JSONL")
	assert.NotContains(t, second, `"metadata"`)
}

func TestPreprocess_RefusesASpecItCannotRunAndPublishesNothing(t *testing.T) {
	tests := map[string]string{
		"an unknown key":                       specWith(t, urlKeySpec, "unique:\n", "uniq:\n"),
		"a url hash without a url key":         specWith(t, urlKeySpec, "    - {name: same-url, fields: [URL]}\n", "    - {name: same-page, fields: [PublisherDomain, Title, Content]}\n"),
		"a clear on a required field":          urlKeySpec + "rules:\n  - {name: r, clear: Title, when: {countDistinct: URL, groupBy: [Content], atLeast: 2}}\n",
		"a rule that neither clears nor drops": urlKeySpec + "rules:\n  - {name: r, when: {countDistinct: Title, groupBy: [Content], atLeast: 2}}\n",
		"a group by a field never mapped":      urlKeySpec + "rules:\n  - {name: r, drop: true, when: {countDistinct: Title, groupBy: [Metadata.Section], atLeast: 2}}\n",
		"the retired signals block":            urlKeySpec + "signals: []\n",
		"a mapping file that is not there":     specWith(t, urlKeySpec, "mapping: mapping.yaml", "mapping: missing.yaml"),
		"the retired mapping identity keys":    urlKeySpec + "identity:\n  idKind: urlHash\n",
	}
	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			run := newPreprocessRun(t, spec, baseMapping, csvFile("part.csv", "title,text,url\nOne,Body,https://example.com/1\n"))

			require.Error(t, run.execute(t.Context()))

			assert.NoDirExists(t, run.outputRoot)
		})
	}
}

func TestPreprocess_DerivesEachIDFromTheNormalisedURL(t *testing.T) {
	run := newPreprocessRun(t, urlKeySpec, baseMapping,
		csvFile("part.csv", "title,text,url\nSpaced,Body,  https://example.com/a b \n"),
	)

	require.NoError(t, run.execute(t.Context()))

	pkg, rows := readArticles(t, run.packageDir(t))
	require.Equal(t, []string{"https://example.com/a%20b"}, rows.urls)
	assert.Equal(t, datapackage.ArticleIDs{Kind: preprocess.IDKindURLHash, Algorithm: "uuidv5", Namespace: "697bf49f-ca14-4303-83eb-f9d05f7c7c42"}, pkg.Preprocess.ID)
	namespace := uuid.MustParse("697bf49f-ca14-4303-83eb-f9d05f7c7c42")
	assert.Equal(t, uuid.NewSHA1(namespace, []byte("https://example.com/a%20b")), rows.byURL["https://example.com/a%20b"].ID)
}

func TestPreprocess_KeepsTheFirstOfRecordsSharingAURL(t *testing.T) {
	run := newPreprocessRun(t, urlKeySpec, baseMapping,
		csvFile("a.csv", "title,text,url\nFirst,Body,https://example.com/story\nOther,Body,https://example.com/other\n"),
		csvFile("b.csv", "title,text,url\nRepeat,Body, https://Example.COM/story\nAgain,Body,https://example.com/other\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	pkg, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, []string{"https://example.com/story", "https://example.com/other"}, rows.urls)
	assert.Equal(t, "First", rows.byURL["https://example.com/story"].Title)
	assert.Equal(t, "Other", rows.byURL["https://example.com/other"].Title)
	assert.Equal(t, map[string]int64{"same-url": 2}, pkg.Preprocess.Counts.Duplicates)
}

func TestPreprocess_KeepsURLVariantsApart(t *testing.T) {
	run := newPreprocessRun(t, urlKeySpec, baseMapping,
		csvFile("part.csv", "title,text,url\nFirst,Body,https://example.com/story\nVariant,Body,http://www.example.com/story/?utm_source=feed#top\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	pkg, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, []string{"https://example.com/story", "http://www.example.com/story/?utm_source=feed#top"}, rows.urls)
	assert.Equal(t, int64(2), pkg.Preprocess.Counts.Written)
}

func TestPreprocess_KeepsTheFirstOfArticlesAKeyMatches(t *testing.T) {
	run := newPreprocessRun(t, withKeys(t, samePageKeys), baseMapping,
		csvFile("part.csv", "title,text,url\n"+
			"Storm hits coast,Wire body,https://example.com/a\n"+
			"Storm hits coast,Wire body,https://example.com/b\n"+
			"Storm hits coast,Wire body,https://other.org/a\n"+
			"Storm hits coast,Other body,https://example.com/c\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	pkg, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, []string{"https://example.com/a", "https://other.org/a", "https://example.com/c"}, rows.urls)
	assert.Equal(t, map[string]int64{"same-url": 0, "same-page": 1}, pkg.Preprocess.Counts.Duplicates)
}

func TestPreprocess_ComparesKeyFieldsWithWhitespaceCollapsed(t *testing.T) {
	run := newPreprocessRun(t, withKeys(t, samePageKeys), baseMapping,
		csvFile("part.csv", "title,text,url\n"+
			"Storm hits coast,Wire body,https://example.com/a\n"+
			"\"  Storm  hits\ncoast \",\"Wire\t body\",https://example.com/b\n"+
			"Storm hits coast,Wire body,https://example.com/c\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	pkg, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, []string{"https://example.com/a"}, rows.urls)
	assert.Equal(t, int64(2), pkg.Preprocess.Counts.Duplicates["same-page"])
}

func TestPreprocess_ComparesEachKeyFieldOnItsOwn(t *testing.T) {
	keys := "    - {name: same-url, fields: [URL]}\n    - {name: same-text, fields: [Title, Content]}\n"
	run := newPreprocessRun(t, withKeys(t, keys), baseMapping,
		csvFile("part.csv", "title,text,url\n"+
			"Storm hits,coast now,https://example.com/1\n"+
			"Storm,hits coast now,https://example.com/2\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	_, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, []string{"https://example.com/1", "https://example.com/2"}, rows.urls)
}

func TestPreprocess_NeverMatchesAKeyOnAnEmptyField(t *testing.T) {
	run := newPreprocessRun(t, withKeys(t, samePageKeys), baseMapping,
		csvFile("part.csv", "title,text,url\nWeather,,https://example.com/1\nWeather,,https://example.com/2\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	_, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, []string{"https://example.com/1", "https://example.com/2"}, rows.urls)
}

func TestPreprocess_ADroppedRecordNeverKnocksOutALaterOne(t *testing.T) {
	run := newPreprocessRun(t, withKeys(t, samePageKeys), baseMapping,
		csvFile("part.csv", "title,text,url\n"+
			"Kept,Body X,https://example.com/story\n"+
			"Copy,Body Y,https://example.com/story\n"+
			"Copy,Body Y,https://example.com/other\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	pkg, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, []string{"https://example.com/story", "https://example.com/other"}, rows.urls)
	assert.Equal(t, map[string]int64{"same-url": 1, "same-page": 0}, pkg.Preprocess.Counts.Duplicates)
}

func TestPreprocess_ADuplicatesURLStaysWithTheArticleItRepeats(t *testing.T) {
	for name, keys := range map[string]string{
		"url key first": "    - {name: same-url, fields: [URL]}\n    - {name: same-body, fields: [Content]}\n",
		"url key last":  "    - {name: same-body, fields: [Content]}\n    - {name: same-url, fields: [URL]}\n",
	} {
		t.Run(name, func(t *testing.T) {
			run := newPreprocessRun(t, withKeys(t, keys), baseMapping,
				csvFile("part.csv", "title,text,url\n"+
					"One,Body A,https://example.com/1\n"+
					"Two,Body A,https://example.com/2\n"+
					"Three,Body C,https://example.com/2\n"),
			)

			require.NoError(t, run.execute(t.Context()))

			dir := run.packageDir(t)
			pkg, rows := readArticles(t, dir)
			assert.Equal(t, []string{"https://example.com/1"}, rows.urls)
			assert.Equal(t, map[string]int64{"same-body": 1, "same-url": 1}, pkg.Preprocess.Counts.Duplicates)
			one := rows.byURL["https://example.com/1"].ID.String()
			for _, r := range readResource[rejectParquetRow](t, dir, datapackage.RejectsResource) {
				assert.Equal(t, &one, r.KeptID)
			}
		})
	}
}

func TestPreprocess_ADuplicateRegistersNoOtherKeys(t *testing.T) {
	keys := "    - {name: same-url, fields: [URL]}\n    - {name: same-title, fields: [Title]}\n    - {name: same-body, fields: [Content]}\n"
	run := newPreprocessRun(t, withKeys(t, keys), baseMapping,
		csvFile("part.csv", "title,text,url\n"+
			"One,Body A,https://example.com/1\n"+
			"Two,Body A,https://example.com/2\n"+
			"Two,Body B,https://example.com/3\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	_, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, []string{"https://example.com/1", "https://example.com/3"}, rows.urls)
}

func TestPreprocess_CountsEachDuplicateUnderTheFirstKeyThatMatches(t *testing.T) {
	keys := "    - {name: same-url, fields: [URL]}\n    - {name: same-title, fields: [Title]}\n    - {name: same-body, fields: [Content]}\n"
	run := newPreprocessRun(t, withKeys(t, keys), baseMapping,
		csvFile("part.csv", "title,text,url\n"+
			"One,Body A,https://example.com/1\n"+
			"One,Body A,https://example.com/1\n"+
			"One,Body A,https://example.com/2\n"+
			"Two,Body A,https://example.com/3\n"),
	)

	require.NoError(t, run.execute(t.Context()))

	pkg, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, []string{"https://example.com/1"}, rows.urls)
	assert.Equal(t, map[string]int64{"same-url": 1, "same-title": 1, "same-body": 1}, pkg.Preprocess.Counts.Duplicates)
}

func TestPreprocess_MintsRandomIDsThatDifferOnEveryRun(t *testing.T) {
	run := newPreprocessRun(t, specWith(t, urlKeySpec, "kind: urlHash", "kind: random"), baseMapping,
		csvFile("part.csv", "title,text,url\nOne,Body,https://example.com/1\nTwo,Body,https://example.com/2\nAgain,Body,https://example.com/1\n"),
	)
	second := run.writingTo(t, "again")

	require.NoError(t, run.execute(t.Context()))
	require.NoError(t, second.execute(t.Context()))

	pkg, rows := readArticles(t, run.packageDir(t))
	_, rerun := readArticles(t, second.packageDir(t))
	assert.Equal(t, []string{"https://example.com/1", "https://example.com/2"}, rows.urls)
	one, two := rows.byURL["https://example.com/1"].ID, rows.byURL["https://example.com/2"].ID
	assert.NotEqual(t, one, two)
	assert.NotEqual(t, one, rerun.byURL["https://example.com/1"].ID)
	assert.Equal(t, uuid.Version(7), one.Version())
	assert.Equal(t, datapackage.ArticleIDs{Kind: preprocess.IDKindRandom, Algorithm: "uuidv7"}, pkg.Preprocess.ID)
}

func TestPreprocess_DerivesThePublisherDomainFromTheURL(t *testing.T) {
	tests := []struct {
		url    string
		stored string
		want   string
	}{
		{url: "https://uk.reuters.com/world/storm", want: "reuters.com"},
		{url: "https://WWW.Reuters.com/markets", stored: "https://www.reuters.com/markets", want: "reuters.com"},
		{url: "https://news.bbc.co.uk/1", want: "bbc.co.uk"},
		{url: "https://x.blogspot.com/post", want: "x.blogspot.com"},
		{url: "http://192.168.0.7/feed", want: "192.168.0.7"},
		{url: "http://localhost:8080/page", want: "localhost"},
	}
	var csv strings.Builder
	csv.WriteString("title,text,url\n")
	for _, tt := range tests {
		csv.WriteString("Title,Body," + tt.url + "\n")
	}
	run := newPreprocessRun(t, urlKeySpec, baseMapping, csvFile("part.csv", csv.String()))

	require.NoError(t, run.execute(t.Context()))

	_, rows := readArticles(t, run.packageDir(t))
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			stored := tt.url
			if tt.stored != "" {
				stored = tt.stored
			}
			assert.Equal(t, tt.want, rows.byURL[stored].PublisherDomain)
		})
	}
}

func TestPreprocess_CancelledRunLeavesNoPackage(t *testing.T) {
	rawDir := filepath.Join(t.TempDir(), "raw")
	writeRawPackage(t, rawDir, rawVersion,
		csvFile("a.csv", "title,text,url\nOne,Body,https://example.com/1\n"),
		csvFile("b.csv", "title,text,url\nTwo,Body,https://example.com/2\n"),
	)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	files := http.FileServer(http.Dir(rawDir))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/b.csv") {
			cancel()
		}
		files.ServeHTTP(w, r)
	}))
	defer srv.Close()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mapping.yaml"), []byte(baseMapping), 0o644))
	specPath := filepath.Join(dir, "preprocess.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(specWith(t, urlKeySpec, "input: raw", "input: "+srv.URL+"/datapackage.json")), 0o644))

	err := runPreprocess(ctx, io.Discard, preprocessConfig{
		SpecPath: specPath,
		Packages: datapackage.Options{HTTPClient: srv.Client(), TempDir: t.TempDir()},
	})

	require.ErrorIs(t, err, context.Canceled)
	entries, err := os.ReadDir(filepath.Join(dir, "canonical"))
	if !errors.Is(err, fs.ErrNotExist) {
		require.NoError(t, err)
	}
	assert.Empty(t, entries, "no package and no temporary folder remain")
}

func TestPreprocess_KeepsADatasetsOwnIDAsTheExternalID(t *testing.T) {
	mapping := strings.Replace(baseMapping, "fields:\n", "fields:\n  - {source: uid, target: Metadata.ExternalID}\n", 1)
	run := newPreprocessRun(t, urlKeySpec, mapping, csvFile("part.csv", "uid,title,text,url\n"+
		"89541,One,Body,https://example.com/1\n"+
		",Two,Body,https://example.com/2\n"))

	require.NoError(t, run.execute(t.Context()))

	_, rows := readArticles(t, run.packageDir(t))
	assert.Equal(t, "89541", rows.byURL["https://example.com/1"].Metadata.ExternalID)
	assert.Empty(t, rows.byURL["https://example.com/2"].Metadata.ExternalID)
}

func TestPreprocess_OmitsTheRawVersionWhenTheRawPackageHasNone(t *testing.T) {
	run := newPreprocessRun(t, urlKeySpec, baseMapping, csvFile("part.csv", "title,text,url\nOne,Body,https://example.com/1\n"))
	require.NoError(t, os.RemoveAll(run.rawDir))
	writeRawPackage(t, run.rawDir, "", csvFile("part.csv", "title,text,url\nOne,Body,https://example.com/1\n"))

	require.NoError(t, run.execute(t.Context()))

	descriptor, err := os.ReadFile(filepath.Join(run.packageDir(t), datapackage.DescriptorName))
	require.NoError(t, err)
	var doc struct {
		Raw map[string]json.RawMessage `json:"raw"`
	}
	require.NoError(t, json.Unmarshal(descriptor, &doc))
	assert.Contains(t, doc.Raw, "name")
	assert.NotContains(t, doc.Raw, "version")
}

// readingCounter keeps what was reported before the first stage after reading.
type readingCounter struct {
	progresstest.Recorder
	staged    bool
	readTotal int64
	readCount int64
}

func (c *readingCounter) Stage(name string) func() {
	if !c.staged {
		c.staged = true
		c.readTotal, c.readCount = c.Total(), c.Count()
	}
	return c.Recorder.Stage(name)
}

func TestPreprocess_CountsEveryRecordRead(t *testing.T) {
	shard := func(name string, urls ...string) rawFile {
		rows := make([]rawShardRow, len(urls))
		for i, url := range urls {
			rows[i] = rawShardRow{Title: "Title " + url, Text: "Body", URL: url}
		}
		return parquetFile(t, name, rows...)
	}
	tests := []struct {
		name      string
		files     []rawFile
		wantTotal int64
	}{
		{
			name: "parquet footers give the total up front",
			files: []rawFile{
				shard("a.parquet", "https://example.com/1", "https://example.com/2", "https://example.com/1"),
				shard("b.parquet", "https://example.com/3", "https://example.com/4"),
			},
			wantTotal: 5,
		},
		{
			name: "a csv resource leaves the total unknown",
			files: []rawFile{
				shard("a.parquet", "https://example.com/1"),
				csvFile("b.csv", "title,text,url\nTwo,Body,https://example.com/2\nTwo again,Body,https://example.com/2\n"),
			},
			wantTotal: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := newPreprocessRun(t, urlKeySpec, baseMapping, tt.files...)
			spec, err := preprocess.Load(run.specPath)
			require.NoError(t, err)
			raw, err := datapackage.Open(t.Context(), spec.Input)
			require.NoError(t, err)
			counter := &readingCounter{}

			published, err := ingest.Preprocess(t.Context(), ingest.PreprocessOptions{
				Raw: raw, Spec: spec.Spec, Mapping: spec.Mapping, OutputRoot: spec.OutputRoot, Progress: counter,
			})

			require.NoError(t, err)
			assert.Equal(t, tt.wantTotal, counter.readTotal)
			assert.Equal(t, published.Package.Preprocess.Counts.Read, counter.readCount)
		})
	}
}
