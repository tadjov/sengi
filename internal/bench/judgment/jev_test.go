package judgment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/suite"
)

const testJevKey = "ts-test-key-not-a-real-one"

// Jev grades with a pinned release only: an alias re-points at another model
// without notice, and the judge block would name a model that did not answer.
func TestNewJev_TakesOnlyAPinnedJevRelease(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", testJevKey)
	for _, model := range []string{"jev-1.14.0", "jev-2.0.13"} {
		t.Run(model, func(t *testing.T) {
			s, err := NewJevStrategy(StrategyOptions{Model: model})

			require.NoError(t, err)
			assert.Equal(t, model, s.Describe().Model)
		})
	}
	for _, model := range []string{"", "jev-latest", "jev-preview", "jev-1.14", "jev-1.14.0-rc1", "JEV-1.14.0", "claude-sonnet-5-5", "sonnet"} {
		t.Run("refuses "+model, func(t *testing.T) {
			_, err := NewJevStrategy(StrategyOptions{Model: model})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "--model")
		})
	}
}

// The key comes from TYPESAFE_API_KEY alone; the Anthropic key --api-key
// carries never reaches TypeSafe.
func TestNewJev_RefusesToStartWithoutTypeSafeAPIKey(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")

	_, err := NewJevStrategy(StrategyOptions{Model: "jev-1.14.0", APIKey: "sk-ant-meant-for-claude"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "TYPESAFE_API_KEY")
}

// Jev reads the article as the LLM judge shows it, content cut included, and
// the query text under the instructions and levels probed against the API.
func TestJev_AsksAboutTheArticleTheLLMJudgeSees(t *testing.T) {
	doc := GradingDoc{
		ID:          uuid.New(),
		Title:       "Sava floods reach Belgrade",
		Description: "The river broke its banks overnight.",
		Content:     strings.Repeat("Ž", 1800),
	}
	const guidance = "Grade what the article is about, not whether the query words appear. A passing mention is the marginal level at most. When in doubt between two levels, pick the lower one."
	tests := []struct {
		name         string
		query        GradingQuery
		instructions string
	}{
		{
			name:         "keyword",
			query:        GradingQuery{ID: "q1", Text: "floods", Description: "Coverage of river floods"},
			instructions: "Query: floods\n\n" + guidance,
		},
		{
			name:  "phrase",
			query: GradingQuery{ID: "q1", Text: "Sava floods", Description: "Coverage of river floods", Category: suite.CategoryPhrase},
			instructions: "This is a phrase query, and the article contains the phrase. Grade how well it answers the query.\n\n" +
				"Query: Sava floods\n\n" + guidance,
		},
		{
			name:  "boolean",
			query: GradingQuery{ID: "q1", Text: "floods AND NOT drought", Description: "Coverage of river floods", Category: suite.CategoryBoolean},
			instructions: "This is a boolean query, and the article satisfies the whole expression, negated terms included. Grade how well it answers the query.\n\n" +
				"Query: floods AND NOT drought\n\n" + guidance,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeJev(t, jevScoreReply("jev-1.14.0", `{"0":0.0,"1":0.0,"2":1.0,"3":0.0}`))

			_, err := newTestJev(t, fake.url).Grade(context.Background(), tt.query, doc)

			require.NoError(t, err)
			req := fake.request(t, 0)
			assert.Equal(t, "jev-1.14.0", req.Model)
			assert.Equal(t, "Title: Sava floods reach Belgrade\nDescription: The river broke its banks overnight.\nContent: "+strings.Repeat("Ž", 1744)+"...\n", req.State)
			assert.Equal(t, map[string]jevQuestionSent{"grade": {
				Type:         "score",
				Instructions: tt.instructions,
				Criteria: []string{
					"The article is about something else; the query topic is absent or the words only coincide.",
					"The article mentions the query topic in passing, but its focus is something else.",
					"The article clearly covers the query topic, though it is not centrally about it.",
					"The article is centrally about the query topic.",
				},
			}}, req.Questions)
			assert.NotContains(t, string(fake.body(t, 0)), tt.query.Description)
		})
	}
}

// The same doc sends the same bytes every time it is graded.
func TestJev_SendsTheSameBytesForTheSameDoc(t *testing.T) {
	fake := newFakeJev(t, jevScoreReply("jev-1.14.0", `{"0":0.0,"1":0.0,"2":1.0,"3":0.0}`))
	s := newTestJev(t, fake.url)
	q := GradingQuery{ID: "q1", Text: "floods"}
	doc := GradingDoc{ID: uuid.New(), Title: "Sava floods reach Belgrade", Description: "The river broke its banks.", Content: "Water rose overnight."}

	for range 2 {
		_, err := s.Grade(context.Background(), q, doc)
		require.NoError(t, err)
	}

	assert.Equal(t, string(fake.body(t, 0)), string(fake.body(t, 1)))
}

// The grade is Jev's most probable level, a tie going to the lower one as
// the instructions ask, never the expected score rounded.
func TestJev_GradesTheMostProbableLevel(t *testing.T) {
	tests := []struct {
		name          string
		probabilities string
		want          int
	}{
		{name: "the probe's reply", probabilities: `{"0":0.0,"1":0.57,"2":0.43,"3":0.0}`, want: 1},
		{name: "a peak the rounded score misses", probabilities: `{"0":0.4,"1":0.0,"2":0.0,"3":0.6}`, want: 3},
		{name: "a tie between neighbours", probabilities: `{"0":0.0,"1":0.5,"2":0.5,"3":0.0}`, want: 1},
		{name: "a tie between the ends", probabilities: `{"0":0.45,"1":0.05,"2":0.05,"3":0.45}`, want: 0},
		{name: "a sum 0.04 over 1", probabilities: `{"0":0.0,"1":0.0,"2":0.53,"3":0.51}`, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeJev(t, jevScoreReply("jev-1.14.0", tt.probabilities))

			grade, err := newTestJev(t, fake.url).Grade(context.Background(), floodsQuery, floodsDoc())

			require.NoError(t, err)
			assert.Equal(t, tt.want, grade)
		})
	}
}

var floodsQuery = GradingQuery{ID: "q1", Text: "floods"}

func floodsDoc() GradingDoc {
	return GradingDoc{ID: uuid.New(), Title: "Sava floods reach Belgrade", Description: "The river broke its banks.", Content: "Water rose overnight."}
}

func newTestJev(t *testing.T, baseURL string) *JevStrategy {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", testJevKey)
	s, err := NewJevStrategy(StrategyOptions{Model: "jev-1.14.0", APIBaseURL: baseURL})
	require.NoError(t, err)
	return s
}

// jevRequestSent is a request body as the TypeSafe API reads it.
type jevRequestSent struct {
	Model     string                     `json:"model"`
	State     string                     `json:"state"`
	Questions map[string]jevQuestionSent `json:"questions"`
}

type jevQuestionSent struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria"`
}

type jevStubReply struct {
	status int
	header map[string]string
	body   string
}

// jevScoreReply is a 200 answering the grade question with probabilities, a
// JSON object keyed by level.
func jevScoreReply(model, probabilities string) jevStubReply {
	return jevStubReply{
		status: http.StatusOK,
		header: map[string]string{"x-typesafe-request-id": "req-200"},
		body: fmt.Sprintf(`{"model":%q,"answers":{"grade":{"type":"score","score":1.5,"confidence":0.4,`+
			`"legend":{"0":"a","1":"b","2":"c","3":"d"},"probabilities":%s}},"usage":{"input_tokens":332,"output_tokens":18}}`,
			model, probabilities),
	}
}

// fakeJev is a TypeSafe endpoint that answers the nth request with the nth
// reply, repeating the last, and keeps every request body. Like the real one,
// it refuses a request without the test key with a 401.
type fakeJev struct {
	url     string
	replies []jevStubReply

	mu     sync.Mutex
	bodies [][]byte
}

func newFakeJev(t *testing.T, replies ...jevStubReply) *fakeJev {
	t.Helper()
	f := &fakeJev{replies: replies}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		f.mu.Lock()
		n := len(f.bodies)
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()

		reply := f.replies[min(n, len(f.replies)-1)]
		switch {
		case r.Header.Get("Authorization") != "Bearer "+testJevKey:
			reply = jevStubReply{status: http.StatusUnauthorized, body: `{"detail":{"error_type":"authentication_error","message":"Invalid API key."}}`}
		case r.Method != http.MethodPost || r.URL.Path != "/v1/systemone":
			reply = jevStubReply{status: http.StatusNotFound, body: `{"detail":"Not Found"}`}
		case r.Header.Get("Content-Type") != "application/json":
			reply = jevStubReply{status: http.StatusBadRequest, body: `{"detail":"expected application/json"}`}
		}
		for name, value := range reply.header {
			w.Header().Set(name, value)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
	}))
	t.Cleanup(server.Close)
	f.url = server.URL
	return f
}

func (f *fakeJev) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func (f *fakeJev) body(t *testing.T, n int) []byte {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Greater(t, len(f.bodies), n, "the endpoint got request %d", n)
	return f.bodies[n]
}

// request decodes the nth body, refusing any field the API does not define.
func (f *fakeJev) request(t *testing.T, n int) jevRequestSent {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(f.body(t, n)))
	dec.DisallowUnknownFields()
	var req jevRequestSent
	require.NoError(t, dec.Decode(&req))
	return req
}
