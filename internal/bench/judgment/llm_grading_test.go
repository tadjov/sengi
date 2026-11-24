package judgment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/bench/suite"
	"github.com/tadjov/sengi/internal/types/document"
)

// The model grades the query a user would type, exactly as stored.
func TestLLMJudge_PromptCarriesTheQueryText(t *testing.T) {
	q := GradingQuery{ID: "q1", Text: `"climate change" summit`}
	provider := &fakeLLMProvider{grade: func(uuid.UUID) int { return 2 }}
	s := &LLMStrategy{provider: provider}

	_, err := s.Grade(context.Background(), q, GradingDoc{ID: uuid.New(), Title: "Climate change summit opens"})
	require.NoError(t, err)

	require.Len(t, provider.requests(), 1)
	assert.Contains(t, provider.requests()[0].User, "Query: "+q.Text)
}

// Phrase, boolean and filter constraints are decided in code: a doc that
// breaks one grades 0 without a request, whatever the model would say, and
// the model grades only the docs that keep it.
func TestLLMJudge_GradesADocBreakingTheQuerysConstraintZeroWithoutAsking(t *testing.T) {
	tests := []struct {
		name     string
		entry    pool.PoolEntry
		admitted document.Article
		broken   document.Article
	}{
		{
			name:     "phrase",
			entry:    pool.PoolEntry{QueryText: "solar power", Category: suite.CategoryPhrase},
			admitted: llmArticle("Solar power plant opens", "reuters.com"),
			broken:   llmArticle("Power prices squeeze solar firms", "reuters.com"),
		},
		{
			name:     "not",
			entry:    pool.PoolEntry{QueryText: "climate AND NOT politics", Category: suite.CategoryBoolean},
			admitted: llmArticle("Climate scientists publish ice data", "reuters.com"),
			broken:   llmArticle("Climate politics heats up in parliament", "reuters.com"),
		},
		{
			name:     "declared filter",
			entry:    pool.PoolEntry{QueryText: "climate", Category: suite.CategoryKeyword, Filters: suite.Filters{PublisherDomain: "reuters.com"}},
			admitted: llmArticle("Climate summit opens", "reuters.com"),
			broken:   llmArticle("Climate summit opens", "bbc.co.uk"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := tt.entry
			entry.QueryID = "q1"
			entry.Docs = []pool.PooledDoc{{DocID: tt.broken.ID}, {DocID: tt.admitted.ID}}
			provider := &fakeLLMProvider{grade: func(uuid.UUID) int { return GradeHighly }}

			jf, err := llmRunner(provider, []document.Article{tt.admitted, tt.broken}, RunnerConfig{}).
				Run(context.Background(), &pool.PoolFile{Queries: []pool.PoolEntry{entry}})

			require.NoError(t, err)
			grades := gradesOf(jf)
			assert.Equal(t, GradeNotRelev, grades[tt.broken.ID])
			assert.Equal(t, GradeHighly, grades[tt.admitted.ID])
			for _, req := range provider.requests() {
				assert.NotContains(t, req.User, tt.broken.ID.String(), "no request for a doc the constraint refuses")
			}
		})
	}
}

// A boolean text that does not parse leaves its constraint undecided, so the
// query stays unjudged rather than graded by the model alone.
func TestLLMJudge_LeavesAQueryWithAnUnparsableBooleanUnjudged(t *testing.T) {
	doc := llmArticle("Climate politics heats up", "reuters.com")
	entry := pool.PoolEntry{QueryID: "q1", QueryText: "climate AND (politics", Category: suite.CategoryBoolean, Docs: []pool.PooledDoc{{DocID: doc.ID}}}
	provider := &fakeLLMProvider{grade: func(uuid.UUID) int { return GradeHighly }}

	jf, err := llmRunner(provider, []document.Article{doc}, RunnerConfig{}).
		Run(context.Background(), &pool.PoolFile{Queries: []pool.PoolEntry{entry}})

	var unparsed *BooleanTextError
	require.ErrorAs(t, err, &unparsed)
	assert.Equal(t, GradeUnjudged, gradesOf(jf)[doc.ID])
	assert.Empty(t, provider.requests())
}

// A doc is shown with its title and description in full and its content up to
// the spec's cut, counted in runes.
func TestLLMJudge_ShowsTitleAndDescriptionInFullAndContentUpToTheCut(t *testing.T) {
	const cut = 40
	whole := GradingDoc{
		ID:          uuid.New(),
		Title:       "Sava floods reach Belgrade",
		Description: strings.Repeat("Rijeka Sava se izlila. ", 40),
		Content:     strings.Repeat("Ž", cut),
	}
	long := GradingDoc{ID: uuid.New(), Title: "Danube floods", Content: strings.Repeat("Đ", 3*cut)}
	q := GradingQuery{ID: "q1", Text: "floods"}
	provider := &fakeLLMProvider{grade: func(uuid.UUID) int { return GradeRelevant }}
	s := &LLMStrategy{provider: provider, contentRunes: cut}

	for _, doc := range []GradingDoc{whole, long} {
		_, err := s.Grade(context.Background(), q, doc)
		require.NoError(t, err)
	}

	reqs := provider.requests()
	require.Len(t, reqs, 2)
	assert.Contains(t, reqs[0].User, whole.Title)
	assert.Contains(t, reqs[0].User, whole.Description)
	assert.Equal(t, whole.Content, contentShown(t, reqs[0].User, whole))
	assert.Equal(t, strings.Repeat("Đ", cut)+"...", contentShown(t, reqs[1].User, long))
}

// contentShown is the content line a prompt shows for doc, the doc's block
// starting at its id.
func contentShown(t *testing.T, prompt string, doc GradingDoc) string {
	t.Helper()
	_, block, found := strings.Cut(prompt, doc.ID.String())
	require.True(t, found, "prompt names doc %s", doc.ID)
	_, content, found := strings.Cut(block, "Content: ")
	require.True(t, found, "prompt shows doc %s's content", doc.ID)
	content, _, _ = strings.Cut(content, "\n")
	return content
}

// Each request holds one query and one doc, so its bytes are the
// same wherever the pool lists the doc and whichever engine found it.
func TestLLMJudge_RequestForADocIsTheSameWhereverThePoolListsIt(t *testing.T) {
	articles := llmArticles("Floods reach Belgrade", "Sava rises", "Danube rises")
	target := articles[0].ID
	first := pool.PoolEntry{QueryID: "q1", QueryText: "floods", Docs: []pool.PooledDoc{
		{DocID: articles[0].ID, Sources: []string{"pg"}},
		{DocID: articles[1].ID, Sources: []string{"pg"}},
		{DocID: articles[2].ID, Sources: []string{"es"}},
	}}
	last := pool.PoolEntry{QueryID: "q1", QueryText: "floods", Docs: []pool.PooledDoc{
		{DocID: articles[2].ID, Sources: []string{"pg"}},
		{DocID: articles[1].ID, Sources: []string{"es"}},
		{DocID: articles[0].ID, Sources: []string{"es", "pg"}},
	}}

	var requests []CompletionRequest
	for _, entry := range []pool.PoolEntry{first, last} {
		provider := &fakeLLMProvider{grade: func(uuid.UUID) int { return GradeRelevant }}
		_, err := llmRunner(provider, articles, RunnerConfig{}).Run(context.Background(), &pool.PoolFile{Queries: []pool.PoolEntry{entry}})
		require.NoError(t, err)
		for _, req := range provider.requests() {
			if strings.Contains(req.User, target.String()) {
				requests = append(requests, req)
			}
		}
	}

	require.Len(t, requests, 2)
	assert.Equal(t, []uuid.UUID{target}, docIDsIn(requests[0].User), "the request holds that doc alone")
	assert.Equal(t, requests[0], requests[1])
}

// An answer without a grade, or with a null one, leaves the doc unjudged for
// a retry, never graded 0.
func TestLLMJudge_LeavesADocWithoutAGradeUnjudged(t *testing.T) {
	tests := map[string]string{
		"relevance instead of grade": `"relevance":3`,
		"null grade":                 `"grade":null`,
	}
	for name, ungraded := range tests {
		t.Run(name, func(t *testing.T) {
			articles := llmArticles("Floods reach Belgrade", "Sava rises")
			missing, graded := articles[0].ID, articles[1].ID
			entry := pool.PoolEntry{QueryID: "q1", QueryText: "floods", Docs: []pool.PooledDoc{{DocID: missing}, {DocID: graded}}}
			provider := &fakeLLMProvider{reply: func(req CompletionRequest) string {
				if strings.Contains(req.User, missing.String()) {
					return `{"doc_id":"` + missing.String() + `",` + ungraded + `}`
				}
				return `{"doc_id":"` + graded.String() + `","grade":2}`
			}}

			jf, err := llmRunner(provider, articles, RunnerConfig{}).
				Run(context.Background(), &pool.PoolFile{Queries: []pool.PoolEntry{entry}})

			require.NoError(t, err)
			assert.Equal(t, GradeUnjudged, gradesOf(jf)[missing])
			assert.Equal(t, GradeRelevant, gradesOf(jf)[graded])
		})
	}
}

// A grade that fails is no grade: Grade reports it as unjudged, never as 0,
// whether the query cannot be read, the request fails or the reply has none.
func TestLLMJudge_GradeReportsAFailedGradeAsUnjudged(t *testing.T) {
	floods := GradingQuery{ID: "q1", Text: "floods"}
	tests := map[string]struct {
		query    GradingQuery
		provider *fakeLLMProvider
	}{
		"the query has no terms":          {query: GradingQuery{ID: "q1", Text: "the of a"}, provider: &fakeLLMProvider{grade: func(uuid.UUID) int { return GradeHighly }}},
		"the boolean text does not parse": {query: GradingQuery{ID: "q1", Text: "floods AND (", Category: suite.CategoryBoolean}, provider: &fakeLLMProvider{grade: func(uuid.UUID) int { return GradeHighly }}},
		"the request fails":               {query: floods, provider: &fakeLLMProvider{err: errors.New("connection refused")}},
		"the reply has no grade":          {query: floods, provider: &fakeLLMProvider{reply: func(CompletionRequest) string { return "I cannot grade this." }}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			grade, err := (&LLMStrategy{provider: tt.provider}).Grade(context.Background(), tt.query, GradingDoc{ID: uuid.New(), Title: "Floods reach Belgrade"})

			require.Error(t, err)
			assert.Equal(t, GradeUnjudged, grade)
		})
	}
}

// fakeLLMProvider grades the doc a request names with grade(doc). A set err
// fails every request.
type fakeLLMProvider struct {
	grade func(uuid.UUID) int
	reply func(CompletionRequest) string
	err   error

	mu   sync.Mutex
	seen []CompletionRequest
}

func (f *fakeLLMProvider) Name() Provider     { return "fake" }
func (f *fakeLLMProvider) Model() string      { return "fake-model" }
func (f *fakeLLMProvider) Effort() Effort     { return EffortLow }
func (f *fakeLLMProvider) Invocation() string { return "fake" }

func (f *fakeLLMProvider) Complete(_ context.Context, req CompletionRequest) (string, error) {
	f.mu.Lock()
	f.seen = append(f.seen, req)
	f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	if f.reply != nil {
		return f.reply(req), nil
	}
	id := docIDsIn(req.User)[0]
	out, err := json.Marshal(gradeEntry{DocID: id.String(), Grade: f.grade(id)})
	return string(out), err
}

func (f *fakeLLMProvider) requests() []CompletionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CompletionRequest(nil), f.seen...)
}

type gradeEntry struct {
	DocID string `json:"doc_id"`
	Grade int    `json:"grade"`
}

// docIDsIn returns the doc ids a prompt names, in the order it names them.
func docIDsIn(prompt string) []uuid.UUID {
	var ids []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, word := range strings.FieldsFunc(prompt, func(r rune) bool {
		return r != '-' && (r < '0' || r > '9') && (r < 'a' || r > 'f')
	}) {
		if id, err := uuid.Parse(word); err == nil && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func llmArticles(titles ...string) []document.Article {
	out := make([]document.Article, len(titles))
	for i, title := range titles {
		out[i] = document.Article{ID: uuid.New(), Title: title}
	}
	return out
}

func llmArticle(title, publisherDomain string) document.Article {
	return document.Article{ID: uuid.New(), Title: title, PublisherDomain: publisherDomain}
}

func llmRunner(provider LLMProvider, articles []document.Article, cfg RunnerConfig) *Runner {
	store := map[uuid.UUID]document.Article{}
	for _, a := range articles {
		store[a.ID] = a
	}
	cfg.Strategy = &LLMStrategy{provider: provider}
	cfg.Reader = &stubStorageReader{articles: store}
	return NewRunner(cfg)
}

func gradesOf(jf *File) map[uuid.UUID]int {
	out := map[uuid.UUID]int{}
	for _, qe := range jf.Queries {
		for _, d := range qe.Docs {
			out[d.DocID] = d.Grade
		}
	}
	return out
}
