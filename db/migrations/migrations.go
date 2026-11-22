// Package migrations holds each Postgres engine's schema, in golang-migrate's format, and its search indexes.
package migrations

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/tadjov/sengi/internal/storage"
)

//go:embed native paradedb tiger
var files embed.FS

const (
	schemaFile    = "001_schema.up.sql"
	schemaVersion = 1
)

// Schema returns the DDL of the engine's schema.
func Schema(e storage.Engine) (string, error) {
	if e.Type() != storage.PG {
		return "", fmt.Errorf("engine %q has no Postgres migrations", e)
	}
	body, err := fs.ReadFile(files, path.Join(string(e), "schema", schemaFile))
	if err != nil {
		return "", fmt.Errorf("read the schema of %s: %w", e, err)
	}
	return string(body), nil
}

// Up runs schema unless schema_migrations already records it, then records it in golang-migrate's format.
func Up(ctx context.Context, conn *pgx.Conn, schema string) error {
	applied, err := schemaApplied(ctx, conn)
	if err != nil || applied {
		return err
	}
	if _, err := conn.Exec(ctx, schema); err != nil {
		return fmt.Errorf("apply %s: %w", schemaFile, err)
	}
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			CREATE TABLE IF NOT EXISTS schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL);
			TRUNCATE schema_migrations;
			INSERT INTO schema_migrations (version, dirty) VALUES (`+strconv.Itoa(schemaVersion)+`, false)`)
		return err
	})
	if err != nil {
		return fmt.Errorf("record schema version %d: %w", schemaVersion, err)
	}
	return nil
}

func schemaApplied(ctx context.Context, conn *pgx.Conn) (bool, error) {
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return false, fmt.Errorf("look up schema_migrations: %w", err)
	}
	if !exists {
		return false, nil
	}
	var applied bool
	err := conn.QueryRow(ctx, "SELECT version = $1 AND NOT dirty FROM schema_migrations LIMIT 1", int64(schemaVersion)).Scan(&applied)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read schema_migrations: %w", err)
	}
	return applied, nil
}

// IndexStatement is one statement of an engine's indexes.sql. Stage names the
// timed ones: the index a CREATE INDEX builds, or fill_<table> for an INSERT
// that fills a table derived from the corpus. Index is set for an index only.
type IndexStatement struct {
	Stage string
	Index string
	SQL   string
}

func (s IndexStatement) String() string {
	if s.Stage != "" {
		return s.Stage
	}
	return s.SQL
}

var (
	createIndex = regexp.MustCompile(`(?i)^CREATE\s+INDEX\s+(\w+)`)
	fillTable   = regexp.MustCompile(`(?i)^INSERT\s+INTO\s+(\w+)`)
)

// SearchIndexes splits indexes.sql on ';', which no statement in it contains.
func SearchIndexes(e storage.Engine) ([]IndexStatement, error) {
	if e.Type() != storage.PG {
		return nil, fmt.Errorf("engine %q has no Postgres search indexes", e)
	}
	body, err := fs.ReadFile(files, path.Join(string(e), "indexes.sql"))
	if err != nil {
		return nil, fmt.Errorf("read the search indexes of %s: %w", e, err)
	}
	var statements []IndexStatement
	for _, sql := range strings.Split(string(body), ";") {
		sql = strings.TrimSpace(sql)
		if sql == "" {
			continue
		}
		statement := IndexStatement{SQL: sql}
		if match := createIndex.FindStringSubmatch(sql); match != nil {
			statement.Stage, statement.Index = match[1], match[1]
		} else if match := fillTable.FindStringSubmatch(sql); match != nil {
			statement.Stage = "fill_" + match[1]
		}
		statements = append(statements, statement)
	}
	return statements, nil
}

// Build migrates the schema and creates every search index, untimed, as a database without a load needs.
func Build(ctx context.Context, conn *pgx.Conn, e storage.Engine) error {
	schema, err := Schema(e)
	if err != nil {
		return err
	}
	if err := Up(ctx, conn, schema); err != nil {
		return fmt.Errorf("migrate the schema of %s: %w", e, err)
	}
	indexes, err := SearchIndexes(e)
	if err != nil {
		return err
	}
	for _, s := range indexes {
		if _, err := conn.Exec(ctx, s.SQL); err != nil {
			return fmt.Errorf("run %s on %s: %w", s, e, err)
		}
	}
	return nil
}
