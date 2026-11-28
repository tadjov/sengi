package pg

import (
	"context"
	"slices"
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

			t.Run("recovers a one-edit typo in a short word, a swap of two letters included", func(t *testing.T) {
				conn := fuzzyCorpus(t, engine, map[string]int{"price": 10, "court": 10, "vote": 10, "the": 10})
				for typed, want := range map[string]string{"pirce": "price", "cuort": "court", "vite": "vote", "teh": "the"} {
					assert.Equal(t, []string{want}, expansions(t, conn, typed), "expansions of %q", typed)
				}
			})

			t.Run("counts a swap in a long word as one edit", func(t *testing.T) {
				conn := fuzzyCorpus(t, engine, map[string]int{"government": 10, "governments": 10})

				assert.Equal(t, []string{"government"}, expansions(t, conn, "govrenmnt"))
			})

			t.Run("returns no word past the edit budget", func(t *testing.T) {
				conn := fuzzyCorpus(t, engine, map[string]int{"prize": 10, "so": 10, "to": 10, "sow": 10, "hurricanexyz": 10})
				for typed, want := range map[string][]string{
					"pirce":     {},
					"so":        {"so"},
					"xo":        {},
					"hurricane": {},
				} {
					assert.Equal(t, want, expansions(t, conn, typed), "expansions of %q", typed)
				}
			})

			t.Run("keeps the 50 words with the fewest edits, then the most frequent, then by word", func(t *testing.T) {
				conn := fuzzyCorpus(t, engine, map[string]int{"hurricanezz": 1000})
				var oneEdit, twoEdits []string
				for letter := 'a'; letter <= 'z'; letter++ {
					oneEdit = append(oneEdit, "hurricane"+string(letter))
					twoEdits = append(twoEdits, "hurricane"+string(letter)+string(letter))
				}
				addWords(t, conn, slices.Concat(oneEdit, twoEdits[:25]), 1)

				want := slices.Concat(oneEdit, []string{"hurricanezz"}, twoEdits[:23])
				assert.Equal(t, want, expansions(t, conn, "hurricane"))
			})
		})
	}
}

// expansions returns the words in the order fuzzy_expansions yields them.
func expansions(t *testing.T, conn *pgx.Conn, typed string) []string {
	t.Helper()
	var words []string
	err := conn.QueryRow(context.Background(), `
		SELECT coalesce(array_agg(word ORDER BY yielded), '{}')
		FROM fuzzy_expansions($1) WITH ORDINALITY AS e (query_position, word, yielded)`, typed).Scan(&words)
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
