package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/tadjov/sengi/internal/storage"
)

// searchExtensions are the extensions whose version changes what a search
// query computes; utility extensions such as uuid-ossp are left out.
var searchExtensions = []string{"vector", "pg_search", "pg_textsearch", "pg_trgm", "fuzzystrmatch"}

var describedTables = []string{"articles", "article_embeddings"}

// IndexDescription is how a Postgres engine had its corpus indexed and what
// it ran with, read from the live server.
type IndexDescription struct {
	// ServerVersion is version(), which names the build and platform too.
	ServerVersion string `json:"server_version"`
	// Extensions are every installed extension.
	Extensions []Extension       `json:"extensions,omitempty"`
	Indexes    []IndexDefinition `json:"indexes,omitempty"`
	// Settings holds the live value of every connection setting the pool
	// applied, keyed by setting name, as SHOW reads it.
	Settings map[string]string `json:"settings,omitempty"`
	// ServerSettings are the settings off their default that the session did
	// not set: what the server was started and configured with.
	ServerSettings []ServerSetting `json:"server_settings,omitempty"`
	// Limits is nil when the server could not read them; Unreadable says why.
	Limits *CgroupLimits `json:"cgroup_limits,omitempty"`
	// Load is nil when corpus_load holds no record.
	Load *storage.LoadRecord `json:"load,omitempty"`
	// Unreadable lists the server reads that failed, each with its error; the
	// rest of the description stands.
	Unreadable []string `json:"unreadable,omitempty"`
}

// ServerSetting is one pg_settings row; Source is where the value came from,
// such as "command line" or "configuration file".
type ServerSetting struct {
	Name    string `json:"name"`
	Setting string `json:"setting"`
	Unit    string `json:"unit,omitempty"`
	Source  string `json:"source"`
}

// CgroupLimits are the cgroup v2 cpu.max and memory.max the server reads from
// inside its container.
type CgroupLimits struct {
	CPUMax    string `json:"cpu_max"`
	MemoryMax string `json:"memory_max"`
}

type Extension struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type IndexDefinition struct {
	Table      string   `json:"table"`
	Name       string   `json:"name"`
	Definition string   `json:"definition"`
	Options    []string `json:"reloptions,omitempty"`
}

// DescribeIndexes reads the server version, the installed extensions, every
// index on the articles and article_embeddings tables, the session value of
// each setting the pool applies on connect, the server's own settings and
// cgroup limits, and the latest corpus load record. A failed read of the
// server's settings or limits is recorded in Unreadable, not returned.
func DescribeIndexes(ctx context.Context, pool *ConnectionPool) (*IndexDescription, error) {
	conn, err := pool.conn.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	var description IndexDescription
	if err := conn.QueryRow(ctx, "SELECT version()").Scan(&description.ServerVersion); err != nil {
		return nil, fmt.Errorf("read server version: %w", err)
	}
	if description.Extensions, err = readExtensions(ctx, conn.Conn()); err != nil {
		return nil, err
	}
	if description.Indexes, err = readIndexDefinitions(ctx, conn.Conn()); err != nil {
		return nil, err
	}
	if description.Settings, err = readSessionSettings(ctx, conn.Conn(), sortedKeys(pool.settings)); err != nil {
		return nil, err
	}
	if description.Load, err = readLatestLoad(ctx, conn.Conn()); err != nil {
		return nil, err
	}
	if description.ServerSettings, err = readServerSettings(ctx, conn.Conn()); err != nil {
		description.Unreadable = append(description.Unreadable, err.Error())
	}
	if description.Limits, err = readCgroupLimits(ctx, conn.Conn()); err != nil {
		description.Unreadable = append(description.Unreadable, err.Error())
	}
	return &description, nil
}

// readServerSettings leaves out the session's rows: they are the connection
// settings, which Settings records as SHOW reads them.
func readServerSettings(ctx context.Context, conn *pgx.Conn) ([]ServerSetting, error) {
	rows, err := conn.Query(ctx, `
		SELECT name, setting, coalesce(unit, ''), source
		FROM pg_settings
		WHERE source NOT IN ('default', 'session')
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read server settings: %w", err)
	}
	settings, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ServerSetting])
	if err != nil {
		return nil, fmt.Errorf("read server settings: %w", err)
	}
	return settings, nil
}

// readCgroupLimits needs a role that may read server files, such as a
// superuser or a member of pg_read_server_files.
func readCgroupLimits(ctx context.Context, conn *pgx.Conn) (*CgroupLimits, error) {
	var limits CgroupLimits
	err := conn.QueryRow(ctx,
		"SELECT pg_read_file('/sys/fs/cgroup/cpu.max'), pg_read_file('/sys/fs/cgroup/memory.max')").
		Scan(&limits.CPUMax, &limits.MemoryMax)
	if err != nil {
		return nil, fmt.Errorf("read cgroup limits: %w", err)
	}
	limits.CPUMax = strings.TrimSpace(limits.CPUMax)
	limits.MemoryMax = strings.TrimSpace(limits.MemoryMax)
	return &limits, nil
}

// readLatestLoad returns nil on a schema older than corpus_load.
func readLatestLoad(ctx context.Context, conn *pgx.Conn) (*storage.LoadRecord, error) {
	var hasTable bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('corpus_load') IS NOT NULL").Scan(&hasTable); err != nil {
		return nil, fmt.Errorf("look up corpus_load: %w", err)
	}
	if !hasTable {
		return nil, nil
	}
	var raw []byte
	err := conn.QueryRow(ctx, "SELECT record FROM corpus_load ORDER BY finished_at DESC LIMIT 1").Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read latest load record: %w", err)
	}
	var record storage.LoadRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("decode latest load record: %w", err)
	}
	return &record, nil
}

func readExtensions(ctx context.Context, conn *pgx.Conn) ([]Extension, error) {
	return collectExtensions(conn.Query(ctx, "SELECT extname, extversion FROM pg_extension ORDER BY extname"))
}

func readSearchExtensions(ctx context.Context, conn *pgx.Conn) ([]Extension, error) {
	return collectExtensions(conn.Query(ctx,
		"SELECT extname, extversion FROM pg_extension WHERE extname = ANY($1) ORDER BY extname",
		searchExtensions))
}

func collectExtensions(rows pgx.Rows, err error) ([]Extension, error) {
	if err != nil {
		return nil, fmt.Errorf("read extensions: %w", err)
	}
	extensions, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Extension])
	if err != nil {
		return nil, fmt.Errorf("read extensions: %w", err)
	}
	return extensions, nil
}

// readIndexDefinitions resolves the tables through the search path, the same
// way the benchmark queries do.
func readIndexDefinitions(ctx context.Context, conn *pgx.Conn) ([]IndexDefinition, error) {
	rows, err := conn.Query(ctx, `
		SELECT t.relname, i.relname, pg_get_indexdef(i.oid), coalesce(i.reloptions, '{}')
		FROM pg_index x
		JOIN pg_class t ON t.oid = x.indrelid
		JOIN pg_class i ON i.oid = x.indexrelid
		WHERE t.oid IN (SELECT to_regclass(name) FROM unnest($1::text[]) AS name)
		ORDER BY t.relname, i.relname`,
		describedTables)
	if err != nil {
		return nil, fmt.Errorf("read index definitions: %w", err)
	}
	indexes, err := pgx.CollectRows(rows, pgx.RowToStructByPos[IndexDefinition])
	if err != nil {
		return nil, fmt.Errorf("read index definitions: %w", err)
	}
	return indexes, nil
}

func readSessionSettings(ctx context.Context, conn *pgx.Conn, names []string) (map[string]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	settings := make(map[string]string, len(names))
	for _, name := range names {
		var value *string
		if err := conn.QueryRow(ctx, "SELECT current_setting($1, true)", name).Scan(&value); err != nil {
			return nil, fmt.Errorf("read setting %s: %w", name, err)
		}
		if value != nil {
			settings[name] = *value
		}
	}
	return settings, nil
}
