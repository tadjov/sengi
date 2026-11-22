// Package pgtest seeds Postgres databases for tests.
package pgtest

import (
	"context"
	"fmt"
	"iter"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/pg"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
)

var databases atomic.Int64

type Corpus struct {
	Articles []document.Article
	// Vectors follow Articles order and are all stored under ModelTag.
	Vectors  []embedding.Vec
	ModelTag string
}

// Load loads c into a new native database on the server, the way datapipe load does, and returns its connection string.
func Load(tb testing.TB, serverConnString string, c Corpus) string {
	tb.Helper()
	ctx := context.Background()
	connString := createDatabase(ctx, tb, serverConnString)

	pool, err := pg.NewConnectionPool(ctx, pg.PoolConfig{ConnStr: connString})
	if err != nil {
		tb.Fatalf("connect to %s: %v", connString, err)
	}
	defer pool.Close()
	loader, err := pg.NewCorpusLoader(pool, storage.PostgresNative)
	if err != nil {
		tb.Fatalf("NewCorpusLoader: %v", err)
	}

	corpus := storage.Corpus{Articles: seq(c.Articles), Language: language.English}
	if c.Vectors != nil {
		corpus.Vectors = seq(c.Vectors)
		corpus.ModelTag = c.ModelTag
		corpus.Embeddings = &storage.PackageRef{Name: "test-embeddings"}
	}
	opts := storage.LoadOptions{BatchSize: max(len(c.Articles), 1), Concurrency: 1}
	if _, err := loader.LoadCorpus(ctx, corpus, opts); err != nil {
		tb.Fatalf("LoadCorpus: %v", err)
	}
	return connString
}

func createDatabase(ctx context.Context, tb testing.TB, serverConnString string) string {
	tb.Helper()
	conn, err := pgx.Connect(ctx, serverConnString)
	if err != nil {
		tb.Fatalf("connect to %s: %v", serverConnString, err)
	}
	defer conn.Close(ctx)

	name := fmt.Sprintf("seeded_%d", databases.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		tb.Fatalf("create database %s: %v", name, err)
	}
	u, err := url.Parse(serverConnString)
	if err != nil {
		tb.Fatalf("parse %s: %v", serverConnString, err)
	}
	u.Path = "/" + name
	return u.String()
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
