package pg

import (
	"context"
	"flag"
	"os"
	"reflect"
	"testing"

	"github.com/tadjov/sengi/internal/testcontainer"
	"github.com/testcontainers/testcontainers-go"
)

var (
	testCtx      context.Context
	testPool     *ConnectionPool
	testExecutor *RawExecutor
)

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		// These are integration tests backed by a Postgres container.
		// Skip them in -short mode (used by CI) so a Docker daemon isn't required.
		os.Exit(0)
	}

	testCtx = context.Background()

	pg, err := testcontainer.NewPGContainer(testCtx, testcontainer.PGConfig{
		Database: "news_test_db",
		Username: "test",
		Password: "test",
	})
	if err != nil {
		panic(err)
	}
	defer testcontainers.TerminateContainer(pg.Container)

	testPool, err = NewConnectionPool(testCtx, PoolConfig{ConnStr: pg.ConnString})
	if err != nil {
		panic(err)
	}
	defer testPool.Close()

	testExecutor = NewRawExecutor(testPool)

	os.Exit(m.Run())
}

func truncateTable(t *testing.T) {
	t.Helper()
	_, err := testPool.Pool().Exec(testCtx, "TRUNCATE TABLE articles CASCADE")
	if err != nil {
		t.Fatalf("failed to truncate table: %v", err)
	}
}

func TestRawExecutor_Exec(t *testing.T) {
	truncateTable(t)
	defer truncateTable(t)

	_, err := testPool.Pool().Exec(testCtx, `
		INSERT INTO articles (id, title, content, url, language, publisher_domain, description) VALUES
			(gen_random_uuid(), 'Climate Change', 'Article about climate', 'http://climate.com', 'eng', 'climate.com', ''),
			(gen_random_uuid(), 'Technology News', 'Article about tech', 'http://tech.com', 'eng', 'tech.com', ''),
			(gen_random_uuid(), 'Test Article', 'This is test content', 'http://test.com', 'eng', 'test.com', 'Test description')
	`)
	if err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	tests := []struct {
		name string
		sql  string
		args []any
		want []map[string]any
	}{
		{
			name: "no placeholders",
			sql:  "SELECT title FROM articles WHERE title @@ plainto_tsquery('english', 'test')",
			want: []map[string]any{{"title": "Test Article"}},
		},
		{
			name: "one argument",
			sql:  "SELECT title FROM articles WHERE title @@ to_tsquery('english', $1::text)",
			args: []any{"climate | change"},
			want: []map[string]any{{"title": "Climate Change"}},
		},
		{
			name: "arguments in placeholder order",
			sql:  "SELECT title FROM articles WHERE title = $1 AND publisher_domain = $2",
			args: []any{"Test Article", "test.com"},
			want: []map[string]any{{"title": "Test Article"}},
		},
		{
			name: "no matching rows",
			sql:  "SELECT title FROM articles WHERE title = $1",
			args: []any{"NonExistent"},
		},
		{
			name: "every row in query order",
			sql:  "SELECT title FROM articles ORDER BY title",
			want: []map[string]any{{"title": "Climate Change"}, {"title": "Technology News"}, {"title": "Test Article"}},
		},
		{
			name: "each selected column under its name",
			sql:  "SELECT title, content, url, publisher_domain, language, description FROM articles WHERE title = $1",
			args: []any{"Test Article"},
			want: []map[string]any{{
				"title":            "Test Article",
				"content":          "This is test content",
				"url":              "http://test.com",
				"publisher_domain": "test.com",
				"language":         "eng",
				"description":      "Test description",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := testExecutor.Exec(testCtx, tt.sql, tt.args)
			if err != nil {
				t.Fatalf("Exec: %v", err)
			}
			if len(result.Hits) != len(tt.want) || (len(tt.want) > 0 && !reflect.DeepEqual(result.Hits, tt.want)) {
				t.Errorf("hits = %v, want %v", result.Hits, tt.want)
			}
		})
	}
}
