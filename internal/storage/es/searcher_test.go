package es

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/api/dto"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/storagetest"
	pkgtesting "github.com/tadjov/sengi/internal/testcontainer"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
	dquery "github.com/tadjov/sengi/internal/types/query"
)

type searchMethod struct {
	name   string
	search func(ctx context.Context, s *Searcher, term string, opts *dquery.BaseOptions) (*storage.SearchResult, error)
}

var searchMethods = []searchMethod{
	{
		name: "SearchStringQuery",
		search: func(ctx context.Context, s *Searcher, term string, opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			return s.SearchStringQuery(ctx, dquery.NewString(term, dquery.StringOptions{}), opts)
		},
	},
	{
		name: "SearchField",
		search: func(ctx context.Context, s *Searcher, term string, opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			match, err := dquery.NewMatch(dquery.FieldTitle, term, dquery.MatchOptions{})
			if err != nil {
				return nil, err
			}
			return s.SearchField(ctx, match, opts)
		},
	},
	{
		name: "SearchFields",
		search: func(ctx context.Context, s *Searcher, term string, opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			q, err := dquery.NewMultiMatch(term, []string{"title", "content"}, dquery.MultiMatchOptions{})
			if err != nil {
				return nil, err
			}
			return s.SearchFields(ctx, q, opts)
		},
	},
	{
		name: "SearchPhrase",
		search: func(ctx context.Context, s *Searcher, term string, opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			q, err := dquery.NewPhrase(term, []dquery.Field{dquery.FieldTitle}, dquery.PhraseOptions{})
			if err != nil {
				return nil, err
			}
			return s.SearchPhrase(ctx, q, opts)
		},
	},
	{
		name: "SearchBoolean",
		search: func(ctx context.Context, s *Searcher, term string, opts *dquery.BaseOptions) (*storage.SearchResult, error) {
			return s.SearchBoolean(ctx, dquery.NewBoolean(term, dquery.BooleanOptions{}), opts)
		},
	},
}

func newSearcherTestEnv(t *testing.T) (ClientConfig, *Searcher) {
	t.Helper()

	container := pkgtesting.NewESContainer(context.Background(), t)
	cfg := ClientConfig{Addresses: []string{container.Address}, IndexName: "articles_searcher_test"}
	return cfg, NewSearcher(newTestClient(t, cfg))
}

func TestSearcher_NoMatchReturnsEmptyPage(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker (testcontainers ES)")
	}
	ctx := context.Background()
	cfg, searcher := newSearcherTestEnv(t)
	seedIndex(t, cfg, titledArticles("climate change policy", "unrelated cooking recipe"), nil)

	for _, method := range searchMethods {
		t.Run(method.name, func(t *testing.T) {
			res, err := method.search(ctx, searcher, "asdfqwerzxcv", &dquery.BaseOptions{Size: 10})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(res.Hits) != 0 {
				t.Errorf("hits = %d, want none", len(res.Hits))
			}
			if res.HasMore {
				t.Error("HasMore = true on an empty page")
			}
			if res.NextCursor != nil {
				t.Errorf("NextCursor = %+v, want nil", res.NextCursor)
			}
			if res.MaxScore != 0 || res.PageMaxScore != 0 {
				t.Errorf("MaxScore = %f, PageMaxScore = %f, want both 0", res.MaxScore, res.PageMaxScore)
			}
		})
	}
}

func TestSearcher_MatchPagesThroughEveryHit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker (testcontainers ES)")
	}
	ctx := context.Background()
	cfg, searcher := newSearcherTestEnv(t)
	// Different title lengths give distinct BM25 scores, so a page's first and
	// last hit differ and the cursor and PageMaxScore can be told apart.
	articles := titledArticles(
		"climate",
		"climate change",
		"climate change policy",
		"climate change policy debate review",
		"unrelated cooking recipe",
	)
	seedIndex(t, cfg, articles, nil)
	var matching []uuid.UUID
	for _, article := range articles[:4] {
		matching = append(matching, article.ID)
	}

	const pageSize = 2
	for _, method := range searchMethods {
		t.Run(method.name, func(t *testing.T) {
			seen := map[uuid.UUID]bool{}
			opts := &dquery.BaseOptions{Size: pageSize}
			for page := 1; ; page++ {
				if page > len(matching)+2 {
					t.Fatalf("still paging after %d pages", page-1)
				}
				res, err := method.search(ctx, searcher, "climate", opts)
				if err != nil {
					t.Fatalf("page %d: %v", page, err)
				}
				if len(res.Hits) == 0 {
					t.Fatalf("page %d is empty", page)
				}

				bestScore, bestNormalized := 0.0, 0.0
				for _, hit := range res.Hits {
					if seen[hit.Article.ID] {
						t.Errorf("page %d repeats article %s", page, hit.Article.ID)
					}
					seen[hit.Article.ID] = true
					if hit.ScoreNormalized <= 0 || hit.ScoreNormalized > 1 {
						t.Errorf("page %d: ScoreNormalized = %f, want (0, 1]", page, hit.ScoreNormalized)
					}
					bestScore = max(bestScore, hit.Score)
					bestNormalized = max(bestNormalized, hit.ScoreNormalized)
				}
				if math.Abs(res.PageMaxScore-bestScore) > 1e-3 {
					t.Errorf("page %d: PageMaxScore = %f, want the page's best score %f", page, res.PageMaxScore, bestScore)
				}
				if page == 1 && math.Abs(bestNormalized-1) > 1e-9 {
					t.Errorf("first page best ScoreNormalized = %f, want 1", bestNormalized)
				}

				if !res.HasMore {
					if res.NextCursor != nil {
						t.Errorf("last page carries NextCursor %+v", res.NextCursor)
					}
					break
				}
				if res.NextCursor == nil {
					t.Fatalf("page %d: HasMore without a NextCursor", page)
				}
				opts = &dquery.BaseOptions{Size: pageSize, Cursor: res.NextCursor}
			}

			for _, id := range matching {
				if !seen[id] {
					t.Errorf("matching article %s never returned", id)
				}
			}
		})
	}
}

func TestSearcher_NonUUIDDocumentIDReturnsError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker (testcontainers ES)")
	}
	ctx := context.Background()
	cfg, searcher := newSearcherTestEnv(t)
	createEmptyIndex(t, cfg)

	doc := map[string]any{"id": "not-a-uuid", "title": "climate change policy", "language": "eng"}
	if _, err := newTestClient(t, cfg).typed.Index(cfg.IndexName).Id("not-a-uuid").Document(doc).Do(ctx); err != nil {
		t.Fatalf("index raw document: %v", err)
	}
	refreshIndex(t, cfg)

	res, err := searcher.SearchStringQuery(ctx, &dquery.String{Query: "climate"}, &dquery.BaseOptions{Size: 10})
	if err == nil {
		t.Fatalf("search returned %+v, want an error for the non-UUID id", res)
	}
	if !strings.Contains(err.Error(), "not-a-uuid") {
		t.Errorf("error = %v, want it to name the non-UUID id", err)
	}
}

func TestSearcher_Parity(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker (testcontainers ES)")
	}
	cfg, searcher := newSearcherTestEnv(t)
	seedIndex(t, cfg, storagetest.ParityCorpus(), nil)

	storagetest.RunParity(t, storagetest.Elasticsearch, searcher)
	storagetest.RunFieldIsolation(t, searcher)
}

func TestSearcher_TotalMatchesIsExactPastTenThousand(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker (testcontainers ES)")
	}
	ctx := context.Background()
	cfg, searcher := newSearcherTestEnv(t)

	// Elasticsearch stops counting at 10,000 unless asked to track every hit.
	const matching = 10_050
	articles := make([]document.Article, matching)
	for i := range articles {
		articles[i] = document.Article{ID: uuid.New(), Title: "climate", Language: language.English}
	}
	seedIndex(t, cfg, articles, nil)

	for _, method := range searchMethods {
		t.Run(method.name, func(t *testing.T) {
			res, err := method.search(ctx, searcher, "climate", &dquery.BaseOptions{Size: 10})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if res.TotalMatches != matching {
				t.Errorf("TotalMatches = %d, want %d", res.TotalMatches, matching)
			}
		})
	}
}

func TestSearcher_ReturnsEveryArticleField(t *testing.T) {
	ctx := context.Background()
	cfg, searcher := newSearcherTestEnv(t)

	published := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)
	modified := time.Date(2019, 3, 5, 0, 0, 0, 0, time.UTC)
	full := document.Article{
		ID:              uuid.MustParse("6f1c0d53-1b0e-5a51-9a2b-7d0f6c3e8a01"),
		URL:             "https://uk.reuters.com/world/storm",
		Title:           "Storm hits the coast",
		Description:     "Thousands without power",
		Content:         "The storm made landfall overnight.",
		PublishedAt:     published,
		Language:        language.English,
		PublisherDomain: "reuters.com",
		Metadata: document.Metadata{
			ExternalID:    "reuters-0412",
			PublisherName: "Reuters",
			Authors:       []string{"Jane Doe", "John Roe"},
			Section:       "World",
			Keywords:      []string{"storm", "coast"},
			Image:         "https://uk.reuters.com/storm.jpg",
			DateModified:  modified,
			Extra:         map[string]any{"wordCount": "412"},
		},
	}
	seedIndex(t, cfg, []document.Article{full}, nil)

	res, err := searcher.SearchStringQuery(ctx, &dquery.String{Query: "storm"}, &dquery.BaseOptions{Size: 10})
	require.NoError(t, err)
	require.Len(t, res.Hits, 1)
	assert.Equal(t, dto.Article{
		ID:              full.ID,
		URL:             full.URL,
		Title:           full.Title,
		Description:     full.Description,
		Content:         full.Content,
		PublishedAt:     &published,
		Language:        "eng",
		PublisherDomain: "reuters.com",
		Metadata: dto.Metadata{
			ExternalID:    "reuters-0412",
			PublisherName: "Reuters",
			Authors:       []string{"Jane Doe", "John Roe"},
			Section:       "World",
			Keywords:      []string{"storm", "coast"},
			Image:         "https://uk.reuters.com/storm.jpg",
			DateModified:  &modified,
			Extra:         map[string]any{"wordCount": "412"},
		},
	}, res.Hits[0].Article)
}
