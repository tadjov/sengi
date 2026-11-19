package pg_test

import (
	"context"
	"fmt"
	"iter"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/db/migrations"
	"github.com/tadjov/tusker/internal/embedding"
	"github.com/tadjov/tusker/internal/storage"
	"github.com/tadjov/tusker/internal/storage/pg"
	"github.com/tadjov/tusker/internal/types/document"
	"github.com/tadjov/tusker/internal/types/language"
	pkgtesting "github.com/tadjov/tusker/pkg/testing"
)

const (
	corpusSize = 2_000
	modelTag   = "qwen3-embedding:0.6b@1024"
)

var databases atomic.Int64

var fixtureNamespace = uuid.MustParse("3b1f6f0e-2a4c-4d3e-9b1a-7c2d5e8f9a10")

var declaredIndexes = map[storage.Engine][]string{
	storage.PostgresNative: {"idx_articles_search_vector", "idx_articles_title_trgm", "idx_articles_title_desc_trgm", "idx_article_embedding", "idx_corpus_words_trgm"},
	storage.ParadeDB:       {"idx_articles_search", "idx_article_embedding"},
	storage.Tiger:          {"idx_articles_bm25", "idx_articles_title_bm25", "idx_article_embedding", "idx_corpus_words_trgm"},
}

// fillsCorpusWords are the engines whose fuzzy search expands query words
// against a table of the corpus's words.
var fillsCorpusWords = map[storage.Engine]bool{storage.PostgresNative: true, storage.Tiger: true}

var filterIndexes = map[storage.Engine][]string{
	storage.PostgresNative: {"idx_articles_published_at", "idx_articles_publisher_domain"},
	storage.ParadeDB:       {"idx_articles_published_at"},
	storage.Tiger:          {"idx_articles_published_at", "idx_articles_publisher_domain"},
}

var declaredSettings = map[storage.Engine]map[string]string{
	storage.PostgresNative: {
		"maintenance_work_mem":             "4GB",
		"max_parallel_maintenance_workers": "3",
		"shared_buffers":                   "1536MB",
		"effective_cache_size":             "4608MB",
		"max_wal_size":                     "16GB",
		"checkpoint_timeout":               "30min",
		"autovacuum":                       "on",
	},
	storage.ParadeDB: {
		"maintenance_work_mem":                     "4GB",
		"max_parallel_maintenance_workers":         "3",
		"shared_buffers":                           "1536MB",
		"effective_cache_size":                     "4608MB",
		"max_wal_size":                             "16GB",
		"checkpoint_timeout":                       "30min",
		"autovacuum":                               "on",
		"idx_articles_search.target_segment_count": "1",
		"idx_articles_search.mutable_segment_rows": "1000",
		"idx_articles_search.segments":             "1",
	},
	storage.Tiger: {
		"maintenance_work_mem":             "4GB",
		"max_parallel_maintenance_workers": "3",
		"shared_buffers":                   "1536MB",
		"effective_cache_size":             "4608MB",
		"max_wal_size":                     "16GB",
		"checkpoint_timeout":               "30min",
		"autovacuum":                       "on",
		"pg_textsearch.memory_limit":       "2GB",
		"pg_textsearch.segments_per_level": "8",
		"idx_articles_bm25.segments":       "1",
		"idx_articles_title_bm25.segments": "1",
	},
}

func TestCorpusLoader(t *testing.T) {
	for _, engine := range []storage.Engine{storage.PostgresNative, storage.ParadeDB, storage.Tiger} {
		t.Run(string(engine), func(t *testing.T) {
			server := pkgtesting.NewPGContainerWithConfig(context.Background(), t, pkgtesting.PGConfig{
				Engine:     engine,
				Unmigrated: true,
			})

			t.Run("loads every article with its vector at any concurrency", func(t *testing.T) {
				for _, concurrency := range []int{1, 4} {
					t.Run(fmt.Sprintf("concurrency %d", concurrency), func(t *testing.T) {
						db := newDatabase(t, server.ConnString)
						articles, vectors := fixture(corpusSize)

						record, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 100, Concurrency: concurrency})
						require.NoError(t, err)

						assert.Equal(t, int64(corpusSize), record.Articles)
						assert.Equal(t, int64(corpusSize), record.Vectors)
						fixtureIDs := make([]uuid.UUID, len(articles))
						for i, a := range articles {
							fixtureIDs[i] = a.ID
						}
						assert.ElementsMatch(t, fixtureIDs, storedIDs(t, db, "SELECT id FROM articles"))
						assert.ElementsMatch(t, fixtureIDs, storedIDs(t, db, "SELECT article_id FROM article_embeddings"))
						sample := []int{0, 1, 999, corpusSize - 1}
						ids := make([]uuid.UUID, len(sample))
						for i, at := range sample {
							ids[i] = articles[at].ID
						}
						stored := readArticles(t, db, ids)
						storedVectors := readVectors(t, db, ids)
						for _, at := range sample {
							want := articles[at]
							assert.Equal(t, want, stored[want.ID], "article %d", at)
							assert.Equal(t, vectors[at].Embedding, storedVectors[want.ID], "vector of article %d", at)
						}
					})
				}
			})

			t.Run("a batch rejected mid-load at concurrency 4 fails the load and the next load is refused", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)
				vectors[1_500].Embedding = vectors[1_500].Embedding[:3]

				_, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 100, Concurrency: 4})

				require.ErrorContains(t, err, "partial corpus")
				assert.False(t, loadRecorded(t, db), "the failed load was recorded in corpus_load")
				retry, _ := fixture(corpusSize)
				_, err = load(t, db, engine, corpusOf(retry, nil), storage.LoadOptions{BatchSize: 100, Concurrency: 4})
				require.ErrorContains(t, err, "already holds a corpus")
			})

			t.Run("loads a trial whose vectors cover the first articles only", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)

				record, err := load(t, db, engine, corpusOf(articles, vectors[:100]), storage.LoadOptions{BatchSize: 500, Concurrency: 1})
				require.NoError(t, err)

				assert.Equal(t, int64(corpusSize), record.Articles)
				assert.Equal(t, int64(100), record.Vectors)
				stored := readVectors(t, db, []uuid.UUID{articles[99].ID, articles[100].ID})
				assert.Equal(t, vectors[99].Embedding, stored[articles[99].ID])
				assert.NotContains(t, stored, articles[100].ID)
			})

			t.Run("indexes text with the corpus language's analysis", func(t *testing.T) {
				analysis := language.English.Analysis()
				wantFragments := map[storage.Engine]map[string][]string{
					storage.PostgresNative: {"idx_articles_search_vector": {"USING gin (search_vector)"}},
					storage.ParadeDB: {"idx_articles_search": {
						"((title)::" + analysis.ParadeTokenizer + ")",
						"((description)::" + analysis.ParadeTokenizer + ")",
						"((content)::" + analysis.ParadeTokenizer + ")",
						"published_at",
						"((publisher_domain)::pdb.literal)",
						"key_field=id",
					}},
					storage.Tiger: {"idx_articles_bm25": {
						"USING bm25 (search_text)",
						"text_config=" + analysis.TigerTextConfig,
						"k1='1.2'",
						"b='0.75'",
					}},
				}
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)

				_, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 1})
				require.NoError(t, err)

				present := indexDefinitions(t, db)
				for index, fragments := range wantFragments[engine] {
					for _, fragment := range fragments {
						assert.Contains(t, present[index], fragment)
					}
				}
			})

			t.Run("refuses a corpus it cannot load faithfully before touching the database", func(t *testing.T) {
				articles, vectors := fixture(10)
				cases := map[string]struct {
					corpus func() storage.Corpus
					opts   storage.LoadOptions
				}{
					"a language the DDL does not analyse": {
						corpus: func() storage.Corpus {
							c := corpusOf(articles, vectors)
							c.Language = "srp"
							return c
						},
						opts: storage.LoadOptions{BatchSize: 500, Concurrency: 1},
					},
					"vectors without a model tag": {
						corpus: func() storage.Corpus {
							c := corpusOf(articles, vectors)
							c.ModelTag = ""
							return c
						},
						opts: storage.LoadOptions{BatchSize: 500, Concurrency: 1},
					},
					"no batch size": {
						corpus: func() storage.Corpus { return corpusOf(articles, vectors) },
						opts:   storage.LoadOptions{Concurrency: 1},
					},
					"no concurrency": {
						corpus: func() storage.Corpus { return corpusOf(articles, vectors) },
						opts:   storage.LoadOptions{BatchSize: 500},
					},
				}
				for name, tc := range cases {
					t.Run(name, func(t *testing.T) {
						db := newDatabase(t, server.ConnString)

						_, err := load(t, db, engine, tc.corpus(), tc.opts)

						require.Error(t, err)
						assert.False(t, tableExists(t, db, "articles"), "the refused load created the schema")
					})
				}
			})

			t.Run("refuses an empty corpus and leaves the database loadable", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)

				_, err := load(t, db, engine, corpusOf([]document.Article{}, nil), storage.LoadOptions{BatchSize: 500, Concurrency: 1})

				require.ErrorContains(t, err, "corpus has no articles")
				var recorded bool
				query(t, db, func(conn *pgx.Conn) error {
					return conn.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM corpus_load)").Scan(&recorded)
				})
				assert.False(t, recorded, "the refused load was recorded in corpus_load")
				present := indexDefinitions(t, db)
				for _, name := range declaredIndexes[engine] {
					assert.NotContains(t, present, name)
				}

				articles, vectors := fixture(10)
				_, err = load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 1})
				require.NoError(t, err)
			})

			t.Run("refuses vectors out of step with the articles, records nothing and refuses the next load", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)
				orphan, _ := fixture(corpusSize + 1)
				vectors[corpusSize-1].ID = orphan[corpusSize].ID

				_, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 1})

				require.ErrorContains(t, err, orphan[corpusSize].ID.String())
				assert.False(t, loadRecorded(t, db), "the refused load was recorded in corpus_load")
				_, err = load(t, db, engine, corpusOf(articles, vectors[:corpusSize-1]), storage.LoadOptions{BatchSize: 500, Concurrency: 1})
				require.ErrorContains(t, err, "already holds a corpus")
			})

			t.Run("refuses a database that already holds a corpus", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)
				first, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 1})
				require.NoError(t, err)

				replacement, _ := fixture(corpusSize + 1)
				_, err = load(t, db, engine, corpusOf(replacement[corpusSize:], nil), storage.LoadOptions{BatchSize: 500, Concurrency: 1})

				require.Error(t, err)
				assert.Empty(t, readArticles(t, db, []uuid.UUID{replacement[corpusSize].ID}))
				var records int
				query(t, db, func(conn *pgx.Conn) error {
					return conn.QueryRow(context.Background(),
						"SELECT count(*) FROM corpus_load WHERE finished_at <> $1", first.FinishedAt).Scan(&records)
				})
				assert.Zero(t, records, "the refused load added a record")
			})

			t.Run("refuses a database whose indexes exist before the data", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				query(t, db, func(conn *pgx.Conn) error {
					return migrations.Build(context.Background(), conn, engine)
				})
				articles, vectors := fixture(10)

				_, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 1})

				require.Error(t, err)
				assert.Empty(t, readArticles(t, db, []uuid.UUID{articles[0].ID}))
			})

			t.Run("stops on cancel and records nothing", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c := corpusOf(articles, vectors)
				c.Articles = func(yield func(document.Article, error) bool) {
					for i, a := range articles {
						if i == corpusSize/2 {
							cancel()
						}
						if !yield(a, nil) {
							return
						}
					}
				}

				_, err := loadWithContext(ctx, t, db, engine, c, storage.LoadOptions{BatchSize: 100, Concurrency: 1})

				require.ErrorIs(t, err, context.Canceled)
				assert.False(t, loadRecorded(t, db), "the cancelled load was recorded in corpus_load")
			})

			if engine == storage.ParadeDB {
				t.Run("builds the bm25 index as one segment", func(t *testing.T) {
					db := newDatabase(t, server.ConnString)
					// Enough rows for parallel build workers, each writing its own segment.
					const rows = 20_000
					articles, _ := fixture(rows)
					for i := range articles {
						articles[i].Content = strings.Repeat(articles[i].Content+" ", 25)
					}

					_, err := load(t, db, engine, corpusOf(articles, nil), storage.LoadOptions{BatchSize: 5_000, Concurrency: 1})
					require.NoError(t, err)

					var segments, documents int
					query(t, db, func(conn *pgx.Conn) error {
						return conn.QueryRow(context.Background(),
							"SELECT count(*), sum(num_docs) FROM paradedb.index_info('idx_articles_search', false)").Scan(&segments, &documents)
					})
					assert.Equal(t, 1, segments)
					assert.Equal(t, rows, documents)
				})
			}

			t.Run("records the load in corpus_load", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)

				record, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 3})
				require.NoError(t, err)

				var stored storage.LoadRecord
				var serverVersion string
				query(t, db, func(conn *pgx.Conn) error {
					if err := conn.QueryRow(context.Background(), "SHOW server_version").Scan(&serverVersion); err != nil {
						return err
					}
					return conn.QueryRow(context.Background(),
						"SELECT record FROM corpus_load WHERE finished_at = $1", record.FinishedAt).Scan(&stored)
				})
				assert.Equal(t, record, stored)
				assert.Equal(t, string(engine), stored.Engine)
				assert.Contains(t, stored.EngineVersion, serverVersion)
				assert.Equal(t, 500, stored.BatchSize)
				assert.Equal(t, 3, stored.Concurrency)
				assert.Equal(t, modelTag, stored.ModelTag)
			})

			t.Run("skip-index loads the data without any declared index or record", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)

				record, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 1, SkipIndex: true})
				require.NoError(t, err)

				assert.Equal(t, int64(corpusSize), record.Articles)
				present := indexDefinitions(t, db)
				for _, name := range declaredIndexes[engine] {
					assert.NotContains(t, present, name)
				}
				var recorded bool
				query(t, db, func(conn *pgx.Conn) error {
					return conn.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM corpus_load)").Scan(&recorded)
				})
				assert.False(t, recorded, "a load without its indexes is recorded in corpus_load")
			})

			t.Run("vacuums and analyzes the loaded tables", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)

				_, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 1, SkipIndex: true})
				require.NoError(t, err)

				for _, table := range []string{"articles", "article_embeddings"} {
					var allVisible, analyzedColumns int
					query(t, db, func(conn *pgx.Conn) error {
						return conn.QueryRow(context.Background(), `SELECT
							(SELECT relallvisible FROM pg_class WHERE oid = to_regclass($1)),
							(SELECT count(*) FROM pg_stats WHERE schemaname = 'public' AND tablename = $1)`,
							table).Scan(&allVisible, &analyzedColumns)
					})
					assert.Positive(t, allVisible, "%s has no all-visible pages, so it was not vacuumed", table)
					assert.Positive(t, analyzedColumns, "%s has no column statistics, so it was not analyzed", table)
				}
			})

			t.Run("keeps autovacuum off the corpus tables while it copies them", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)
				c := corpusOf(articles, vectors)
				var duringCopy map[string][]string
				var probeErr error
				c.Articles = func(yield func(document.Article, error) bool) {
					for i, a := range articles {
						if i == corpusSize/2 {
							duringCopy, probeErr = storageParameters(db)
						}
						if !yield(a, nil) {
							return
						}
					}
				}

				_, err := load(t, db, engine, c, storage.LoadOptions{BatchSize: 100, Concurrency: 4})

				require.NoError(t, err)
				require.NoError(t, probeErr)
				for _, relation := range []string{"articles", "articles.toast", "article_embeddings", "article_embeddings.toast"} {
					assert.Contains(t, duringCopy[relation], "autovacuum_enabled=false", "storage parameters of %s during the copy", relation)
				}
			})

			t.Run("vacuums the corpus words it fills", func(t *testing.T) {
				if !fillsCorpusWords[engine] {
					t.Skip("no corpus words table on this engine")
				}
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)
				_, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 1})
				require.NoError(t, err)

				var allVisible int
				query(t, db, func(conn *pgx.Conn) error {
					return conn.QueryRow(context.Background(), "SELECT relallvisible FROM pg_class WHERE oid = to_regclass('corpus_words')").Scan(&allVisible)
				})
				assert.Positive(t, allVisible, "corpus_words has no all-visible pages, so it was not vacuumed")
			})

			t.Run("hands the corpus tables back to autovacuum after the load", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(10)
				_, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 1})
				require.NoError(t, err)

				for _, table := range []string{"articles", "article_embeddings"} {
					var options []string
					query(t, db, func(conn *pgx.Conn) error {
						return conn.QueryRow(context.Background(), `SELECT coalesce(c.reloptions, '{}') || coalesce(toast.reloptions, '{}')
							FROM pg_class c LEFT JOIN pg_class toast ON toast.oid = c.reltoastrelid
							WHERE c.oid = to_regclass($1)`, table).Scan(&options)
					})
					assert.Empty(t, options, "%s keeps storage parameters after the load", table)
				}
			})

			t.Run("builds every declared index as its own timed stage", func(t *testing.T) {
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)

				record, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 1})
				require.NoError(t, err)

				present := indexDefinitions(t, db)
				stages := map[string]int{}
				var staged float64
				for _, stage := range record.Stages {
					stages[stage.Name]++
					staged += stage.Seconds
				}
				assert.GreaterOrEqual(t, record.TotalSeconds, staged, "the total spans every stage")
				for _, name := range declaredIndexes[engine] {
					assert.Contains(t, present, name)
					assert.Equal(t, 1, stages[name], "stages timing index %s", name)
				}
				assert.Contains(t, present["idx_article_embedding"], "WITH (m='16', ef_construction='64')")
				for _, name := range filterIndexes[engine] {
					assert.Contains(t, present, name)
					assert.Zero(t, stages[name], "the schema builds filter index %s, so no stage times it", name)
				}
				assert.Equal(t, declaredSettings[engine], record.Settings)
			})

			t.Run("fills the corpus words after the load as a timed stage", func(t *testing.T) {
				if !fillsCorpusWords[engine] {
					t.Skip("no corpus words table on this engine")
				}
				db := newDatabase(t, server.ConnString)
				articles, vectors := fixture(corpusSize)

				record, err := load(t, db, engine, corpusOf(articles, vectors), storage.LoadOptions{BatchSize: 500, Concurrency: 1})
				require.NoError(t, err)

				words := map[string]int{}
				query(t, db, func(conn *pgx.Conn) error {
					rows, err := conn.Query(context.Background(), "SELECT word, ndoc FROM corpus_words")
					if err != nil {
						return err
					}
					for rows.Next() {
						var word string
						var ndoc int
						if err := rows.Scan(&word, &ndoc); err != nil {
							return err
						}
						words[word] = ndoc
					}
					return rows.Err()
				})
				// Every fixture article's title and content say "storm"; "landfall" is in content only.
				assert.Equal(t, corpusSize, words["storm"])
				assert.Equal(t, corpusSize, words["landfall"])
				assert.Equal(t, corpusSize, words["thousands"], "description words are counted")
				for word := range words {
					assert.Regexp(t, `^[a-z]+$`, word, "only words of letters are kept")
				}
				stages := map[string]int{}
				for _, stage := range record.Stages {
					stages[stage.Name]++
				}
				assert.Equal(t, 1, stages["fill_corpus_words"])
			})
		})
	}
}

func load(t *testing.T, connString string, engine storage.Engine, c storage.Corpus, opts storage.LoadOptions) (storage.LoadRecord, error) {
	t.Helper()
	return loadWithContext(context.Background(), t, connString, engine, c, opts)
}

func loadWithContext(ctx context.Context, t *testing.T, connString string, engine storage.Engine, c storage.Corpus, opts storage.LoadOptions) (storage.LoadRecord, error) {
	t.Helper()
	pool, err := pg.NewConnectionPool(context.Background(), pg.PoolConfig{ConnStr: connString})
	require.NoError(t, err)
	defer pool.Close()
	loader, err := pg.NewCorpusLoader(pool, engine)
	require.NoError(t, err)
	return loader.LoadCorpus(ctx, c, opts)
}

func newDatabase(t *testing.T, serverConnString string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, serverConnString)
	require.NoError(t, err)
	defer conn.Close(ctx)

	name := fmt.Sprintf("load_%d", databases.Add(1))
	_, err = conn.Exec(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)

	u, err := url.Parse(serverConnString)
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}

func fixture(n int) ([]document.Article, []embedding.Vec) {
	articles := make([]document.Article, n)
	vectors := make([]embedding.Vec, n)
	for i := range n {
		id := uuid.NewSHA1(fixtureNamespace, fmt.Appendf(nil, "article-%d", i))
		articles[i] = document.Article{
			ID:              id,
			URL:             fmt.Sprintf("https://news.example.com/%d", i),
			Title:           fmt.Sprintf("Storm number %d hits the coast", i),
			Description:     "Thousands without power",
			Content:         fmt.Sprintf("The storm made landfall overnight, report %d.", i),
			PublishedAt:     time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC).Add(time.Duration(i) * time.Hour),
			Language:        language.English,
			PublisherDomain: "example.com",
			Metadata: document.Metadata{
				PublisherName: "Example News",
				Authors:       []string{"Jane Doe"},
				Extra:         map[string]any{"rank": fmt.Sprint(i)},
			},
		}
		values := make([]float32, 1024)
		values[i%1024] = 1
		values[(i+1)%1024] = float32(i) / float32(n)
		vectors[i] = embedding.Vec{ID: id, Model: modelTag, Embedding: values}
	}
	return articles, vectors
}

func corpusOf(articles []document.Article, vectors []embedding.Vec) storage.Corpus {
	c := storage.Corpus{
		Articles:  seq(articles),
		Language:  language.English,
		Canonical: storage.PackageRef{Name: "fixture-canonical", Version: "article-v2-00000000", ResourceHash: "sha256:" + zeros(64)},
	}
	if vectors != nil {
		c.Vectors = seq(vectors)
		c.ModelTag = modelTag
		c.Embeddings = &storage.PackageRef{Name: "fixture-embeddings", Version: "lead-v1-1024-00000000", ResourceHash: "sha256:" + zeros(64)}
	}
	return c
}

func seq[T any](items []T) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for _, item := range items {
			if !yield(item, nil) {
				return
			}
		}
	}
}

func zeros(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}

func readArticles(t *testing.T, connString string, ids []uuid.UUID) map[uuid.UUID]document.Article {
	t.Helper()
	pool, err := pg.NewConnectionPool(context.Background(), pg.PoolConfig{ConnStr: connString})
	require.NoError(t, err)
	defer pool.Close()
	articles, err := pg.NewArticleReader(pool).GetByIDs(context.Background(), ids)
	require.NoError(t, err)
	byID := make(map[uuid.UUID]document.Article, len(articles))
	for _, a := range articles {
		a.PublishedAt = a.PublishedAt.UTC()
		byID[a.ID] = a
	}
	return byID
}

func storedIDs(t *testing.T, connString, sql string) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	query(t, connString, func(conn *pgx.Conn) error {
		rows, err := conn.Query(context.Background(), sql)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	})
	return ids
}

// storageParameters reads the reloptions of each corpus table and its TOAST table without a *testing.T,
// so a corpus iterator running on the loader's goroutine can call it.
func storageParameters(connString string) (map[string][]string, error) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	parameters := map[string][]string{}
	for _, table := range []string{"articles", "article_embeddings"} {
		var main, toast []string
		err := conn.QueryRow(ctx, `SELECT coalesce(c.reloptions, '{}'), coalesce(t.reloptions, '{}')
			FROM pg_class c LEFT JOIN pg_class t ON t.oid = c.reltoastrelid
			WHERE c.oid = to_regclass($1)`, table).Scan(&main, &toast)
		if err != nil {
			return nil, err
		}
		parameters[table], parameters[table+".toast"] = main, toast
	}
	return parameters, nil
}

func loadRecorded(t *testing.T, connString string) bool {
	t.Helper()
	var recorded bool
	query(t, connString, func(conn *pgx.Conn) error {
		return conn.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM corpus_load)").Scan(&recorded)
	})
	return recorded
}

func tableExists(t *testing.T, connString, table string) bool {
	t.Helper()
	var exists bool
	query(t, connString, func(conn *pgx.Conn) error {
		return conn.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists)
	})
	return exists
}

func query(t *testing.T, connString string, run func(*pgx.Conn) error) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), connString)
	require.NoError(t, err)
	defer conn.Close(context.Background())
	require.NoError(t, run(conn))
}

func indexDefinitions(t *testing.T, connString string) map[string]string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connString)
	require.NoError(t, err)
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT indexname, indexdef FROM pg_indexes
		WHERE tablename IN ('articles', 'article_embeddings', 'corpus_words') AND indexname NOT LIKE '%_pkey'`)
	require.NoError(t, err)
	definitions := map[string]string{}
	for rows.Next() {
		var name, definition string
		require.NoError(t, rows.Scan(&name, &definition))
		definitions[name] = definition
	}
	require.NoError(t, rows.Err())
	return definitions
}

func readVectors(t *testing.T, connString string, ids []uuid.UUID) map[uuid.UUID][]float32 {
	t.Helper()
	pool, err := pg.NewConnectionPool(context.Background(), pg.PoolConfig{ConnStr: connString, RegisterVecTypes: true})
	require.NoError(t, err)
	defer pool.Close()
	vectors, err := pg.NewVectorStore(nil, pool, modelTag).DocVectors(context.Background(), ids)
	require.NoError(t, err)
	return vectors
}
