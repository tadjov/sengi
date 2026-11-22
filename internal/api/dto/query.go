package dto

import (
	"bytes"
	"encoding/json"

	"github.com/tadjov/sengi/internal/apperr"
	"github.com/tadjov/sengi/internal/types/query"
)

// SearchRequest wraps exactly one query type: {"size": N, "cursor": "...", "query": {"<type>": {...}}}.
type SearchRequest struct {
	Size   int          `json:"size,omitempty" validate:"omitempty,min=1"`
	Cursor string       `json:"cursor,omitempty"`
	Query  QueryWrapper `json:"query"`
}

type SearchResponse struct {
	NextCursor   *string               `json:"next_cursor,omitempty"`
	HasMore      bool                  `json:"has_more"`
	MaxScore     float64               `json:"max_score,omitempty"`
	PageMaxScore float64               `json:"page_max_score,omitempty"`
	TotalMatches int64                 `json:"total_matches,omitempty"`
	Hits         []ArticleSearchResult `json:"hits"`
	// Effective is the contract the search ran under, with every omitted
	// parameter filled by the engine.
	Effective query.Effective `json:"effective"`
	// Ignored lists the stated parameters the engine did not honour.
	Ignored []query.Ignored `json:"ignored"`
}

// CapabilitiesResponse is what the running backend serves and honours.
type CapabilitiesResponse struct {
	QueryTypes QueryTypes         `json:"query_types"`
	FullText   query.Capabilities `json:"full_text"`
}

// QueryTypes reports which query types the running backend serves.
type QueryTypes struct {
	StringQuery bool `json:"query_string"`
	Match       bool `json:"match"`
	MultiMatch  bool `json:"multi_match"`
	Phrase      bool `json:"phrase"`
	Boolean     bool `json:"boolean"`
	Semantic    bool `json:"semantic"`
	Hybrid      bool `json:"hybrid"`
}

// QueryWrapper holds exactly one query type; decoding rejects none or several.
type QueryWrapper struct {
	Match      *MatchParams      `json:"match,omitempty"`
	MultiMatch *MultiMatchParams `json:"multi_match,omitempty"`
	Phrase     *PhraseParams     `json:"phrase,omitempty"`
	Boolean    *BooleanParams    `json:"boolean,omitempty"`
	Hybrid     *HybridParams     `json:"hybrid,omitempty"`
}

type MatchParams struct {
	Query     string `json:"query" validate:"required,min=1"`
	Field     string `json:"field" validate:"required"`
	Operator  string `json:"operator,omitempty"`
	Fuzziness string `json:"fuzziness,omitempty"`
	Language  string `json:"language,omitempty"`
}

type MultiMatchParams struct {
	Query string `json:"query" validate:"required,min=1"`
	// Fields name the fields to match, each optionally boosted as "title^3".
	Fields   []string `json:"fields" validate:"required,min=1"`
	Operator string   `json:"operator,omitempty"`
	Language string   `json:"language,omitempty"`
}

type PhraseParams struct {
	Query    string   `json:"query" validate:"required,min=1"`
	Fields   []string `json:"fields" validate:"required,min=1"`
	Slop     int      `json:"slop,omitempty" validate:"min=0,max=3"`
	Language string   `json:"language,omitempty"`
}

// StringQueryParams are the parameters of the GET query-string search.
type StringQueryParams struct {
	Query    string
	Language string
}

func (p *StringQueryParams) ToDomain() (*query.String, error) {
	if err := checkQueryLength("q", p.Query); err != nil {
		return nil, err
	}
	if p.Query == "" {
		return nil, apperr.NewValidation("q parameter is required")
	}
	lang, err := parseLanguage(p.Language)
	if err != nil {
		return nil, err
	}
	return query.NewString(p.Query, query.StringOptions{Language: lang}), nil
}

// HybridParams represents hybrid (lexical FTS + vector) query parameters.
type HybridParams struct {
	Query    string `json:"query" validate:"required,min=1"`
	Language string `json:"language,omitempty"`
	K        int    `json:"k,omitempty" validate:"omitempty,min=1"`
}

func (p *HybridParams) ToDomain() (*query.Hybrid, error) {
	if err := checkQueryLength("query", p.Query); err != nil {
		return nil, err
	}

	lang, err := parseLanguage(p.Language)
	if err != nil {
		return nil, err
	}
	return query.NewHybrid(p.Query, query.HybridOptions{Language: lang, K: p.K}), nil
}

func (p *MatchParams) ToDomain() (*query.Match, error) {
	if err := checkQueryLength("query", p.Query); err != nil {
		return nil, err
	}
	field, err := parseField(p.Field)
	if err != nil {
		return nil, err
	}

	op, err := parseOperator(p.Operator)
	if err != nil {
		return nil, err
	}
	fuzziness, err := parseFuzziness(p.Fuzziness)
	if err != nil {
		return nil, err
	}
	lang, err := parseLanguage(p.Language)
	if err != nil {
		return nil, err
	}
	match, err := query.NewMatch(field, p.Query, query.MatchOptions{Language: lang, Operator: op, Fuzziness: fuzziness})
	if err != nil {
		return nil, apperr.NewValidationWrap("invalid match query", err)
	}
	return match, nil
}

func (p *MultiMatchParams) ToDomain() (*query.MultiMatch, error) {
	if err := checkQueryLength("query", p.Query); err != nil {
		return nil, err
	}
	op, err := parseOperator(p.Operator)
	if err != nil {
		return nil, err
	}
	lang, err := parseLanguage(p.Language)
	if err != nil {
		return nil, err
	}
	newQuery, err := query.NewMultiMatch(p.Query, p.Fields, query.MultiMatchOptions{Language: lang, Operator: op})
	if err != nil {
		return nil, apperr.NewValidationWrap("invalid multi_match query", err)
	}

	return newQuery, nil
}

type BooleanParams struct {
	Expression string `json:"expression" validate:"required,min=1"`
	Language   string `json:"language,omitempty"`
}

func (p *BooleanParams) ToDomain() (*query.Boolean, error) {
	if err := checkQueryLength("expression", p.Expression); err != nil {
		return nil, err
	}

	lang, err := parseLanguage(p.Language)
	if err != nil {
		return nil, err
	}
	return query.NewBoolean(p.Expression, query.BooleanOptions{Language: lang}), nil
}

func (p *PhraseParams) ToDomain() (*query.Phrase, error) {
	if err := checkQueryLength("query", p.Query); err != nil {
		return nil, err
	}
	fields, err := parseFields(p.Fields)
	if err != nil {
		return nil, err
	}

	lang, err := parseLanguage(p.Language)
	if err != nil {
		return nil, err
	}
	newQuery, err := query.NewPhrase(p.Query, fields, query.PhraseOptions{Slop: p.Slop, Language: lang})
	if err != nil {
		return nil, apperr.NewValidationWrap("invalid phrase query", err)
	}

	return newQuery, nil
}

func (q *QueryWrapper) GetQueryType() query.Kind {
	if q.Match != nil {
		return query.MatchType
	}
	if q.MultiMatch != nil {
		return query.MultiMatchType
	}
	if q.Phrase != nil {
		return query.PhraseType
	}
	if q.Boolean != nil {
		return query.BooleanType
	}
	if q.Hybrid != nil {
		return query.HybridType
	}
	return ""
}

// SemanticParams are the parameters of the GET semantic search.
type SemanticParams struct {
	Query string
}

type SemanticSearchResponse struct {
	NextCursor *string   `json:"next_cursor,omitempty"`
	HasMore    bool      `json:"has_more"`
	Hits       []Article `json:"hits"`
}

func (p *SemanticParams) ToDomain() (*query.Semantic, error) {
	if err := checkQueryLength("q", p.Query); err != nil {
		return nil, err
	}
	if p.Query == "" {
		return nil, apperr.NewValidation("q parameter is required")
	}

	return query.NewSemantic(p.Query), nil
}

func (q *QueryWrapper) UnmarshalJSON(data []byte) error {
	type Alias QueryWrapper
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(q),
	}

	// A custom unmarshaller does not inherit the outer decoder's settings.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&aux); err != nil {
		return err
	}

	count := 0
	if q.Match != nil {
		count++
	}
	if q.MultiMatch != nil {
		count++
	}
	if q.Phrase != nil {
		count++
	}
	if q.Boolean != nil {
		count++
	}
	if q.Hybrid != nil {
		count++
	}

	if count == 0 {
		return apperr.NewValidation("query must specify one of: match, multi_match, phrase, boolean, hybrid")
	}
	if count > 1 {
		return apperr.NewValidation("query must specify only one query type")
	}

	return nil
}
