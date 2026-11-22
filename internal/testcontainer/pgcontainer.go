package testcontainer

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/tadjov/sengi/db/migrations"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

type PGContainer struct {
	Container  testcontainers.Container
	ConnString string
}

// PGConfig defaults Engine to storage.PostgresNative and migrates unless Unmigrated is set.
type PGConfig struct {
	// Database, Username and Password apply to NewPGContainer only.
	Database   string
	Username   string
	Password   string
	Engine     storage.Engine
	Unmigrated bool
	// Isolated starts a server for this test alone, for a test that stops or
	// reconfigures it.
	Isolated bool
	// ServerFlags follow the engine's own server-start flags, e.g. "-c", "work_mem=8MB".
	ServerFlags []string
}

// engineImages are the digest-pinned images docker-compose.yml runs.
var engineImages = map[storage.Engine]string{
	storage.PostgresNative: "pgvector/pgvector:0.8.7-pg18-bookworm@sha256:38681dc6eaf55308e2a4c6de392d131652dcfac70e1263e35cd9b5ea3fe39782",
	storage.ParadeDB:       "paradedb/paradedb:0.26.8-pg18@sha256:97a698fdf5dbd1748052f60eaf19befb716fe6313c990d91b365715e3d6532d9",
	storage.Tiger:          "timescale/timescaledb-ha:pg18.7-ts2.32.1-r606.1@sha256:ff4362ab6890052930ef75a72d94b7072fcc510bc79c84722700c1b2b68f3474",
}

var serverFlags = []string{
	"-c", "shared_buffers=1536MB", "-c", "effective_cache_size=4608MB",
	"-c", "maintenance_work_mem=4GB", "-c", "max_parallel_maintenance_workers=3",
	"-c", "random_page_cost=1.1", "-c", "effective_io_concurrency=200",
	"-c", "max_wal_size=16GB", "-c", "checkpoint_timeout=30min",
}

// engineServerFlags mirrors the server-start settings in docker-compose.yml.
var engineServerFlags = map[storage.Engine][]string{
	storage.PostgresNative: serverFlags,
	storage.ParadeDB: append(slices.Clone(serverFlags),
		"-c", "work_mem=4MB", "-c", "max_parallel_workers_per_gather=2"),
	storage.Tiger: append(slices.Clone(serverFlags),
		"-c", "work_mem=4MB", "-c", "jit=on", "-c", "max_parallel_workers_per_gather=2", "-c", "default_toast_compression=pglz",
		"-c", "pg_textsearch.memory_limit=2GB"),
}

// NewPGContainer starts a server of its own, which the caller terminates.
func NewPGContainer(ctx context.Context, cfg PGConfig) (*PGContainer, error) {
	return createPGContainer(ctx, cfg)
}

func NewPGContainerWithCleanup(ctx context.Context, tb testing.TB) *PGContainer {
	tb.Helper()
	return NewPGContainerWithConfig(ctx, tb, PGConfig{})
}

// NewPGContainerWithConfig gives the test a database of its own, dropped when
// the test ends. The server is shared by every test of the process that asks
// for the same engine and flags, unless cfg.Isolated starts one for this test.
func NewPGContainerWithConfig(ctx context.Context, tb testing.TB, cfg PGConfig) *PGContainer {
	tb.Helper()
	if testing.Short() {
		tb.Skip("skipping testcontainer-backed test in -short mode")
	}
	if cfg.Engine == "" {
		cfg.Engine = storage.PostgresNative
	}
	if cfg.Isolated {
		return isolatedPGContainer(ctx, tb, cfg)
	}

	server, err := sharedPGServer(ctx, cfg)
	if err != nil {
		tb.Fatalf("failed to start the shared %s server: %v", cfg.Engine, err)
	}
	database, err := server.createDatabase(ctx, cfg)
	if err != nil {
		tb.Fatalf("failed to create a test database on %s: %v", cfg.Engine, err)
	}
	tb.Cleanup(func() {
		if err := server.dropDatabase(context.WithoutCancel(ctx), database); err != nil {
			tb.Logf("failed to drop test database %s: %v", database, err)
		}
	})
	return &PGContainer{Container: server.container.Container, ConnString: server.connString(database)}
}

func isolatedPGContainer(ctx context.Context, tb testing.TB, cfg PGConfig) *PGContainer {
	tb.Helper()
	cfg.Database, cfg.Username, cfg.Password = adminDatabase, testUser, testUser
	container, err := createPGContainer(ctx, cfg)
	if err != nil {
		tb.Fatalf("failed to create postgres container: %v", err)
	}
	tb.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container.Container); err != nil {
			tb.Logf("failed to terminate postgres container: %v", err)
		}
	})
	return container
}

const (
	adminDatabase = "news_test_db"
	testUser      = "test"
)

// sharedServer is one container the tests of a process create databases on.
// It is never terminated here: the testcontainers reaper removes it when the
// test process exits.
type sharedServer struct {
	container *PGContainer
	databases atomic.Int64
}

var (
	sharedServersMu sync.Mutex
	sharedServers   = map[string]*sharedServer{}
)

func sharedPGServer(ctx context.Context, cfg PGConfig) (*sharedServer, error) {
	key := string(cfg.Engine) + "\x00" + strings.Join(cfg.ServerFlags, "\x00")
	sharedServersMu.Lock()
	defer sharedServersMu.Unlock()
	if server, ok := sharedServers[key]; ok {
		return server, nil
	}
	container, err := createPGContainer(context.WithoutCancel(ctx), PGConfig{
		Database: adminDatabase, Username: testUser, Password: testUser,
		Engine: cfg.Engine, ServerFlags: cfg.ServerFlags, Unmigrated: true,
	})
	if err != nil {
		return nil, err
	}
	server := &sharedServer{container: container}
	sharedServers[key] = server
	return server, nil
}

func (s *sharedServer) connString(database string) string {
	u, err := url.Parse(s.container.ConnString)
	if err != nil {
		panic(fmt.Sprintf("testcontainer: connection string %q: %v", s.container.ConnString, err))
	}
	u.Path = "/" + database
	return u.String()
}

func (s *sharedServer) createDatabase(ctx context.Context, cfg PGConfig) (string, error) {
	database := fmt.Sprintf("test_%d", s.databases.Add(1))
	admin, err := pgx.Connect(ctx, s.container.ConnString)
	if err != nil {
		return "", fmt.Errorf("connect to create %s: %w", database, err)
	}
	defer admin.Close(context.WithoutCancel(ctx))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		return "", fmt.Errorf("create %s: %w", database, err)
	}
	if cfg.Unmigrated {
		return database, nil
	}
	return database, migrate(ctx, s.connString(database), cfg.Engine)
}

func (s *sharedServer) dropDatabase(ctx context.Context, database string) error {
	admin, err := pgx.Connect(ctx, s.container.ConnString)
	if err != nil {
		return err
	}
	defer admin.Close(ctx)
	_, err = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)")
	return err
}

func createPGContainer(ctx context.Context, cfg PGConfig) (*PGContainer, error) {
	if cfg.Engine == "" {
		cfg.Engine = storage.PostgresNative
	}
	image, ok := engineImages[cfg.Engine]
	if !ok {
		return nil, fmt.Errorf("no postgres image for engine %q", cfg.Engine)
	}

	pgContainer, err := postgres.Run(ctx,
		image,
		postgres.WithDatabase(cfg.Database),
		postgres.WithUsername(cfg.Username),
		postgres.WithPassword(cfg.Password),
		testcontainers.WithCmdArgs(append(slices.Clone(engineServerFlags[cfg.Engine]), cfg.ServerFlags...)...),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		_ = testcontainers.TerminateContainer(pgContainer)
		return nil, fmt.Errorf("failed to start %s container: %w", cfg.Engine, err)
	}

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err == nil && !cfg.Unmigrated {
		err = migrate(ctx, connStr, cfg.Engine)
	}
	if err != nil {
		_ = testcontainers.TerminateContainer(pgContainer)
		return nil, err
	}

	return &PGContainer{
		Container:  pgContainer,
		ConnString: connStr,
	}, nil
}

func migrate(ctx context.Context, connStr string, engine storage.Engine) error {
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return fmt.Errorf("connect to migrate %s: %w", engine, err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	return migrations.Build(ctx, conn, engine)
}
