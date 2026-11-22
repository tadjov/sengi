package pg

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/testcontainer"
)

func TestFuzzyExpansion(t *testing.T) {
	for _, engine := range []storage.Engine{storage.PostgresNative, storage.Tiger} {
		t.Run(string(engine), func(t *testing.T) {
			conn := fuzzyCorpus(t, engine, map[string]int{
				"government": 900, "goverment": 3, "governments": 50, "shutdown": 40,
				"policy": 70, "police": 80, "so": 500, "to": 1000,
			})

			t.Run("expands each word to the corpus words within its edit budget", func(t *testing.T) {
				for _, tc := range []struct {
					typed string
					want  []string
				}{
					{"goverment shutdwon", []string{"goverment", "government", "governments", "shutdown"}},
					{"polcy", []string{"policy"}},
					{"so", []string{"so"}},
					{"Goverment!", []string{"goverment", "government", "governments"}},
				} {
					assert.ElementsMatch(t, tc.want, expansions(t, conn, tc.typed), "expansions of %q", tc.typed)
				}
			})

			switch engine {
			case storage.PostgresNative:
				t.Run("fuzzy_tsquery ANDs the words and ORs each word's expansions", func(t *testing.T) {
					for text, want := range map[string]bool{
						"the government shutdown": true,
						"governments shut down":   false,
						"the goverment budget":    false,
					} {
						var matches bool
						err := conn.QueryRow(context.Background(),
							"SELECT to_tsvector('english', $1) @@ fuzzy_tsquery('goverment shutdwon')", text).Scan(&matches)
						require.NoError(t, err)
						assert.Equal(t, want, matches, "%q matches goverment shutdwon", text)
					}
				})
			case storage.Tiger:
				t.Run("fuzzy_words carries every expansion as one any-word query", func(t *testing.T) {
					var words string
					err := conn.QueryRow(context.Background(), "SELECT fuzzy_words('goverment shutdwon')").Scan(&words)
					require.NoError(t, err)
					assert.ElementsMatch(t, expansions(t, conn, "goverment shutdwon"), strings.Fields(words))
				})
			}

			t.Run("keeps the 50 nearest corpus words of a word", func(t *testing.T) {
				var nearest, further []string
				for letter := 'a'; letter <= 'z'; letter++ {
					nearest = append(nearest, "hurricane"+string(letter))
					further = append(further, "hurricane"+string(letter)+string(letter))
				}
				addWords(t, conn, append(nearest, further...), 1)

				got := expansions(t, conn, "hurricane")
				assert.Len(t, got, 50)
				assert.Subset(t, got, nearest)
			})
		})
	}
}

func expansions(t *testing.T, conn *pgx.Conn, typed string) []string {
	t.Helper()
	var words []string
	err := conn.QueryRow(context.Background(),
		"SELECT coalesce(array_agg(word), '{}') FROM fuzzy_expansions($1)", typed).Scan(&words)
	require.NoError(t, err)
	return words
}

func fuzzyCorpus(t *testing.T, engine storage.Engine, words map[string]int) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	server := testcontainer.NewPGContainerWithConfig(ctx, t, testcontainer.PGConfig{Engine: engine})
	conn, err := pgx.Connect(ctx, server.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(ctx) })
	for word, ndoc := range words {
		addWords(t, conn, []string{word}, ndoc)
	}
	return conn
}

func addWords(t *testing.T, conn *pgx.Conn, words []string, ndoc int) {
	t.Helper()
	for _, word := range words {
		_, err := conn.Exec(context.Background(), "INSERT INTO corpus_words (word, ndoc) VALUES ($1, $2)", word, ndoc)
		require.NoError(t, err, "insert %q", word)
	}
}
