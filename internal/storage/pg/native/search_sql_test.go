package native

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/pg/pgtest"
	"github.com/tadjov/sengi/internal/storage/storagetest"
	"github.com/tadjov/sengi/internal/types/operator"
	dquery "github.com/tadjov/sengi/internal/types/query"
)

var updateGolden = flag.Bool("update", false, "rewrite search_sql.golden from the SQL the searcher sends")

type sqlRecorder struct {
	mu         sync.Mutex
	statements []string
}

func (r *sqlRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, data.SQL)
	return ctx
}

func (r *sqlRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *sqlRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	taken := r.statements
	r.statements = nil
	return taken
}

// The benchmark's Postgres arms are compared on this SQL, so a change to its text is a new run.
func TestSearcher_SendsThePinnedSQL(t *testing.T) {
	if testPool == nil {
		t.Skip("requires Docker (testcontainers Postgres)")
	}
	connString := pgtest.Load(t, testServer, pgtest.Corpus{Articles: storagetest.ParityCorpus()})
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("parse %s: %v", connString, err)
	}
	recorder := &sqlRecorder{}
	config.ConnConfig.Tracer = recorder
	pool, err := pgxpool.NewWithConfig(testCtx, config)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	s := &Searcher{db: pool}

	type search func(opts *dquery.BaseOptions) (*storage.SearchResult, error)
	paradigms := []struct {
		name   string
		search search
	}{
		{"string", func(opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			return s.SearchStringQuery(testCtx, dquery.NewString("budget tariff", dquery.StringOptions{}), opts)
		}},
		{"field title", func(opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			match, err := dquery.NewMatch(dquery.FieldTitle, "budget", dquery.MatchOptions{})
			if err != nil {
				return nil, err
			}
			return s.SearchField(testCtx, match, opts)
		}},
		{"field content or", func(opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			match, err := dquery.NewMatch(dquery.FieldContent, "budget tariff", dquery.MatchOptions{Operator: operator.Explicit(operator.Or)})
			if err != nil {
				return nil, err
			}
			return s.SearchField(testCtx, match, opts)
		}},
		{"fields boosted", func(opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			q, err := dquery.NewMultiMatch("budget", []string{"title^3", "content"}, dquery.MultiMatchOptions{})
			if err != nil {
				return nil, err
			}
			return s.SearchFields(testCtx, q, opts)
		}},
		{"fields all", func(opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			q, err := dquery.NewMultiMatch("budget", []string{"title", "description", "content"}, dquery.MultiMatchOptions{})
			if err != nil {
				return nil, err
			}
			return s.SearchFields(testCtx, q, opts)
		}},
		{"phrase exact", func(opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			q, err := dquery.NewPhrase("budget tariff", []dquery.Field{dquery.FieldTitle}, dquery.PhraseOptions{})
			if err != nil {
				return nil, err
			}
			return s.SearchPhrase(testCtx, q, opts)
		}},
		{"phrase slop", func(opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			q, err := dquery.NewPhrase("budget tariff", []dquery.Field{dquery.FieldTitle, dquery.FieldDescription}, dquery.PhraseOptions{Slop: 2})
			if err != nil {
				return nil, err
			}
			return s.SearchPhrase(testCtx, q, opts)
		}},
		{"boolean", func(opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			return s.SearchBoolean(testCtx, dquery.NewBoolean("budget AND NOT harbor", dquery.BooleanOptions{}), opts)
		}},
	}
	pages := []struct {
		name string
		opts *dquery.BaseOptions
	}{
		{"first page", &dquery.BaseOptions{Size: 10}},
		{"after a cursor", &dquery.BaseOptions{Size: 10, Cursor: &dquery.Cursor{Score: 0.5, ID: uuid.Max}}},
	}

	var sent strings.Builder
	for _, paradigm := range paradigms {
		for _, page := range pages {
			recorder.take()
			if _, err := paradigm.search(page.opts); err != nil {
				t.Fatalf("%s, %s: %v", paradigm.name, page.name, err)
			}
			statements := recorder.take()
			if len(statements) < 2 {
				t.Fatalf("%s, %s: sent %d statements, want the count and the page", paradigm.name, page.name, len(statements))
			}
			for i, statement := range statements {
				fmt.Fprintf(&sent, "=== %s, %s, statement %d\n%s\n", paradigm.name, page.name, i+1, statement)
			}
		}
	}

	// Not under testdata, which .gitignore excludes.
	const golden = "search_sql.golden"
	if *updateGolden {
		if err := os.WriteFile(golden, []byte(sent.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v", golden, err)
	}
	if sent.String() != string(want) {
		t.Errorf("the SQL sent differs from %s; rerun with -update only if the change is meant:\n%s", golden, sent.String())
	}
}
