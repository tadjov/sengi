package testcontainer_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/storage"
	pkgtesting "github.com/tadjov/sengi/internal/testcontainer"
)

type serverBuild struct {
	ServerVersionNum string
	Trigram          string
	Fuzzystrmatch    string
}

func TestPostgresEnginesShareServerBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping testcontainer-backed test in -short mode")
	}
	ctx := context.Background()
	engines := []storage.Engine{storage.PostgresNative, storage.ParadeDB, storage.Tiger}
	builds := make(map[storage.Engine]serverBuild, len(engines))
	for _, engine := range engines {
		t.Run(string(engine), func(t *testing.T) {
			container := pkgtesting.NewPGContainerWithConfig(ctx, t, pkgtesting.PGConfig{Engine: engine})
			builds[engine] = readServerBuild(ctx, t, container.ConnString)
		})
	}

	for _, engine := range engines[1:] {
		assert.Equal(t, builds[engines[0]], builds[engine], "%s against %s", engine, engines[0])
	}
}

// readServerBuild leaves out pgvector, which ParadeDB's image ships at its own version.
func readServerBuild(ctx context.Context, t *testing.T, connString string) serverBuild {
	t.Helper()
	conn, err := pgx.Connect(ctx, connString)
	require.NoError(t, err)
	defer conn.Close(ctx)

	var build serverBuild
	err = conn.QueryRow(ctx, `
		SELECT current_setting('server_version_num'),
		       (SELECT default_version FROM pg_available_extensions WHERE name = 'pg_trgm'),
		       (SELECT default_version FROM pg_available_extensions WHERE name = 'fuzzystrmatch')`,
	).Scan(&build.ServerVersionNum, &build.Trigram, &build.Fuzzystrmatch)
	require.NoError(t, err)
	t.Logf("%+v", build)
	return build
}
