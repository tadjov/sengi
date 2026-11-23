package pg

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"github.com/tadjov/sengi/db/migrations"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/progress"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
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
	load := storage.StartLoad(opts.Progress)
	record := storage.LoadRecord{
		Engine:      string(l.engine),
		Canonical:   c.Canonical,
		Embeddings:  c.Embeddings,
		ModelTag:    c.ModelTag,
		BatchSize:   opts.BatchSize,
		Concurrency: opts.Concurrency,
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
	if err := load.Stage("schema", func() error { return l.schema.Up(ctx, conn) }); err != nil {
		return storage.LoadRecord{}, err
	}
	if err := l.refuseLoaded(ctx, conn); err != nil {
		return storage.LoadRecord{}, err
	}
	if err := pauseAutovacuum(ctx, conn); err != nil {
		return storage.LoadRecord{}, err
	}
	copyPool, err := l.openCopyPool(ctx, opts.Concurrency)
	if err != nil {
		return storage.LoadRecord{}, err
	}
	defer copyPool.Close()
	copier := &corpusCopier{pool: copyPool, concurrency: opts.Concurrency, batchSize: opts.BatchSize, counter: load.Counter()}

	var ids []uuid.UUID
	err = load.Stage("copy_articles", func() error {
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
		err = load.Stage("copy_embeddings", func() error {
			record.Vectors, err = copier.copyVectors(ctx, c, ids)
			return err
		})
		if err != nil {
			return storage.LoadRecord{}, err
		}
	}
	copyPool.Close()

	if !opts.SkipIndex {
		if err := l.buildIndexes(ctx, conn, load); err != nil {
			return storage.LoadRecord{}, err
		}
		if err := l.readSettings(ctx, conn, record.Settings); err != nil {
			return storage.LoadRecord{}, err
		}
	}
	err = load.Stage("vacuum_analyze", func() error {
		_, err := conn.Exec(ctx, "VACUUM (ANALYZE) articles, article_embeddings")
		return err
	})
	if err != nil {
		return storage.LoadRecord{}, fmt.Errorf("vacuum analyze: %w", err)
	}
	if err := resumeAutovacuum(ctx, conn); err != nil {
		return storage.LoadRecord{}, err
	}
	if inspect := engineKnobs[l.engine].inspect; inspect != nil && !opts.SkipIndex {
		if err := inspect(ctx, conn, record.Settings); err != nil {
			return storage.LoadRecord{}, err
		}
	}
	if record.EngineVersion, err = engineVersion(ctx, conn); err != nil {
		return storage.LoadRecord{}, err
	}

	load.Finish(&record)
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

const loadLockKey = 0x73656e6769 // "sengi"

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

var serverSettings = []string{
	"shared_buffers", "effective_cache_size", "maintenance_work_mem", "max_parallel_maintenance_workers",
	"max_wal_size", "checkpoint_timeout", "autovacuum",
}

// buildIndexes times each search index and each derived-table fill as its own stage; a SET or ANALYZE runs untimed.
func (l *CorpusLoader) buildIndexes(ctx context.Context, conn *pgx.Conn, load *storage.LoadStopwatch) error {
	for _, s := range l.indexes {
		run := func() error {
			if _, err := conn.Exec(ctx, s.SQL); err != nil {
				// The detail carries the offending row, such as the key a primary key found twice.
				if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Detail != "" {
					return fmt.Errorf("build %s: %w: %s", s, err, pgErr.Detail)
				}
				return fmt.Errorf("build %s: %w", s, err)
			}
			return nil
		}
		if s.Stage == "" {
			if err := run(); err != nil {
				return err
			}
			continue
		}
		err := load.Stage(s.Stage, func() error {
			defer l.watchIndexBuild(ctx, load.Counter(), conn.PgConn().PID())()
			return run()
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (l *CorpusLoader) readSettings(ctx context.Context, conn *pgx.Conn, settings map[string]string) error {
	for _, name := range slices.Concat(serverSettings, engineKnobs[l.engine].settings) {
		var value string
		if err := conn.QueryRow(ctx, "SELECT current_setting($1)", name).Scan(&value); err != nil {
			return fmt.Errorf("read setting %s: %w", name, err)
		}
		settings[name] = value
	}
	return nil
}

const indexProgressInterval = 10 * time.Second

// watchIndexBuild reports how far the session's CREATE INDEX has got, every indexProgressInterval until stop is called.
func (l *CorpusLoader) watchIndexBuild(ctx context.Context, counter progress.Counter, pid uint32) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(indexProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if attrs := l.indexBuildProgress(ctx, pid); attrs != nil {
					counter.Detail(attrs...)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// indexBuildProgress returns nothing when the index type reports no progress.
func (l *CorpusLoader) indexBuildProgress(ctx context.Context, pid uint32) []any {
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

var corpusTables = []string{"articles", "article_embeddings"}

// pauseAutovacuum keeps autovacuum off the corpus tables, TOAST included, until the load's own vacuum.
func pauseAutovacuum(ctx context.Context, conn *pgx.Conn) error {
	for _, table := range corpusTables {
		if _, err := conn.Exec(ctx, "ALTER TABLE "+pgx.Identifier{table}.Sanitize()+" SET (autovacuum_enabled = false, toast.autovacuum_enabled = false)"); err != nil {
			return fmt.Errorf("pause autovacuum on %s: %w", table, err)
		}
	}
	return nil
}

func resumeAutovacuum(ctx context.Context, conn *pgx.Conn) error {
	for _, table := range corpusTables {
		if _, err := conn.Exec(ctx, "ALTER TABLE "+pgx.Identifier{table}.Sanitize()+" RESET (autovacuum_enabled, toast.autovacuum_enabled)"); err != nil {
			return fmt.Errorf("resume autovacuum on %s: %w", table, err)
		}
	}
	return nil
}

// openCopyPool needs the schema's vector type, which its connections register.
func (l *CorpusLoader) openCopyPool(ctx context.Context, concurrency int) (*pgxpool.Pool, error) {
	config := l.pool.conn.Config()
	config.MaxConns = int32(concurrency)
	config.MinConns = 0
	config.AfterConnect = afterConnect(true, nil)
	copyPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open %d copy connections: %w", concurrency, err)
	}
	return copyPool, nil
}

// corpusCopier commits each batch in its own transaction, so a failed copy keeps whichever batches committed.
type corpusCopier struct {
	pool        *pgxpool.Pool
	concurrency int
	batchSize   int
	committed   atomic.Int64
	counter     progress.Counter
}

func (cp *corpusCopier) failed(err error) error {
	if cp.committed.Load() == 0 {
		return err
	}
	return fmt.Errorf("%w; the database now holds a partial corpus, drop its schema before loading again", err)
}

// copyArticles returns the ids in corpus order, which the vectors must follow.
func (cp *corpusCopier) copyArticles(ctx context.Context, c storage.Corpus) ([]uuid.UUID, error) {
	cp.counter.SetTotal(c.ArticleCount)
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
	plan := copyPlan[document.Article]{Table: "articles", Columns: ArticleColumns, Items: c.Articles, ToRow: toRow, BatchSize: cp.batchSize}
	if err := copyConcurrently(ctx, cp, plan); err != nil {
		return nil, cp.failed(fmt.Errorf("copy articles: %w", err))
	}
	return ids, nil
}

// copyVectors refuses any vector out of step with the articles, orphans included.
func (cp *corpusCopier) copyVectors(ctx context.Context, c storage.Corpus, ids []uuid.UUID) (int64, error) {
	cp.counter.SetTotal(c.VectorCount)
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
		Table:     "article_embeddings",
		Columns:   []string{"article_id", "model_name", "embedding"},
		Items:     c.Vectors,
		ToRow:     toRow,
		BatchSize: cp.batchSize,
	}
	if err := copyConcurrently(ctx, cp, plan); err != nil {
		return 0, cp.failed(fmt.Errorf("copy embeddings: %w", err))
	}
	return n, nil
}

type copyPlan[T any] struct {
	Table     string
	Columns   []string
	Items     iter.Seq2[T, error]
	ToRow     func(T) ([]any, error)
	BatchSize int
}

// copyConcurrently converts rows on one goroutine, in corpus order, and copies each batch on the next free connection.
func copyConcurrently[T any](ctx context.Context, cp *corpusCopier, plan copyPlan[T]) error {
	group, ctx := errgroup.WithContext(ctx)
	batches := make(chan [][]any, cp.concurrency)
	group.Go(func() error {
		defer close(batches)
		return batchRows(ctx, plan, batches)
	})
	for range cp.concurrency {
		group.Go(func() error {
			for batch := range batches {
				if _, err := cp.pool.CopyFrom(ctx, pgx.Identifier{plan.Table}, plan.Columns, pgx.CopyFromRows(batch)); err != nil {
					return err
				}
				cp.committed.Add(int64(len(batch)))
				cp.counter.Add(int64(len(batch)))
			}
			return nil
		})
	}
	return group.Wait()
}

func batchRows[T any](ctx context.Context, plan copyPlan[T], batches chan<- [][]any) error {
	batch := make([][]any, 0, plan.BatchSize)
	send := func() error {
		select {
		case batches <- batch:
			batch = make([][]any, 0, plan.BatchSize)
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
		if len(batch) == plan.BatchSize {
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
