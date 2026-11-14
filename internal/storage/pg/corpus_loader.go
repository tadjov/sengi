package pg

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"github.com/tadjov/tusker/db/migrations"
	"github.com/tadjov/tusker/internal/embedding"
	"github.com/tadjov/tusker/internal/storage"
	"github.com/tadjov/tusker/internal/types/document"
	"github.com/tadjov/tusker/internal/types/language"
	"golang.org/x/sync/errgroup"
)

var _ storage.CorpusLoader = (*CorpusLoader)(nil)

// CorpusLoader migrates the schema, copies the corpus, then times each index build.
type CorpusLoader struct {
	pool    *ConnectionPool
	engine  storage.Engine
	schema  migrations.Set
	indexes []migrations.IndexStatement
}

// NewCorpusLoader needs a pool that skips vector types; the schema migrations create them.
func NewCorpusLoader(pool *ConnectionPool, engine storage.Engine) (*CorpusLoader, error) {
	schema, err := migrations.Schema(engine)
	if err != nil {
		return nil, err
	}
	indexes, err := migrations.SearchIndexes(engine)
	if err != nil {
		return nil, err
	}
	return &CorpusLoader{pool: pool, engine: engine, schema: schema, indexes: indexes}, nil
}

// LoadCorpus refuses a database that already holds a corpus or its search indexes.
func (l *CorpusLoader) LoadCorpus(ctx context.Context, c storage.Corpus, opts storage.LoadOptions) (storage.LoadRecord, error) {
	if err := validateCorpus(c, opts); err != nil {
		return storage.LoadRecord{}, err
	}
	record := storage.LoadRecord{
		Engine:      string(l.engine),
		StartedAt:   now(),
		Canonical:   c.Canonical,
		Embeddings:  c.Embeddings,
		ModelTag:    c.ModelTag,
		BatchSize:   opts.BatchSize,
		Concurrency: opts.Concurrency,
		Stages:      []storage.StageTiming{},
		Settings:    map[string]string{},
	}

	// One session, so index settings read back from it and never reach the pool.
	pooled, err := l.pool.conn.Acquire(ctx)
	if err != nil {
		return storage.LoadRecord{}, fmt.Errorf("acquire connection: %w", err)
	}
	conn := pooled.Hijack()
	defer conn.Close(context.WithoutCancel(ctx))

	if err := lockLoad(ctx, conn); err != nil {
		return storage.LoadRecord{}, err
	}
	if err := timed(&record, "schema", func() error { return l.schema.Up(ctx, conn) }); err != nil {
		return storage.LoadRecord{}, err
	}
	if err := l.refuseLoaded(ctx, conn); err != nil {
		return storage.LoadRecord{}, err
	}
	writers, err := l.openWriters(ctx, opts.Concurrency)
	if err != nil {
		return storage.LoadRecord{}, err
	}
	defer writers.Close()
	copier := &corpusCopier{writers: writers, concurrency: opts.Concurrency, batchSize: opts.BatchSize}

	var ids []uuid.UUID
	err = timed(&record, "copy_articles", func() error {
		ids, err = copier.copyArticles(ctx, c)
		return err
	})
	if err != nil {
		return storage.LoadRecord{}, err
	}
	if len(ids) == 0 {
		return storage.LoadRecord{}, errors.New("corpus has no articles")
	}
	record.Articles = int64(len(ids))

	if c.Vectors != nil {
		err = timed(&record, "copy_embeddings", func() error {
			record.Vectors, err = copier.copyVectors(ctx, c, ids)
			return err
		})
		if err != nil {
			return storage.LoadRecord{}, err
		}
	}
	writers.Close()

	if !opts.SkipIndex {
		if err := l.buildIndexes(ctx, conn, &record); err != nil {
			return storage.LoadRecord{}, err
		}
	}
	err = timed(&record, "vacuum_analyze", func() error {
		defer storage.WatchStage(ctx, []any{"engine", record.Engine, "stage", "vacuum_analyze"}, nil)()
		_, err := conn.Exec(ctx, "VACUUM (ANALYZE) articles, article_embeddings")
		return err
	})
	if err != nil {
		return storage.LoadRecord{}, fmt.Errorf("vacuum analyze: %w", err)
	}
	if inspect := engineKnobs[l.engine].inspect; inspect != nil && !opts.SkipIndex {
		if err := inspect(ctx, conn, record.Settings); err != nil {
			return storage.LoadRecord{}, err
		}
	}
	if record.EngineVersion, err = engineVersion(ctx, conn); err != nil {
		return storage.LoadRecord{}, err
	}

	record.Finish(now())
	// An unindexed corpus is not what a bench run measures, so corpus_load skips it.
	if opts.SkipIndex {
		return record, nil
	}
	if _, err := conn.Exec(ctx, "INSERT INTO corpus_load (finished_at, record) VALUES ($1, $2)", record.FinishedAt, record); err != nil {
		return storage.LoadRecord{}, fmt.Errorf("record the load in corpus_load: %w", err)
	}
	return record, nil
}

func engineVersion(ctx context.Context, conn *pgx.Conn) (string, error) {
	var version strings.Builder
	var server string
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version')").Scan(&server); err != nil {
		return "", fmt.Errorf("read server version: %w", err)
	}
	version.WriteString("PostgreSQL " + server)
	extensions, err := readSearchExtensions(ctx, conn)
	if err != nil {
		return "", err
	}
	for _, e := range extensions {
		fmt.Fprintf(&version, ", %s %s", e.Name, e.Version)
	}
	return version.String(), nil
}

func validateCorpus(c storage.Corpus, opts storage.LoadOptions) error {
	switch {
	case c.Language != language.English:
		return fmt.Errorf("corpus language %q: the Postgres schemas analyse only %q", c.Language, language.English)
	case c.Articles == nil:
		return errors.New("corpus has no articles")
	case (c.Vectors == nil) != (c.Embeddings == nil):
		return errors.New("corpus vectors and their embeddings package must come together")
	case c.Vectors != nil && c.ModelTag == "":
		return errors.New("corpus vectors carry no model tag")
	case opts.BatchSize <= 0:
		return fmt.Errorf("batch size %d: want at least 1", opts.BatchSize)
	case opts.Concurrency <= 0:
		return fmt.Errorf("concurrency %d: want at least 1 batch written at once", opts.Concurrency)
	}
	return nil
}

const loadLockKey = 0x7475736b6572 // "tusker"

func lockLoad(ctx context.Context, conn *pgx.Conn) error {
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", int64(loadLockKey)).Scan(&locked); err != nil {
		return fmt.Errorf("lock the database for loading: %w", err)
	}
	if !locked {
		return errors.New("another corpus load is running on this database")
	}
	return nil
}

func (l *CorpusLoader) refuseLoaded(ctx context.Context, conn *pgx.Conn) error {
	var names []string
	for _, s := range l.indexes {
		if s.Index != "" {
			names = append(names, s.Index)
		}
	}
	var built []string
	err := conn.QueryRow(ctx, `SELECT coalesce(array_agg(indexname ORDER BY indexname), '{}') FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = ANY($1)`, names).Scan(&built)
	if err != nil {
		return fmt.Errorf("check for existing search indexes: %w", err)
	}
	if len(built) > 0 {
		return fmt.Errorf("search indexes %s already exist; drop the schema before loading", strings.Join(built, ", "))
	}
	var loaded bool
	err = conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM articles) OR EXISTS (SELECT 1 FROM corpus_load)").Scan(&loaded)
	if err != nil {
		return fmt.Errorf("check for an existing corpus: %w", err)
	}
	if loaded {
		return errors.New("the database already holds a corpus; drop its schema before loading another")
	}
	return nil
}

var indexBuildSettings = []string{"maintenance_work_mem", "max_parallel_maintenance_workers"}

// buildIndexes times each search index and each derived-table fill as its own stage; a SET or ANALYZE runs untimed.
func (l *CorpusLoader) buildIndexes(ctx context.Context, conn *pgx.Conn, record *storage.LoadRecord) error {
	for _, s := range l.indexes {
		run := func() error {
			if _, err := conn.Exec(ctx, s.SQL); err != nil {
				return fmt.Errorf("build search index %s: %w", s, err)
			}
			return nil
		}
		if s.Stage == "" {
			if err := run(); err != nil {
				return err
			}
			continue
		}
		err := timed(record, s.Stage, func() error {
			defer storage.WatchStage(ctx, []any{"engine", record.Engine, "stage", s.Stage}, l.indexBuildProgress(conn.PgConn().PID()))()
			return run()
		})
		if err != nil {
			return err
		}
	}
	for _, name := range slices.Concat(indexBuildSettings, engineKnobs[l.engine].settings) {
		var value string
		if err := conn.QueryRow(ctx, "SELECT current_setting($1)", name).Scan(&value); err != nil {
			return fmt.Errorf("read setting %s: %w", name, err)
		}
		record.Settings[name] = value
	}
	return nil
}

// indexBuildProgress reads how far the session's CREATE INDEX has got, or nothing when the index type reports none.
func (l *CorpusLoader) indexBuildProgress(pid uint32) func(context.Context) []any {
	return func(ctx context.Context) []any {
		var phase string
		var blocksDone, blocksTotal, tuplesDone, tuplesTotal int64
		err := l.pool.conn.QueryRow(ctx, `SELECT phase, blocks_done, blocks_total, tuples_done, tuples_total
			FROM pg_stat_progress_create_index WHERE pid = $1`, pid).Scan(&phase, &blocksDone, &blocksTotal, &tuplesDone, &tuplesTotal)
		if err != nil {
			return nil
		}
		attrs := []any{"phase", phase}
		if blocksTotal > 0 {
			attrs = append(attrs, "blocks", fmt.Sprintf("%d/%d", blocksDone, blocksTotal))
		}
		if tuplesTotal > 0 {
			attrs = append(attrs, "tuples", fmt.Sprintf("%d/%d", tuplesDone, tuplesTotal))
		}
		return attrs
	}
}

// now truncates to timestamptz's microseconds, so records round-trip through corpus_load.
func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

func timed(record *storage.LoadRecord, stage string, run func() error) error {
	slog.Info("corpus load stage started", "engine", record.Engine, "stage", stage)
	start := time.Now()
	if err := run(); err != nil {
		return err
	}
	seconds := time.Since(start).Seconds()
	record.Stages = append(record.Stages, storage.StageTiming{Name: stage, Seconds: seconds})
	slog.Info("corpus load stage finished", "engine", record.Engine, "stage", stage, "seconds", seconds)
	return nil
}

// openWriters opens the copy connections after the schema has created the vector type they register.
func (l *CorpusLoader) openWriters(ctx context.Context, concurrency int) (*pgxpool.Pool, error) {
	config := l.pool.conn.Config()
	config.MaxConns = int32(concurrency)
	config.MinConns = 0
	config.AfterConnect = afterConnect(true, nil)
	writers, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open %d copy connections: %w", concurrency, err)
	}
	return writers, nil
}

// corpusCopier commits each batch in its own transaction, so a failed copy keeps the batches before it.
type corpusCopier struct {
	writers     *pgxpool.Pool
	concurrency int
	batchSize   int
	committed   atomic.Int64
}

func (cp *corpusCopier) failed(err error) error {
	if cp.committed.Load() == 0 {
		return err
	}
	return fmt.Errorf("%w; the database now holds a partial corpus, recreate its volume before loading again", err)
}

// copyArticles returns the ids in corpus order, which the vectors must follow.
func (cp *corpusCopier) copyArticles(ctx context.Context, c storage.Corpus) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	toRow := func(a document.Article) ([]any, error) {
		if a.ID == uuid.Nil {
			return nil, fmt.Errorf("article %d has no id", len(ids))
		}
		if a.Language != c.Language {
			return nil, fmt.Errorf("article %s is in %q, the corpus in %q", a.ID, a.Language, c.Language)
		}
		ids = append(ids, a.ID)
		return insertRow(a)
	}
	plan := copyPlan[document.Article]{Table: "articles", Columns: ArticleColumns, Items: c.Articles, ToRow: toRow}
	if err := copyConcurrently(ctx, cp, plan); err != nil {
		return nil, cp.failed(fmt.Errorf("copy articles: %w", err))
	}
	return ids, nil
}

// copyVectors refuses any vector out of step with the articles, orphans included.
func (cp *corpusCopier) copyVectors(ctx context.Context, c storage.Corpus, ids []uuid.UUID) (int64, error) {
	var n int64
	toRow := func(v embedding.Vec) ([]any, error) {
		if n >= int64(len(ids)) {
			return nil, fmt.Errorf("vector %d for article %s is past the last of %d articles", n, v.ID, len(ids))
		}
		if v.ID != ids[n] {
			return nil, fmt.Errorf("vector %d is for article %s, article %d is %s", n, v.ID, n, ids[n])
		}
		n++
		return []any{v.ID, c.ModelTag, pgvector.NewVector(v.Embedding)}, nil
	}
	plan := copyPlan[embedding.Vec]{
		Table:   "article_embeddings",
		Columns: []string{"article_id", "model_name", "embedding"},
		Items:   c.Vectors,
		ToRow:   toRow,
	}
	if err := copyConcurrently(ctx, cp, plan); err != nil {
		return 0, cp.failed(fmt.Errorf("copy embeddings: %w", err))
	}
	return n, nil
}

type copyPlan[T any] struct {
	Table   string
	Columns []string
	Items   iter.Seq2[T, error]
	ToRow   func(T) ([]any, error)
}

// copyConcurrently converts rows on one goroutine, in corpus order, and copies each batch on the next free connection.
func copyConcurrently[T any](ctx context.Context, cp *corpusCopier, plan copyPlan[T]) error {
	group, ctx := errgroup.WithContext(ctx)
	batches := make(chan [][]any, cp.concurrency)
	group.Go(func() error {
		defer close(batches)
		return batchRows(ctx, plan, cp.batchSize, batches)
	})
	var copied atomic.Int64
	for range cp.concurrency {
		group.Go(func() error {
			for batch := range batches {
				if _, err := cp.writers.CopyFrom(ctx, pgx.Identifier{plan.Table}, plan.Columns, pgx.CopyFromRows(batch)); err != nil {
					return err
				}
				cp.committed.Add(int64(len(batch)))
				rows := copied.Add(int64(len(batch)))
				if rows/storage.ProgressRows > (rows-int64(len(batch)))/storage.ProgressRows {
					slog.Info("corpus load progress", "table", plan.Table, "rows", rows)
				}
			}
			return nil
		})
	}
	return group.Wait()
}

func batchRows[T any](ctx context.Context, plan copyPlan[T], batchSize int, batches chan<- [][]any) error {
	batch := make([][]any, 0, batchSize)
	send := func() error {
		select {
		case batches <- batch:
			batch = make([][]any, 0, batchSize)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for item, err := range plan.Items {
		if err != nil {
			return err
		}
		row, err := plan.ToRow(item)
		if err != nil {
			return err
		}
		batch = append(batch, row)
		if len(batch) == batchSize {
			if err := send(); err != nil {
				return err
			}
		}
	}
	if len(batch) == 0 {
		return nil
	}
	return send()
}
