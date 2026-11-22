package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/queryvectors"
	pkgtesting "github.com/tadjov/sengi/internal/testcontainer"
	"github.com/tadjov/sengi/internal/types/document"
)

func llmJudge(provider, model string) meta.Judge {
	return meta.Judge{
		Strategy:      string(judgment.StrategyLLM),
		Provider:      provider,
		Model:         model,
		PromptVersion: judgment.PromptVersion,
	}
}

func fileWith(j meta.Judge) *judgment.File {
	return &judgment.File{Meta: meta.Meta{Judge: &j}}
}

func TestCheckResumeCompat_FileWithoutJudgeResumes(t *testing.T) {
	// A file carrying no judge block at all has nothing to contradict.
	prior := &judgment.File{}
	now := llmJudge("claude-cli", "claude-haiku-5-5")

	err := checkResumeCompat(prior, now)
	assert.NoError(t, err, "file with no judge block should be resumable")
}

func TestCheckResumeCompat_IdenticalJudgeMatches(t *testing.T) {
	j := llmJudge("claude-cli", "claude-haiku-5-5")

	err := checkResumeCompat(fileWith(j), j)
	assert.NoError(t, err)
}

func TestCheckResumeCompat_StrategyMismatch(t *testing.T) {
	prior := fileWith(meta.Judge{Strategy: string(judgment.StrategyKeyword)})
	now := llmJudge("claude-cli", "claude-haiku-5-5")

	err := checkResumeCompat(prior, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judge mismatch")
}

func TestCheckResumeCompat_ModelMismatch(t *testing.T) {
	prior := fileWith(llmJudge("claude-cli", "claude-haiku-4-5-20251001"))
	now := llmJudge("claude-cli", "claude-opus-5")

	err := checkResumeCompat(prior, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judge mismatch")
}

func TestCheckResumeCompat_ProviderMismatch(t *testing.T) {
	prior := fileWith(llmJudge("claude-cli", "some-model"))
	now := llmJudge("codex-cli", "some-model")

	err := checkResumeCompat(prior, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judge mismatch")
}

func TestCheckResumeCompat_PromptVersionMismatch(t *testing.T) {
	prior := fileWith(meta.Judge{
		Strategy:      string(judgment.StrategyLLM),
		Provider:      "claude-cli",
		Model:         "claude-haiku-5-5",
		PromptVersion: "v0-old",
	})
	now := llmJudge("claude-cli", "claude-haiku-5-5")

	err := checkResumeCompat(prior, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judge mismatch")
}

func TestCheckResumeCompat_InvocationMismatch(t *testing.T) {
	was := llmJudge("claude-cli", "claude-haiku-5-5")
	was.Invocation = "claude -p --model claude-haiku-5-5"
	now := was
	now.Invocation = "claude -p --safe-mode --model claude-haiku-5-5"

	err := checkResumeCompat(fileWith(was), now)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `invocation: was "claude -p --model claude-haiku-5-5", now "claude -p --safe-mode --model claude-haiku-5-5"`)
}

func TestCheckResumeCompat_EffortMismatch(t *testing.T) {
	was := llmJudge("claude-api", "claude-sonnet-5-5")
	was.Effort = "low"
	now := was
	now.Effort = "high"

	err := checkResumeCompat(fileWith(was), now)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `effort: was "low", now "high"`)
	assert.NotContains(t, err.Error(), "model:", "only the fields that differ")
}

// Legacy grades came from an unrecorded model, so they must not be extended.
func TestCheckResumeCompat_LegacyFileRefusesResume(t *testing.T) {
	prior := &judgment.File{Meta: meta.Meta{
		LegacyStrategy:   "claude-cli",
		LegacyJudgeModel: "claude",
	}}
	prior.Meta.Normalize()
	now := llmJudge("claude-cli", "claude-haiku-5-5")

	err := checkResumeCompat(prior, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judge mismatch")
}

func judgmentsWithGrades(grades ...int) *judgment.File {
	docs := make([]judgment.GradedDoc, len(grades))
	for i, g := range grades {
		docs[i] = judgment.GradedDoc{DocID: uuid.New(), Grade: g}
	}
	return &judgment.File{
		Meta:    meta.Meta{Judge: &meta.Judge{Strategy: string(judgment.StrategyBM25)}},
		Queries: []judgment.Entry{{QueryID: "q1", Docs: docs}},
	}
}

func TestRequireJudged(t *testing.T) {
	cases := []struct {
		name          string
		grades        []int
		allowUnjudged bool
		wantErr       bool
	}{
		{name: "every doc graded", grades: []int{0, 3}},
		{name: "an unjudged doc fails", grades: []int{2, judgment.GradeUnjudged}, wantErr: true},
		{name: "an unjudged doc is allowed on request", grades: []int{2, judgment.GradeUnjudged}, allowUnjudged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireJudged(judgmentsWithGrades(tc.grades...), tc.allowUnjudged)

			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCheckResumeCompat_RubricVersionMismatch(t *testing.T) {
	prior := fileWith(meta.Judge{Strategy: string(judgment.StrategyBM25)})

	err := checkResumeCompat(prior, judgment.NewBM25Strategy().Describe())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "judge mismatch")
}

func TestBuildManualJudgments_KeepsHandGradesAndAddsNewPoolDocs(t *testing.T) {
	graded, placeholder, newInPool, leftThePool, newQueryDoc := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	pf := &pool.PoolFile{Queries: []pool.PoolEntry{
		{QueryID: "q1", Docs: []pool.PooledDoc{{DocID: graded}, {DocID: placeholder}, {DocID: newInPool}}},
		{QueryID: "q2", Docs: []pool.PooledDoc{{DocID: newQueryDoc}}},
	}}
	existing := &judgment.File{Queries: []judgment.Entry{{QueryID: "q1", Docs: []judgment.GradedDoc{
		{DocID: graded, Grade: judgment.GradeRelevant},
		{DocID: placeholder, Grade: judgment.GradeUnjudged},
		{DocID: leftThePool, Grade: judgment.GradeNotRelev},
	}}}}

	got := buildManualJudgments(pf, existing)

	want := map[string]map[uuid.UUID]int{
		"q1": {
			graded:      judgment.GradeRelevant,
			placeholder: judgment.GradeUnjudged,
			newInPool:   judgment.GradeUnjudged,
			leftThePool: judgment.GradeNotRelev,
		},
		"q2": {newQueryDoc: judgment.GradeUnjudged},
	}
	assert.Equal(t, want, gradesByQuery(got))
}

func TestBuildManualJudgments_WithoutExistingFileWritesPlaceholders(t *testing.T) {
	doc := uuid.New()
	pf := &pool.PoolFile{Queries: []pool.PoolEntry{{QueryID: "q1", Docs: []pool.PooledDoc{{DocID: doc}}}}}

	got := buildManualJudgments(pf, nil)

	assert.Equal(t, map[string]map[uuid.UUID]int{"q1": {doc: judgment.GradeUnjudged}}, gradesByQuery(got))
}

func gradesByQuery(jf *judgment.File) map[string]map[uuid.UUID]int {
	out := map[string]map[uuid.UUID]int{}
	for _, qe := range jf.Queries {
		out[qe.QueryID] = map[uuid.UUID]int{}
		for _, d := range qe.Docs {
			out[qe.QueryID][d.DocID] = d.Grade
		}
	}
	return out
}

type articleStore map[uuid.UUID]document.Article

func (s articleStore) GetByIDs(_ context.Context, ids []uuid.UUID) ([]document.Article, error) {
	out := make([]document.Article, 0, len(ids))
	for _, id := range ids {
		if a, ok := s[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// judgeFixture is a one-query track on disk plus the store its articles come from.
type judgeFixture struct {
	track    string
	articles articleStore
	pool     *pool.PoolFile
	// reader, when set, is read instead of articles.
	reader storage.Reader
}

func newJudgeFixture(t *testing.T, queryText string, titles ...string) *judgeFixture {
	t.Helper()
	track := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(track, "trec"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(track, "spec.yaml"), []byte("schema_version: 1\nid: judge\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(track, "suite.yaml"), []byte("schema_version: 1\nid: judge\n"), 0o644))

	fx := &judgeFixture{track: track, articles: articleStore{}}
	entry := pool.PoolEntry{QueryID: "q1", QueryText: queryText}
	for _, title := range titles {
		id := uuid.New()
		fx.articles[id] = document.Article{ID: id, Title: title}
		entry.Docs = append(entry.Docs, pool.PooledDoc{DocID: id})
	}
	fx.pool = &pool.PoolFile{Meta: meta.New("pool"), Queries: []pool.PoolEntry{entry}}
	fx.writePool(t)
	return fx
}

func (fx *judgeFixture) writePool(t *testing.T) {
	t.Helper()
	require.NoError(t, pool.WritePoolFile(fx.pool, filepath.Join(fx.track, "trec", "pool.yaml")))
}

func (fx *judgeFixture) judge(t *testing.T, args ...string) error {
	t.Helper()
	_, err := fx.judgeOutput(t, args...)
	return err
}

func (fx *judgeFixture) judgeOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newJudgeCmdWithReader(func(*cobra.Command, string) (storage.Reader, error) {
		if fx.reader != nil {
			return fx.reader, nil
		}
		return fx.articles, nil
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{fx.track}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func (fx *judgeFixture) writePriorBM25(t *testing.T, runID string, queries ...judgment.Entry) {
	t.Helper()
	judge := judgment.NewBM25Strategy().Describe()
	require.NoError(t, judgment.WriteFile(&judgment.File{
		Meta:    meta.Meta{RunID: runID, Judge: &judge, PoolRunID: fx.pool.Meta.RunID},
		Queries: queries,
	}, filepath.Join(fx.track, "trec", "annotations.bm25.yaml")))
}

func (fx *judgeFixture) annotations(t *testing.T, set string) *judgment.File {
	t.Helper()
	jf, err := judgment.ReadFile(filepath.Join(fx.track, "trec", "annotations."+set+".yaml"))
	require.NoError(t, err)
	return jf
}

func TestJudge_UnjudgedDocFailsAfterWritingTheFile(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit", "climate report")
	for id := range fx.articles {
		delete(fx.articles, id)
		break
	}

	err := fx.judge(t, "--strategy", "bm25")

	require.Error(t, err)
	jf := fx.annotations(t, "bm25")
	require.NotNil(t, jf.Meta.UnjudgedCount)
	assert.Equal(t, 1, *jf.Meta.UnjudgedCount)
}

func TestJudge_AllowUnjudgedExitsZero(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit", "climate report")
	for id := range fx.articles {
		delete(fx.articles, id)
		break
	}

	assert.NoError(t, fx.judge(t, "--strategy", "bm25", "--allow-unjudged"))
}

func TestJudge_FullyGradedPoolExitsZero(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit", "climate report")

	require.NoError(t, fx.judge(t, "--strategy", "bm25"))

	jf := fx.annotations(t, "bm25")
	require.NotNil(t, jf.Meta.UnjudgedCount)
	assert.Zero(t, *jf.Meta.UnjudgedCount)
}

func TestJudge_QueryWithoutTermsFailsAfterWritingTheFile(t *testing.T) {
	fx := newJudgeFixture(t, "the of a", "climate change summit")
	fx.writePriorBM25(t, "prior-run", judgment.Entry{QueryID: "q-gone"})

	err := fx.judge(t, "--strategy", "bm25", "--allow-unjudged", "--resume")

	var noTerms *judgment.NoQueryTermsError
	require.ErrorAs(t, err, &noTerms)
	assert.Equal(t, "q1", noTerms.QueryID)
	got := gradesByQuery(fx.annotations(t, "bm25"))
	assert.Equal(t, judgment.GradeUnjudged, got["q1"][fx.pool.Queries[0].Docs[0].DocID])
	assert.NotContains(t, got, "q-gone")
}

func TestJudge_ResumedFileCarriesTheNewRunID(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	fx.writePriorBM25(t, "prior-run", judgment.Entry{QueryID: "q1"})

	require.NoError(t, fx.judge(t, "--strategy", "bm25", "--resume"))

	runID := fx.annotations(t, "bm25").Meta.RunID
	assert.NotEmpty(t, runID)
	assert.NotEqual(t, "prior-run", runID)
}

func TestJudge_ManualRerunKeepsHandGrades(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	require.NoError(t, fx.judge(t, "--strategy", "manual"))
	handGraded := fx.annotations(t, "manual")
	handGraded.Queries[0].Docs[0].Grade = judgment.GradeHighly
	require.NoError(t, judgment.WriteFile(handGraded, filepath.Join(fx.track, "trec", "annotations.manual.yaml")))
	newDoc := uuid.New()
	fx.pool.Queries[0].Docs = append(fx.pool.Queries[0].Docs, pool.PooledDoc{DocID: newDoc})
	fx.writePool(t)

	require.NoError(t, fx.judge(t, "--strategy", "manual"))

	got := gradesByQuery(fx.annotations(t, "manual"))["q1"]
	assert.Equal(t, judgment.GradeHighly, got[handGraded.Queries[0].Docs[0].DocID])
	assert.Equal(t, judgment.GradeUnjudged, got[newDoc])
}

func TestJudge_ManualForceDiscardsHandGrades(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	require.NoError(t, fx.judge(t, "--strategy", "manual"))
	handGraded := fx.annotations(t, "manual")
	handGraded.Queries[0].Docs[0].Grade = judgment.GradeHighly
	require.NoError(t, judgment.WriteFile(handGraded, filepath.Join(fx.track, "trec", "annotations.manual.yaml")))

	require.NoError(t, fx.judge(t, "--strategy", "manual", "--force"))

	assert.Equal(t, judgment.GradeUnjudged, fx.annotations(t, "manual").Queries[0].Docs[0].Grade)
}

func TestJudge_ResumeDropsQueriesThatLeftThePool(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	judge := judgment.NewBM25Strategy().Describe()
	require.NoError(t, judgment.WriteFile(&judgment.File{
		Meta:    meta.Meta{Judge: &judge, PoolRunID: fx.pool.Meta.RunID},
		Queries: []judgment.Entry{{QueryID: "q-gone", Docs: []judgment.GradedDoc{{DocID: uuid.New(), Grade: judgment.GradeUnjudged}}}},
	}, filepath.Join(fx.track, "trec", "annotations.bm25.yaml")))

	require.NoError(t, fx.judge(t, "--strategy", "bm25", "--resume"))

	assert.NotContains(t, gradesByQuery(fx.annotations(t, "bm25")), "q-gone")
}

func TestJudge_ManualRefusesAnotherJudgesFile(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	require.NoError(t, fx.judge(t, "--strategy", "bm25"))
	bm25Path := filepath.Join(fx.track, "trec", "annotations.bm25.yaml")

	err := fx.judge(t, "--strategy", "manual", "--output", bm25Path)

	require.Error(t, err)
	assert.Equal(t, string(judgment.StrategyBM25), fx.annotations(t, "bm25").Judge().Strategy)
}

func TestJudge_ManualForceReplacesAnotherJudgesFile(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	require.NoError(t, fx.judge(t, "--strategy", "bm25"))
	bm25Path := filepath.Join(fx.track, "trec", "annotations.bm25.yaml")

	require.NoError(t, fx.judge(t, "--strategy", "manual", "--output", bm25Path, "--force"))

	assert.Equal(t, string(judgment.StrategyManual), fx.annotations(t, "bm25").Judge().Strategy)
}

// interruptingStore cancels the run when the article interruptAt is fetched.
type interruptingStore struct {
	articleStore
	interruptAt uuid.UUID
	interrupt   context.CancelFunc
}

func (s *interruptingStore) GetByIDs(ctx context.Context, ids []uuid.UUID) ([]document.Article, error) {
	if slices.Contains(ids, s.interruptAt) {
		s.interrupt()
	}
	return s.articleStore.GetByIDs(ctx, ids)
}

func TestJudge_InterruptedRunLeavesAnIdentifiedFile(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	q2Doc := uuid.New()
	fx.articles[q2Doc] = document.Article{ID: q2Doc, Title: "climate report"}
	fx.pool.Queries = append(fx.pool.Queries, pool.PoolEntry{QueryID: "q2", QueryText: "climate", Docs: []pool.PooledDoc{{DocID: q2Doc}}})
	fx.writePool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &interruptingStore{articleStore: fx.articles, interruptAt: q2Doc, interrupt: cancel}

	cmd := newJudgeCmdWithReader(func(*cobra.Command, string) (storage.Reader, error) { return store, nil })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{fx.track, "--strategy", "bm25"})
	require.ErrorIs(t, cmd.ExecuteContext(ctx), context.Canceled)

	jf := fx.annotations(t, "bm25")
	assert.NotEmpty(t, jf.Meta.RunID)
	assert.Equal(t, fx.pool.Meta.RunID, jf.Meta.PoolRunID, "--resume reads only this flush, so it must name the pool")
	assert.NotNil(t, jf.Meta.UnjudgedCount)
	assert.Contains(t, gradesByQuery(jf), "q1")
}

func TestJudge_ForceOutsideManualIsRejected(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")

	err := fx.judge(t, "--strategy", "bm25", "--force")

	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(fx.track, "trec", "annotations.bm25.yaml"))
}

// The Ollama here answers with the opposite vector, so only the package's vector
// puts the one doc stored at it among the ten nearest.
func TestJudge_VectorGradesWithTheTracksQueryVectors(t *testing.T) {
	ctx := context.Background()
	container := pkgtesting.NewPGContainerWithCleanup(ctx, t)
	track := queryVectorsTrack(t, container.ConnString)
	const queryText = "central bank interest rates"
	queryVector := packageQueryVector(t, track, queryText)
	opposite := make([]float32, len(queryVector))
	for i, v := range queryVector {
		opposite[i] = -v
	}

	conn, err := pgx.Connect(ctx, container.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(ctx) })
	fx := &judgeFixture{track: track, articles: articleStore{}}
	entry := pool.PoolEntry{QueryID: "q2", QueryText: queryText}
	store := func(vector []float32) uuid.UUID {
		id := uuid.New()
		_, err := conn.Exec(ctx, `INSERT INTO articles (id, url, title, content, language) VALUES ($1, $2, $3, $4, $5)`,
			id, "https://example.com/"+id.String(), "rates", "content", "eng")
		require.NoError(t, err)
		_, err = conn.Exec(ctx, `INSERT INTO article_embeddings (article_id, model_name, embedding) VALUES ($1, $2, $3)`,
			id, queryVectorsFixtureTag, pgvector.NewVector(vector))
		require.NoError(t, err)
		fx.articles[id] = document.Article{ID: id, Title: "rates"}
		entry.Docs = append(entry.Docs, pool.PooledDoc{DocID: id})
		return id
	}
	docID := store(queryVector)
	for range 10 {
		store(opposite)
	}
	fx.pool = &pool.PoolFile{Meta: meta.New("pool"), Queries: []pool.PoolEntry{entry}}
	fx.writePool(t)

	var ollamaCalls atomic.Int32
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ollamaCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": opposite})
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("EMBEDDING_BASE_URL", ollama.URL)
	t.Setenv("EMBEDDING_MODEL", "qwen3-embedding:0.6b")
	t.Setenv("PG_CONNECTION_STRING", "")

	t.Run("grades with the package vector", func(t *testing.T) {
		require.NoError(t, fx.judge(t, "--strategy", "vector", "--pg", container.ConnString))

		assert.Equal(t, judgment.GradeHighly, gradesByQuery(fx.annotations(t, "vector"))["q2"][docID])
		assert.Zero(t, ollamaCalls.Load())
	})

	t.Run("refuses a package the suite has moved past", func(t *testing.T) {
		suitePath := filepath.Join(track, "suite.yaml")
		raw, err := os.ReadFile(suitePath)
		require.NoError(t, err)
		edited := strings.Replace(string(raw), `"climate change policy"`, `"climate change policies"`, 1)
		require.NoError(t, os.WriteFile(suitePath, []byte(edited), 0o644))

		err = fx.judge(t, "--strategy", "vector", "--pg", container.ConnString)

		var stale *queryvectors.StaleQueryError
		require.ErrorAs(t, err, &stale)
		assert.Equal(t, "q1", stale.QueryID)
	})
}

func packageQueryVector(t *testing.T, track, text string) []float32 {
	t.Helper()
	store, err := queryvectors.Open(context.Background(), queryvectors.Options{
		Package: filepath.Join(track, "trec", "query-vectors"),
		Suite:   filepath.Join(track, "suite.yaml"),
		Model:   queryVectorsFixtureTag,
	})
	require.NoError(t, err)
	vector, err := store.QueryVector(context.Background(), text)
	require.NoError(t, err)
	return vector
}

func TestJudge_KeywordStrategyWritesItsOwnSet(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")

	require.NoError(t, fx.judge(t, "--strategy", "keyword"))

	assert.Equal(t, "keyword", fx.annotations(t, "keyword").Judge().Strategy)
}

func TestJudge_LexicalIsNoLongerAStrategy(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")

	err := fx.judge(t, "--strategy", "lexical")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "keyword")
	assert.NoFileExists(t, filepath.Join(fx.track, "trec", "annotations.lexical.yaml"))
}

func TestJudge_PoolQueryWithoutTextFailsNamingIt(t *testing.T) {
	for _, strategy := range []string{"keyword", "bm25"} {
		t.Run(strategy, func(t *testing.T) {
			fx := newJudgeFixture(t, "", "climate change summit")
			fx.pool.Queries[0].QueryDesc = "Coverage of climate change"
			fx.writePool(t)

			err := fx.judge(t, "--strategy", strategy)

			require.Error(t, err)
			assert.Contains(t, err.Error(), `"q1"`)
			assert.NoFileExists(t, filepath.Join(fx.track, "trec", "annotations."+strategy+".yaml"))
		})
	}
}

func TestJudge_ManualNeedsNoQueryText(t *testing.T) {
	fx := newJudgeFixture(t, "", "climate change summit")

	require.NoError(t, fx.judge(t, "--strategy", "manual"))
}

func TestCheckResumeCompat_RefusesTheDeterministicJudgesV1Rubric(t *testing.T) {
	for _, strategy := range []judgment.Strategy{
		judgment.NewKeywordStrategy(),
		judgment.NewBM25Strategy(),
		judgment.NewVectorStrategyWithStore(stubVectorStore{}, "qwen3-embedding:0.6b@1024"),
		judgment.NewHybridStrategyWithStore(stubVectorStore{}, "qwen3-embedding:0.6b@1024"),
	} {
		t.Run(strategy.Name(), func(t *testing.T) {
			v1 := strategy.Describe()
			v1.RubricVersion = "v1"

			err := checkResumeCompat(fileWith(v1), strategy.Describe())

			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("rubric_version: was %q, now %q", "v1", strategy.Describe().RubricVersion))
		})
	}
}

func TestJudge_UnparsableBooleanTextFailsAfterWritingTheFile(t *testing.T) {
	fx := newJudgeFixture(t, "climate AND (politics", "climate change summit")
	fx.pool.Queries[0].Category = "boolean"
	fx.writePool(t)

	out, err := fx.judgeOutput(t, "--strategy", "bm25", "--allow-unjudged")

	var unparsed *judgment.BooleanTextError
	require.ErrorAs(t, err, &unparsed)
	assert.Equal(t, "q1", unparsed.QueryID)
	jf := fx.annotations(t, "bm25")
	assert.Equal(t, judgment.GradeUnjudged, gradesByQuery(jf)["q1"][fx.pool.Queries[0].Docs[0].DocID])
	require.NotNil(t, jf.Meta.GradedCount)
	require.NotNil(t, jf.Meta.UnjudgedCount)
	assert.Equal(t, 0, *jf.Meta.GradedCount)
	assert.Equal(t, 1, *jf.Meta.UnjudgedCount)
	// Every flush writes this file, so only the closing line shows it was finalised.
	assert.Regexp(t, `Judgments written: \S+/trec/annotations\.bm25\.yaml`, out)
}

func TestJudge_LLMJudgeBlockRecordsItsEffort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary is a POSIX shell script")
	}
	tests := []struct {
		name   string
		args   []string
		effort string
	}{
		{name: "default effort", effort: "low"},
		{name: "another effort", args: []string{"--effort", "medium"}, effort: "medium"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newJudgeFixture(t, "climate change", "climate change summit", "climate report", "climate deal")
			args := []string{"--strategy", "llm", "--provider", "claude-cli", "--cli-binary", gradingClaudeCLI(t), "--model", "claude-haiku-5-5"}

			require.NoError(t, fx.judge(t, append(args, tt.args...)...))

			jf := fx.annotations(t, "claude-cli")
			assert.Equal(t, tt.effort, jf.Judge().Effort)
			assert.Contains(t, jf.Judge().Invocation, "--model claude-haiku-5-5")
			assert.NotContains(t, slices.Collect(maps.Values(gradesByQuery(jf)["q1"])), judgment.GradeUnjudged)
		})
	}
}

// gradingClaudeCLI is a fake claude binary that grades every doc 2 and keeps its last arguments.
func gradingClaudeCLI(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "2.1.322 (Claude Code)"
  exit 0
fi
printf '%s\n' "$@" > "$(dirname "$0")/args"
ids=$(grep -oE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' | sort -u)
entries=""
for id in $ids; do
  [ -n "$entries" ] && entries="$entries,"
  entries="$entries{\\\"doc_id\\\":\\\"$id\\\",\\\"grade\\\":2}"
done
printf '{"type":"result","is_error":false,"result":"[%s]"}\n' "$entries"
`
	path := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func TestJudge_LLMWithoutModelGradesWithTheDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	if runtime.GOOS == "windows" {
		t.Skip("fake binary is a POSIX shell script")
	}
	var apiModels []string
	var mu sync.Mutex
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		apiModels = append(apiModels, body.Model)
		mu.Unlock()
		docID := docIDPattern.FindString(body.Messages[0].Content)
		reply, err := json.Marshal(`{"doc_id":"` + docID + `","grade":2}`)
		assert.NoError(t, err)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":` + string(reply) + `}]}`))
	}))
	defer api.Close()
	cli := gradingClaudeCLI(t)

	for _, provider := range []string{"claude-cli", "claude-api"} {
		t.Run(provider, func(t *testing.T) {
			fx := newJudgeFixture(t, "climate change", "climate change summit")

			require.NoError(t, fx.judge(t, "--strategy", "llm", "--provider", provider,
				"--api-base", api.URL, "--cli-binary", cli))

			assert.Equal(t, "claude-sonnet-5-5", fx.annotations(t, provider).Judge().Model)
		})
	}
	assert.Equal(t, "claude-sonnet-5-5", gradingCLIFlag(t, cli, "--model"), "the CLI grades with the default")
	assert.Equal(t, []string{"claude-sonnet-5-5"}, slices.Compact(apiModels), "the API grades with the default")

	for _, provider := range []string{"claude-cli", "claude-api"} {
		t.Run(provider+" with an empty --model", func(t *testing.T) {
			fx := newJudgeFixture(t, "climate change", "climate change summit")

			err := fx.judge(t, "--strategy", "llm", "--provider", provider, "--model", "",
				"--api-base", api.URL, "--cli-binary", cli)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "--model")
			assert.NoFileExists(t, filepath.Join(fx.track, "trec", "annotations."+provider+".yaml"))
		})
	}
}

var docIDPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func TestJudge_LLMUnknownEffortFailsNamingTheFlag(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")

	err := fx.judge(t, "--strategy", "llm", "--provider", "claude-cli", "--cli-binary", filepath.Join(t.TempDir(), "no-claude-here"),
		"--model", "sonnet", "--effort", "minimal")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--effort")
	assert.NoFileExists(t, filepath.Join(fx.track, "trec", "annotations.claude-cli.yaml"))
}

func TestCheckResumeCompat_RefusesTheLLMJudgesV2Prompt(t *testing.T) {
	strategy, err := judgment.NewLLMStrategy(judgment.StrategyOptions{
		Provider: judgment.ProviderClaudeCLI, CLIBinary: gradingClaudeCLI(t), Model: "sonnet", Effort: judgment.EffortLow,
	})
	require.NoError(t, err)
	v2 := strategy.Describe()
	v2.PromptVersion = "v2"

	err = checkResumeCompat(fileWith(v2), strategy.Describe())

	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("prompt_version: was %q, now %q", "v2", judgment.PromptVersion))
}

func TestCheckResumeCompat_RefusesAFileGradedUnderOtherGates(t *testing.T) {
	llm, err := judgment.NewLLMStrategy(judgment.StrategyOptions{
		Provider: judgment.ProviderClaudeCLI, CLIBinary: gradingClaudeCLI(t), Model: "sonnet", Effort: judgment.EffortLow,
	})
	require.NoError(t, err)
	gates := llm.Describe().GateVersion
	require.NotEmpty(t, gates, "the LLM judge block names its gates")
	t.Setenv("TYPESAFE_API_KEY", "ts-cli-test-key")
	jev, err := judgment.NewJevStrategy(judgment.StrategyOptions{Model: "jev-1.14.0"})
	require.NoError(t, err)
	for _, strategy := range []judgment.Strategy{judgment.NewKeywordStrategy(), judgment.NewBM25Strategy(), llm, jev} {
		t.Run(strategy.Name(), func(t *testing.T) {
			now := strategy.Describe()
			assert.Equal(t, gates, now.GateVersion, "one gate version for every judge that gates")
			was := now
			was.GateVersion = "v0"

			err := checkResumeCompat(fileWith(was), now)

			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("gate_version: was %q, now %q", "v0", now.GateVersion))
		})
	}
}

// An unset make variable passes an empty --model.
func TestJudge_LLMEmptyModelFailsWithoutTouchingTheAnnotations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary is a POSIX shell script")
	}
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	path := filepath.Join(fx.track, "trec", "annotations.claude-cli.yaml")
	require.NoError(t, os.WriteFile(path, []byte("graded by hand, keep me\n"), 0o644))

	err := fx.judge(t, "--strategy", "llm", "--provider", "claude-cli", "--cli-binary", gradingClaudeCLI(t), "--model", "", "--resume")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--model")
	kept, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "graded by hand, keep me\n", string(kept))
}

func gradingCLIFlag(t *testing.T, cli, flag string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(cli), "args"))
	require.NoError(t, err)
	args := strings.Split(string(raw), "\n")
	i := slices.Index(args, flag)
	require.True(t, i >= 0 && i+1 < len(args), "the CLI got %s", flag)
	return args[i+1]
}

func TestJudge_CarryOverGradesOnlyNewDocs(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	oldPoolRunID, oldDoc := fx.pool.Meta.RunID, fx.pool.Queries[0].Docs[0].DocID
	oldPool := filepath.Join(t.TempDir(), "old-pool.yaml")
	require.NoError(t, pool.WritePoolFile(fx.pool, oldPool))
	judge := judgment.NewKeywordStrategy().Describe()
	old := filepath.Join(t.TempDir(), "old.yaml")
	require.NoError(t, judgment.WriteFile(&judgment.File{
		Meta:    meta.Meta{RunID: "old-judge", Judge: &judge, PoolRunID: oldPoolRunID},
		Queries: []judgment.Entry{{QueryID: "q1", Docs: []judgment.GradedDoc{{DocID: oldDoc, Grade: judgment.GradeMarginally}}}},
	}, old))
	newDoc := uuid.New()
	fx.articles[newDoc] = document.Article{ID: newDoc, Title: "climate change protest"}
	fx.pool.Meta = meta.New("pool")
	fx.pool.Queries[0].Docs = append(fx.pool.Queries[0].Docs, pool.PooledDoc{DocID: newDoc})
	fx.writePool(t)

	require.NoError(t, fx.judge(t, "--strategy", "keyword", "--carry-over", old, "--carry-over-pool", oldPool))

	got := fx.annotations(t, "keyword")
	grades := gradesByQuery(got)["q1"]
	// Keyword would grade the old doc 3; 1 means it was reused.
	assert.Equal(t, judgment.GradeMarginally, grades[oldDoc])
	assert.GreaterOrEqual(t, grades[newDoc], judgment.GradeNotRelev)
	assert.Equal(t, &meta.CarriedOver{JudgmentsRunID: "old-judge", PoolRunID: oldPoolRunID, Grades: 1}, got.Meta.CarriedOver)
	assert.Equal(t, fx.pool.Meta.RunID, got.Meta.PoolRunID)
}

// rePoolOverOldJudgments returns the flags that carry an old grade of q1's first doc, and their record.
func (fx *judgeFixture) rePoolOverOldJudgments(t *testing.T, newDocs ...uuid.UUID) ([]string, *meta.CarriedOver) {
	t.Helper()
	oldPoolRunID, oldDoc := fx.pool.Meta.RunID, fx.pool.Queries[0].Docs[0].DocID
	oldPool := filepath.Join(t.TempDir(), "old-pool.yaml")
	require.NoError(t, pool.WritePoolFile(fx.pool, oldPool))
	judge := judgment.NewKeywordStrategy().Describe()
	old := filepath.Join(t.TempDir(), "old.yaml")
	require.NoError(t, judgment.WriteFile(&judgment.File{
		Meta:    meta.Meta{RunID: "old-judge", Judge: &judge, PoolRunID: oldPoolRunID},
		Queries: []judgment.Entry{{QueryID: "q1", Docs: []judgment.GradedDoc{{DocID: oldDoc, Grade: judgment.GradeMarginally}}}},
	}, old))
	fx.pool.Meta = meta.New("pool")
	for _, id := range newDocs {
		fx.pool.Queries[0].Docs = append(fx.pool.Queries[0].Docs, pool.PooledDoc{DocID: id})
	}
	fx.writePool(t)
	return []string{"--carry-over", old, "--carry-over-pool", oldPool},
		&meta.CarriedOver{JudgmentsRunID: "old-judge", PoolRunID: oldPoolRunID, Grades: 1}
}

func TestJudge_ResumeAfterACarryOverKeepsItsRecord(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	oldDoc, newDoc := fx.pool.Queries[0].Docs[0].DocID, uuid.New()
	carryOver, want := fx.rePoolOverOldJudgments(t, newDoc)
	err := fx.judge(t, append([]string{"--strategy", "keyword"}, carryOver...)...)
	require.ErrorContains(t, err, "unjudged", "newDoc has no article to grade yet")
	fx.articles[newDoc] = document.Article{ID: newDoc, Title: "climate change protest"}

	require.NoError(t, fx.judge(t, "--strategy", "keyword", "--resume"))

	got := fx.annotations(t, "keyword")
	assert.Equal(t, judgment.GradeMarginally, gradesByQuery(got)["q1"][oldDoc], "the carried grade is still the file's")
	assert.Equal(t, want, got.Meta.CarriedOver)
}

type readerFailingOn struct {
	articleStore
	doc uuid.UUID
}

func (r readerFailingOn) GetByIDs(ctx context.Context, ids []uuid.UUID) ([]document.Article, error) {
	if slices.Contains(ids, r.doc) {
		return nil, errors.New("connection reset")
	}
	return r.articleStore.GetByIDs(ctx, ids)
}

func TestJudge_InterruptedCarryOverRunKeepsItsRecord(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	carryOver, want := fx.rePoolOverOldJudgments(t)
	unreadable := uuid.New()
	fx.pool.Queries = append(fx.pool.Queries, pool.PoolEntry{QueryID: "q2", QueryText: "climate report", Docs: []pool.PooledDoc{{DocID: unreadable}}})
	fx.writePool(t)
	fx.reader = readerFailingOn{articleStore: fx.articles, doc: unreadable}

	err := fx.judge(t, append([]string{"--strategy", "keyword"}, carryOver...)...)

	require.ErrorContains(t, err, "connection reset")
	assert.Equal(t, want, fx.annotations(t, "keyword").Meta.CarriedOver, "the file q1 left behind")
}

func TestJudge_ResumeAfterARunStoppedBeforeTheCarriedQueryKeepsItsGrade(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")
	carriedDoc := fx.pool.Queries[0].Docs[0].DocID
	carryOver, want := fx.rePoolOverOldJudgments(t)
	reachedDoc, unreadable := uuid.New(), uuid.New()
	fx.articles[reachedDoc] = document.Article{ID: reachedDoc, Title: "climate report"}
	fx.pool.Queries = append([]pool.PoolEntry{
		{QueryID: "q-reached", QueryText: "climate report", Docs: []pool.PooledDoc{{DocID: reachedDoc}}},
		{QueryID: "q-unread", QueryText: "climate summit", Docs: []pool.PooledDoc{{DocID: unreadable}}},
	}, fx.pool.Queries...)
	fx.writePool(t)
	fx.reader = readerFailingOn{articleStore: fx.articles, doc: unreadable}
	err := fx.judge(t, append([]string{"--strategy", "keyword"}, carryOver...)...)
	require.ErrorContains(t, err, "connection reset")
	fx.reader = nil
	fx.articles[unreadable] = document.Article{ID: unreadable, Title: "climate summit"}

	require.NoError(t, fx.judge(t, "--strategy", "keyword", "--resume"))

	got := fx.annotations(t, "keyword")
	// keyword grades it 3, so 1 means the carried grade survived.
	assert.Equal(t, judgment.GradeMarginally, gradesByQuery(got)["q1"][carriedDoc])
	assert.Equal(t, want, got.Meta.CarriedOver)
}

func TestJudge_CarryOverRefusesWholePoolJudges(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")

	err := fx.judge(t, "--strategy", "bm25", "--carry-over", "old.yaml", "--carry-over-pool", "old-pool.yaml")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "whole pool")
}

// jevAPI grades every doc 2 under the model it is asked for and records each request.
type jevAPI struct {
	*httptest.Server

	mu    sync.Mutex
	asked []jevAsked
}

type jevAsked struct {
	Model string `json:"model"`
	State string `json:"state"`
}

func newJevAPI(t *testing.T) *jevAPI {
	t.Helper()
	api := &jevAPI{}
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jevAsked
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		api.mu.Lock()
		api.asked = append(api.asked, req)
		api.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"answers":{"grade":{"type":"score","score":2.0,"confidence":0.9,`+
			`"probabilities":{"0":0.0,"1":0.0,"2":1.0,"3":0.0}}},"usage":{"input_tokens":400,"output_tokens":18}}`, req.Model)
	}))
	t.Cleanup(api.Close)
	return api
}

func (a *jevAPI) requests() []jevAsked {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]jevAsked(nil), a.asked...)
}

func TestJudge_JevWithoutModelGradesWithThePinnedRelease(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "ts-cli-test-key")
	api := newJevAPI(t)
	fx := newJudgeFixture(t, "climate change", "climate change summit", "climate report")

	require.NoError(t, fx.judge(t, "--strategy", "jev", "--api-base", api.URL))

	jf := fx.annotations(t, "jev")
	assert.Equal(t, "jev", jf.Judge().Strategy)
	assert.Equal(t, "jev-1.14.0", jf.Judge().Model)
	for _, grade := range gradesByQuery(jf)["q1"] {
		assert.Equal(t, judgment.GradeRelevant, grade)
	}
	for _, req := range api.requests() {
		assert.Equal(t, "jev-1.14.0", req.Model)
	}
}

func TestJudge_JevRefusesAModelItCannotPin(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "ts-cli-test-key")
	for _, model := range []string{"claude-sonnet-5-5", "jev-latest", ""} {
		t.Run(model, func(t *testing.T) {
			api := newJevAPI(t)
			fx := newJudgeFixture(t, "climate change", "climate change summit")

			err := fx.judge(t, "--strategy", "jev", "--api-base", api.URL, "--model", model)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "--model")
			assert.Empty(t, api.requests())
			assert.NoFileExists(t, filepath.Join(fx.track, "trec", "annotations.jev.yaml"))
		})
	}
}

func TestJudge_RefusesLLMFlagsOutsideLLM(t *testing.T) {
	flags := []struct {
		flag string
		args []string
	}{
		{flag: "--provider", args: []string{"--provider", "claude-api"}},
	}
	for _, strategy := range []string{"keyword", "bm25", "jev", "manual"} {
		for _, tt := range flags {
			t.Run(strategy+" "+tt.flag, func(t *testing.T) {
				t.Setenv("TYPESAFE_API_KEY", "ts-cli-test-key")
				api := newJevAPI(t)
				fx := newJudgeFixture(t, "climate change", "climate change summit")

				err := fx.judge(t, append([]string{"--strategy", strategy, "--api-base", api.URL}, tt.args...)...)

				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.flag)
				assert.Empty(t, api.requests())
				assert.NoFileExists(t, filepath.Join(fx.track, "trec", "annotations."+strategy+".yaml"))
			})
		}
	}
}

func TestJudge_LLMWithoutProviderUsesClaudeCLI(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")

	require.NoError(t, fx.judge(t, "--strategy", "llm", "--cli-binary", gradingClaudeCLI(t), "--model", "claude-haiku-5-5"))

	assert.Equal(t, "claude-cli", fx.annotations(t, "claude-cli").Judge().Provider)
}

func TestJudge_JevWithoutItsKeyFailsNamingIt(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-meant-for-claude")
	api := newJevAPI(t)
	fx := newJudgeFixture(t, "climate change", "climate change summit")

	err := fx.judge(t, "--strategy", "jev", "--api-base", api.URL)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "TYPESAFE_API_KEY")
	assert.Empty(t, api.requests())
	assert.NoFileExists(t, filepath.Join(fx.track, "trec", "annotations.jev.yaml"))
}

func TestJudge_JevResumeAsksOnlyAboutUngradedDocs(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "ts-cli-test-key")
	api := newJevAPI(t)
	fx := newJudgeFixture(t, "climate change", "climate change summit", "climate report")
	missing := fx.pool.Queries[0].Docs[1].DocID
	article := fx.articles[missing]
	delete(fx.articles, missing)
	require.Error(t, fx.judge(t, "--strategy", "jev", "--api-base", api.URL), "one doc left unjudged")
	fx.articles[missing] = article
	askedBefore := len(api.requests())

	require.NoError(t, fx.judge(t, "--strategy", "jev", "--api-base", api.URL, "--resume"))

	resumed := api.requests()[askedBefore:]
	require.Len(t, resumed, 1)
	assert.Contains(t, resumed[0].State, article.Title)
	assert.Equal(t, judgment.GradeRelevant, gradesByQuery(fx.annotations(t, "jev"))["q1"][missing])
}
