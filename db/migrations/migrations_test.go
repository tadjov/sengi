package migrations_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/db/migrations"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/testcontainer"
)

func TestEachDownUndoesItsUp(t *testing.T) {
	for _, engine := range []storage.Engine{storage.PostgresNative, storage.ParadeDB, storage.Tiger} {
		t.Run(string(engine), func(t *testing.T) {
			conn := emptyDatabase(t, engine)
			schema, all := schemaOf(t, engine)

			preinstalled := extensions(t, conn)
			before := []catalog{catalogOf(t, conn, preinstalled)}
			for _, m := range all {
				require.NoError(t, schema.Apply(context.Background(), conn, m))
				before = append(before, catalogOf(t, conn, preinstalled))
			}
			for i := len(all) - 1; i >= 0; i-- {
				applyDown(t, conn, engine, all[i])
				assert.Equal(t, before[i], catalogOf(t, conn, preinstalled), "after the down of %s", all[i].Name)
			}
		})
	}
}

func TestFuzzyFunctionsDownKeepsTheWordList(t *testing.T) {
	for _, engine := range []storage.Engine{storage.PostgresNative, storage.Tiger} {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			conn := emptyDatabase(t, engine)
			schema, all := schemaOf(t, engine)
			require.NoError(t, schema.Up(ctx, conn))
			_, err := conn.Exec(ctx, "INSERT INTO corpus_words (word, ndoc) VALUES ('court', 5)")
			require.NoError(t, err)

			last := all[len(all)-1]
			applyDown(t, conn, engine, last)

			var expansionsExist bool
			require.NoError(t, conn.QueryRow(ctx, "SELECT to_regproc('fuzzy_expansions') IS NOT NULL").Scan(&expansionsExist))
			assert.False(t, expansionsExist, "the last migration's down leaves fuzzy_expansions")

			require.NoError(t, schema.Apply(ctx, conn, last))
			var words []string
			require.NoError(t, conn.QueryRow(ctx, "SELECT array_agg(word) FROM fuzzy_expansions('court')").Scan(&words))
			assert.Equal(t, []string{"court"}, words)
		})
	}
}

// catalog names each extension, and each public relation and function no
// extension owns.
type catalog []string

// catalogOf leaves out the ignored extensions, which a database made from
// the image's template1 already holds and a down may drop.
func catalogOf(t *testing.T, conn *pgx.Conn, ignored []string) catalog {
	t.Helper()
	var objects catalog
	err := conn.QueryRow(context.Background(), `
		SELECT coalesce(array_agg(object ORDER BY object), '{}') FROM (
			SELECT 'extension ' || extname FROM pg_extension WHERE extname <> ALL($1)
			UNION ALL
			SELECT 'relation ' || c.relname FROM pg_class c
			WHERE c.relnamespace = 'public'::regnamespace AND c.relname NOT LIKE 'schema_migrations%'
			  AND NOT EXISTS (SELECT FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
			UNION ALL
			SELECT 'function ' || p.oid::regprocedure FROM pg_proc p
			WHERE p.pronamespace = 'public'::regnamespace
			  AND NOT EXISTS (SELECT FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
		) AS objects (object)`, ignored).Scan(&objects)
	require.NoError(t, err)
	return objects
}

func extensions(t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	var names []string
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT array_agg(extname) FROM pg_extension").Scan(&names))
	return names
}

func schemaOf(t *testing.T, engine storage.Engine) (migrations.Set, []migrations.Migration) {
	t.Helper()
	schema, err := migrations.Schema(engine)
	require.NoError(t, err)
	all, err := schema.Migrations()
	require.NoError(t, err)
	return schema, all
}

func applyDown(t *testing.T, conn *pgx.Conn, engine storage.Engine, m migrations.Migration) {
	t.Helper()
	dir := os.DirFS(path.Join(string(engine), "schema"))
	down := fmt.Sprintf("%03d_%s.down.sql", m.Version, m.Name)
	body, err := fs.ReadFile(dir, down)
	require.NoError(t, err)
	_, err = conn.Exec(context.Background(), string(body))
	require.NoError(t, err, "apply %s", down)
}

func emptyDatabase(t *testing.T, engine storage.Engine) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	server := testcontainer.NewPGContainerWithConfig(ctx, t, testcontainer.PGConfig{Engine: engine, Unmigrated: true})
	conn, err := pgx.Connect(ctx, server.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(ctx) })
	return conn
}
