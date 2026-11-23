package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"time"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/bench/spec"
)

type EsExecutor struct {
	name    string
	baseURL string
	index   string
	client  *http.Client
	// defaultTimeout bounds a call whose caller set no deadline.
	defaultTimeout time.Duration
}

func NewEsExecutor(name, baseURL, index string) *EsExecutor {
	return &EsExecutor{
		name:    name,
		baseURL: baseURL,
		index:   index,
		client:  &http.Client{},

		defaultTimeout: spec.DefaultQueryTimeout,
	}
}

// withDeadline keeps the caller's deadline, or sets the default one: the client
// carries no timeout of its own.
func (e *EsExecutor) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, e.defaultTimeout)
}

func (e *EsExecutor) Execute(ctx context.Context, search Request) (*Execution, error) {
	url := fmt.Sprintf("%s/%s/_search", e.baseURL, e.index)
	body := []byte(search.Query)
	if search.SearchTemplateID != "" {
		url = fmt.Sprintf("%s/%s/_search/template", e.baseURL, e.index)
		var err error
		if body, err = storedTemplateCall(search); err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("es create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("es request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("es read response: %w", err)
	}
	latency := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("es status %d: %s", resp.StatusCode, string(respBody))
	}

	var esResp esSearchResponse
	if err := json.Unmarshal(respBody, &esResp); err != nil {
		return nil, fmt.Errorf("es parse response: %w", err)
	}

	ids := make([]uuid.UUID, 0, len(esResp.Hits.Hits))
	for _, hit := range esResp.Hits.Hits {
		id, err := hit.articleID()
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}

	return &Execution{
		RankedDocIDs:  ids,
		CorpusMatches: esResp.Hits.Total.exactCount(),
		Latency:       latency,
		ServerTime:    esResp.serverTime(),
	}, nil
}

func storedTemplateCall(search Request) ([]byte, error) {
	body, err := json.Marshal(map[string]any{"id": search.SearchTemplateID, "params": search.Params})
	if err != nil {
		return nil, fmt.Errorf("es encode template %q params: %w", search.SearchTemplateID, err)
	}
	return body, nil
}

// RegisterSearchTemplate stores the template's Mustache source with
// PUT _scripts/<id>, replacing any earlier version.
func (e *EsExecutor) RegisterSearchTemplate(ctx context.Context, template SearchTemplate) error {
	body, err := json.Marshal(map[string]any{
		"script": map[string]string{"lang": "mustache", "source": template.Source},
	})
	if err != nil {
		return fmt.Errorf("es encode template %q: %w", template.ID, err)
	}
	url := fmt.Sprintf("%s/_scripts/%s", e.baseURL, neturl.PathEscape(template.ID))
	if _, err := e.send(ctx, http.MethodPut, url, body); err != nil {
		return fmt.Errorf("es store template %q: %w", template.ID, err)
	}
	return nil
}

// renderTemplate returns the search body ES builds from a stored template and
// its params, without running it.
func (e *EsExecutor) renderTemplate(ctx context.Context, search Request) (string, error) {
	body, err := storedTemplateCall(search)
	if err != nil {
		return "", err
	}
	respBody, err := e.send(ctx, http.MethodPost, e.baseURL+"/_render/template", body)
	if err != nil {
		return "", fmt.Errorf("es render template %q: %w", search.SearchTemplateID, err)
	}
	var rendered struct {
		TemplateOutput json.RawMessage `json:"template_output"`
	}
	if err := json.Unmarshal(respBody, &rendered); err != nil {
		return "", fmt.Errorf("es render template %q: %w", search.SearchTemplateID, err)
	}
	return string(rendered.TemplateOutput), nil
}

func (e *EsExecutor) send(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

func (e *EsExecutor) CorpusCount(ctx context.Context) (int64, error) {
	ctx, cancel := e.withDeadline(ctx)
	defer cancel()
	body, err := e.send(ctx, http.MethodGet, fmt.Sprintf("%s/%s/_count", e.baseURL, e.index), nil)
	if err != nil {
		return 0, fmt.Errorf("es count: %w", err)
	}
	var c struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return 0, fmt.Errorf("es count parse: %w", err)
	}
	return c.Count, nil
}

// Validate has ES parse and type-check the query without scanning documents.
// A stored template is rendered with its params first. _validate/query knows
// only the Query DSL, so a top-level knn clause is checked structurally and a
// body it rejects for fields such as size is retried as its query block alone.
func (e *EsExecutor) Validate(ctx context.Context, search Request) error {
	ctx, cancel := e.withDeadline(ctx)
	defer cancel()
	rawQuery := search.Query
	if search.SearchTemplateID != "" {
		rendered, err := e.renderTemplate(ctx, search)
		if err != nil {
			return err
		}
		rawQuery = rendered
	}
	if hasKnn(rawQuery) {
		return e.validateKnnBody(ctx, rawQuery)
	}
	verdict, err := e.validateQuery(ctx, []byte(rawQuery))
	if err != nil || verdict.valid {
		return err
	}
	if verdict.explanation != "" {
		return verdict.err()
	}
	queryBlock, ok := stripToQueryBody(rawQuery)
	if !ok {
		return verdict.err()
	}
	return e.validateQueryBlock(ctx, queryBlock)
}

func (e *EsExecutor) validateQueryBlock(ctx context.Context, body []byte) error {
	verdict, err := e.validateQuery(ctx, body)
	if err != nil || verdict.valid {
		return err
	}
	return verdict.err()
}

// queryVerdict is what _validate/query said about a body.
type queryVerdict struct {
	valid       bool
	explanation string
	response    []byte
}

func (v queryVerdict) err() error {
	if v.explanation != "" {
		return fmt.Errorf("es invalid: %s", v.explanation)
	}
	return fmt.Errorf("es invalid: %s", v.response)
}

func (e *EsExecutor) validateQuery(ctx context.Context, body []byte) (queryVerdict, error) {
	url := fmt.Sprintf("%s/%s/_validate/query?explain=true", e.baseURL, e.index)
	response, err := e.send(ctx, http.MethodPost, url, body)
	if err != nil {
		return queryVerdict{}, fmt.Errorf("es validate: %w", err)
	}
	var v esValidateResponse
	if err := json.Unmarshal(response, &v); err != nil {
		return queryVerdict{}, fmt.Errorf("es validate parse: %w", err)
	}
	verdict := queryVerdict{valid: v.Valid, response: response}
	for _, explained := range v.Explanations {
		if explained.Error != "" {
			verdict.explanation = explained.Error
			break
		}
	}
	return verdict, nil
}

// stripToQueryBody keeps only the body's query block.
func stripToQueryBody(raw string) ([]byte, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, false
	}
	q, ok := m["query"]
	if !ok {
		return nil, false
	}
	out, err := json.Marshal(map[string]json.RawMessage{"query": q})
	if err != nil {
		return nil, false
	}
	return out, true
}

// hasKnn reports whether the search body carries a top-level `knn` clause.
func hasKnn(raw string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	_, ok := m["knn"]
	return ok
}

// validateKnnBody checks the knn clause structurally and any query block with ES.
func (e *EsExecutor) validateKnnBody(ctx context.Context, rawQuery string) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawQuery), &m); err != nil {
		return fmt.Errorf("es invalid: body is not JSON: %w", err)
	}
	if err := validateKnnClause(m["knn"]); err != nil {
		return fmt.Errorf("es invalid: %w", err)
	}
	if q, ok := m["query"]; ok {
		body, err := json.Marshal(map[string]json.RawMessage{"query": q})
		if err != nil {
			return fmt.Errorf("es invalid: %w", err)
		}
		return e.validateQueryBlock(ctx, body)
	}
	return nil
}

// validateKnnClause accepts a single knn object or an array of them.
func validateKnnClause(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("knn clause is empty")
	}
	var single map[string]json.RawMessage
	if err := json.Unmarshal(raw, &single); err == nil {
		return validateKnnEntry(single)
	}
	var many []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &many); err != nil {
		return fmt.Errorf("knn must be an object or array of objects")
	}
	if len(many) == 0 {
		return fmt.Errorf("knn array is empty")
	}
	for i, entry := range many {
		if err := validateKnnEntry(entry); err != nil {
			return fmt.Errorf("knn[%d]: %w", i, err)
		}
	}
	return nil
}

func validateKnnEntry(entry map[string]json.RawMessage) error {
	field, ok := entry["field"]
	if !ok {
		return fmt.Errorf("knn missing required \"field\"")
	}
	var fieldName string
	if err := json.Unmarshal(field, &fieldName); err != nil || fieldName == "" {
		return fmt.Errorf("knn \"field\" must be a non-empty string")
	}
	_, hasVec := entry["query_vector"]
	_, hasBuilder := entry["query_vector_builder"]
	if !hasVec && !hasBuilder {
		return fmt.Errorf("knn requires \"query_vector\" or \"query_vector_builder\"")
	}
	return nil
}

type esValidateResponse struct {
	Valid        bool                    `json:"valid"`
	Explanations []esValidateExplanation `json:"explanations,omitempty"`
}

type esValidateExplanation struct {
	Index string `json:"index"`
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}

type esSearchResponse struct {
	// Took is whole ms, so a search under 1 ms reads 0.
	Took *int64 `json:"took"`
	Hits esHits `json:"hits"`
}

func (r esSearchResponse) serverTime() *time.Duration {
	if r.Took == nil {
		return nil
	}
	took := time.Duration(*r.Took) * time.Millisecond
	return &took
}

type esHits struct {
	Total *esTotal `json:"total"`
	Hits  []esHit  `json:"hits"`
}

// esTotal is ES hits.total, absent when track_total_hits is false. Relation is
// "gte" when ES stopped counting at track_total_hits rather than counting every
// match.
type esTotal struct {
	Value    int64  `json:"value"`
	Relation string `json:"relation"`
}

func (t *esTotal) exactCount() *int64 {
	if t == nil || t.Relation == "gte" {
		return nil
	}
	v := t.Value
	return &v
}

type esHit struct {
	ID     string   `json:"_id"`
	Source esSource `json:"_source"`
}

// articleID reads the article id from _id, which the indexer sets to it, so a
// search can disable _source. _source.id covers an index whose _id is not the
// article id.
func (h esHit) articleID() (uuid.UUID, error) {
	if id, err := uuid.Parse(h.ID); err == nil {
		return id, nil
	}
	id, err := uuid.Parse(h.Source.ID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("es parse doc id: _id %q and _source.id %q are not uuids: %w", h.ID, h.Source.ID, err)
	}
	return id, nil
}

type esSource struct {
	ID string `json:"id"`
}
