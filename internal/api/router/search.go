package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/tadjov/sengi/internal/api/dto"
	"github.com/tadjov/sengi/internal/apperr"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/types/operator"
	dquery "github.com/tadjov/sengi/internal/types/query"
)

type SearchRouter struct {
	e                   *echo.Echo
	searcher            storage.FullTextBackend
	stringQuerySearcher storage.StringQuerySearcher
	fieldSearcher       storage.FieldSearcher
	multiFieldSearcher  storage.MultiFieldSearcher
	phraseSearcher      storage.PhraseSearcher
	booleanSearcher     storage.BooleanSearcher
	semanticSearcher    storage.SemanticSearcher
	hybridSearcher      storage.HybridSearcher
}

// Searchers are the backends a SearchRouter serves. FullText serves each
// paradigm whose searcher interface it implements; a nil Semantic or Hybrid
// leaves that paradigm unserved.
type Searchers struct {
	FullText storage.FullTextBackend
	Semantic storage.SemanticSearcher
	Hybrid   storage.HybridSearcher
}

func NewSearchRouter(e *echo.Echo, searchers Searchers) *SearchRouter {
	return &SearchRouter{
		e:                e,
		searcher:         searchers.FullText,
		semanticSearcher: searchers.Semantic,
		hybridSearcher:   searchers.Hybrid,
	}
}

func (r *SearchRouter) Bind() {
	r.stringQuerySearcher, _ = r.searcher.(storage.StringQuerySearcher)
	r.fieldSearcher, _ = r.searcher.(storage.FieldSearcher)
	r.multiFieldSearcher, _ = r.searcher.(storage.MultiFieldSearcher)
	r.phraseSearcher, _ = r.searcher.(storage.PhraseSearcher)
	r.booleanSearcher, _ = r.searcher.(storage.BooleanSearcher)

	if r.stringQuerySearcher != nil {
		r.e.GET("/v1/articles/search", r.searchHandler)
	}
	r.e.POST("/v1/articles/_search", r.structuredSearchHandler)
	if r.semanticSearcher != nil {
		r.e.GET("/v1/articles/semantic_search", r.semanticSearchHandler)
	}
	r.e.GET("/v1/capabilities", r.capabilitiesHandler)
}

// @Summary Report supported query types and parameters
// @Description Returns the query types the running backend serves and the full-text parameters its engine honours: the operator it applies when a request names none, whether it supports fuzziness, phrase slop and boosts, and the fields it can match apart from every other field. A stated parameter outside these is a 400 unless the request sends `Prefer: handling=lenient`.
// @Tags capabilities
// @Produce json
// @Success 200 {object} dto.CapabilitiesResponse "Supported query types and parameters"
// @Router /v1/capabilities [get]
func (r *SearchRouter) capabilitiesHandler(c echo.Context) error {
	return c.JSON(http.StatusOK, dto.CapabilitiesResponse{
		QueryTypes: dto.QueryTypes{
			StringQuery: r.stringQuerySearcher != nil,
			Match:       r.fieldSearcher != nil,
			MultiMatch:  r.multiFieldSearcher != nil,
			Phrase:      r.phraseSearcher != nil,
			Boolean:     r.booleanSearcher != nil,
			Semantic:    r.semanticSearcher != nil,
			Hybrid:      r.hybridSearcher != nil,
		},
		FullText: r.searcher.Capabilities(),
	})
}

// preferredHandling reads the request's Prefer handling and confirms a lenient
// one in Preference-Applied.
func preferredHandling(c echo.Context) dquery.Handling {
	handling := dto.HandlingFromPrefer(c.Request().Header.Values("Prefer"))
	if handling == dquery.HandlingLenient {
		c.Response().Header().Set("Preference-Applied", "handling=lenient")
	}
	return handling
}

// paramQuery is a query whose stated params an engine may not honour.
type paramQuery interface {
	Params() dquery.Params
	Apply(dquery.Params)
}

// honour checks the params of q against what the engine can honour: an error
// under strict handling; under lenient handling q loses them before it runs
// and they are returned as ignored.
func (r *SearchRouter) honour(q paramQuery, handling dquery.Handling) ([]dquery.Ignored, error) {
	honoured, ignored, err := dquery.CheckCapabilities(r.searcher.Capabilities(), q.Params(), handling)
	if err != nil {
		return nil, apperr.NewValidationWrap(err.Error(), err)
	}
	q.Apply(honoured)
	return ignored, nil
}

// @Summary Simple query string search
// @Description Free-text search over title, description and content: operators and quotes are read as words. Terms combine under the engine's default operator (PostgreSQL AND, Elasticsearch OR); the response echoes it in `effective`. Any query parameter not listed here is a 400. Cacheable and bookmarkable.
// @Tags search
// @Accept json
// @Produce json
// @Param q query string true "Query text" example("climate change")
// @Param size query int false "Results per page (default: 100, max: 10000)" example(10)
// @Param cursor query string false "Pagination cursor (base64-encoded from previous response)"
// @Param lang query string false "ISO 639-3 language code; only eng today (default: eng)" example("eng")
// @Param Prefer header string false "handling=lenient drops a parameter the engine cannot honour and lists it in ignored, instead of a 400"
// @Success 200 {object} dto.SearchResponse "Search results with pagination metadata"
// @Failure 400 {object} map[string]string "Bad request - missing or invalid parameters"
// @Failure 500 {object} map[string]string "Internal server error"
// @Router /v1/articles/search [get]
func (r *SearchRouter) searchHandler(c echo.Context) error {
	if err := dto.CheckQueryParams(c.QueryParams(), "q", "size", "cursor", "lang"); err != nil {
		return err
	}

	params := dto.StringQueryParams{
		Query:    c.QueryParam("q"),
		Language: c.QueryParam("lang"),
	}
	queryString, err := params.ToDomain()
	if err != nil {
		return err
	}

	page, err := parsePage(c.QueryParam("size"), c.QueryParam("cursor"))
	if err != nil {
		return err
	}

	preferredHandling(c)

	searchResult, err := r.stringQuerySearcher.SearchStringQuery(c.Request().Context(), queryString, page)
	if err != nil {
		slog.ErrorContext(c.Request().Context(), "Failed to execute full-text search", "error", err, "query", params.Query)
		return err
	}

	return r.buildResponse(c, searchResult, contractReport{
		effective: queryString.Effective(r.defaultOperator()),
	})
}

// parsePage reads the size and cursor query parameters, each empty when unset.
func parsePage(size, cursor string) (*dquery.BaseOptions, error) {
	pageSize, err := dto.ParsePageSize(size)
	if err != nil {
		return nil, err
	}
	position, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	return &dquery.BaseOptions{Cursor: position, Size: pageSize}, nil
}

func decodeCursor(cursor string) (*dquery.Cursor, error) {
	position, err := dquery.DecodeCursor(cursor)
	if err != nil {
		return nil, apperr.NewValidation("invalid cursor parameter")
	}
	return position, nil
}

func encodeCursor(position *dquery.Cursor) (*string, error) {
	if position == nil {
		return nil, nil
	}
	encoded, err := dquery.EncodeCursor(position.Score, position.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to encode cursor: %w", err)
	}
	return &encoded, nil
}

// @Summary Structured search API
// @Description Structured match, multi_match, phrase, boolean or hybrid search. A stated operator, field, fuzziness or language is honoured by the engine or the request is a 400 naming the parameter and the engine. An omitted operator takes the engine's default (PostgreSQL AND, Elasticsearch OR). Boosts are best effort. A key the query type does not define is a 400, and so is a query type the backend does not serve. With `Prefer: handling=lenient` an unsupported parameter is removed before the search runs and listed in `ignored`. Every response echoes `effective` and `ignored`.
// @Tags search
// @Accept json
// @Produce json
// @Param request body dto.SearchRequest true "Structured search request with query wrapper"
// @Param Prefer header string false "handling=lenient drops a parameter the engine cannot honour and lists it in ignored, instead of a 400"
// @Success 200 {object} dto.SearchResponse "Search results with pagination metadata"
// @Failure 400 {object} map[string]string "Bad request - invalid query structure or parameters, or a query type the backend does not serve"
// @Failure 500 {object} map[string]string "Internal server error"
// @Router /v1/articles/_search [post]
func (r *SearchRouter) structuredSearchHandler(c echo.Context) error {
	if ctype := c.Request().Header.Get(echo.HeaderContentType); !strings.HasPrefix(ctype, echo.MIMEApplicationJSON) {
		return echo.ErrUnsupportedMediaType
	}
	req, err := dto.DecodeSearchRequest(c.Request().Body)
	if err != nil {
		// The body limit surfaces as a read error and keeps its own status.
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) {
			return httpErr
		}
		return err
	}

	if err := c.Validate(req); err != nil {
		return err
	}

	size, err := dto.PageSize(req.Size)
	if err != nil {
		return err
	}
	cursor, err := decodeCursor(req.Cursor)
	if err != nil {
		return err
	}
	page := &dquery.BaseOptions{Cursor: cursor, Size: size}

	switch queryType := req.Query.GetQueryType(); queryType {
	case dquery.MatchType:
		if r.fieldSearcher == nil {
			return notEnabled(queryType)
		}
		return runStructured(r, c, page, req.Query.Match, r.fieldSearcher.SearchField,
			func(q *dquery.Match) dquery.Effective { return q.Effective(r.defaultOperator()) })
	case dquery.MultiMatchType:
		if r.multiFieldSearcher == nil {
			return notEnabled(queryType)
		}
		return runStructured(r, c, page, req.Query.MultiMatch, r.multiFieldSearcher.SearchFields,
			func(q *dquery.MultiMatch) dquery.Effective { return q.Effective(r.defaultOperator()) })
	case dquery.PhraseType:
		if r.phraseSearcher == nil {
			return notEnabled(queryType)
		}
		return runStructured(r, c, page, req.Query.Phrase, r.phraseSearcher.SearchPhrase, (*dquery.Phrase).Effective)
	case dquery.BooleanType:
		if r.booleanSearcher == nil {
			return notEnabled(queryType)
		}
		return runStructured(r, c, page, req.Query.Boolean, r.booleanSearcher.SearchBoolean,
			func(q *dquery.Boolean) dquery.Effective { return q.Effective(r.defaultOperator()) })
	case dquery.HybridType:
		if r.hybridSearcher == nil {
			return notEnabled(queryType)
		}
		return runStructured(r, c, page, req.Query.Hybrid, r.hybridSearcher.SearchHybrid,
			func(q *dquery.Hybrid) dquery.Effective { return q.Effective(r.defaultOperator()) })
	default:
		return apperr.NewValidation("query must specify one of: match, multi_match, phrase, boolean, hybrid")
	}
}

func notEnabled(queryType dquery.Kind) error {
	return apperr.NewValidation(fmt.Sprintf("%s search is not enabled on this server", queryType))
}

// runStructured converts params to its domain query, checks the query against
// the engine's capabilities when it states params the engine may not honour,
// runs it and reports the contract it ran under.
func runStructured[P interface{ ToDomain() (Q, error) }, Q any](
	r *SearchRouter,
	c echo.Context,
	page *dquery.BaseOptions,
	params P,
	search func(context.Context, Q, *dquery.BaseOptions) (*storage.SearchResult, error),
	effective func(Q) dquery.Effective,
) error {
	domainQuery, err := params.ToDomain()
	if err != nil {
		return err
	}

	handling := preferredHandling(c)
	var ignored []dquery.Ignored
	if stated, ok := any(domainQuery).(paramQuery); ok {
		if ignored, err = r.honour(stated, handling); err != nil {
			return err
		}
	}

	searchResult, err := search(c.Request().Context(), domainQuery, page)
	if err != nil {
		slog.ErrorContext(c.Request().Context(), "Failed to execute structured search", "error", err, "params", params)
		return err
	}

	return r.buildResponse(c, searchResult, contractReport{
		effective: effective(domainQuery),
		ignored:   ignored,
	})
}

// @Summary Vector-based semantic search
// @Description Vector similarity search over the article embeddings, with the query embedded by the model the corpus was loaded with. Any query parameter not listed here is a 400. Routed only when the backend serves semantic search.
// @Tags search
// @Produce json
// @Param q query string true "Query text" example("climate change")
// @Param size query int false "Results per page (default: 100, max: 10000)" example(10)
// @Param cursor query string false "Pagination cursor (base64-encoded from previous response)"
// @Success 200 {object} dto.SemanticSearchResponse "Search results with pagination metadata"
// @Failure 400 {object} map[string]string "Bad request - missing or invalid parameters"
// @Failure 500 {object} map[string]string "Internal server error"
// @Router /v1/articles/semantic_search [get]
func (r *SearchRouter) semanticSearchHandler(c echo.Context) error {
	if err := dto.CheckQueryParams(c.QueryParams(), "q", "size", "cursor"); err != nil {
		return err
	}

	params := dto.SemanticParams{Query: c.QueryParam("q")}
	domainQuery, err := params.ToDomain()
	if err != nil {
		return err
	}

	page, err := parsePage(c.QueryParam("size"), c.QueryParam("cursor"))
	if err != nil {
		return err
	}

	searchResult, err := r.semanticSearcher.SearchSemantic(c.Request().Context(), domainQuery, page)
	if err != nil {
		slog.ErrorContext(c.Request().Context(), "Failed to execute semantic search", "error", err, "query", params.Query)
		return err
	}

	nextCursor, err := encodeCursor(searchResult.NextCursor)
	if err != nil {
		return err
	}

	hits := searchResult.Hits
	if hits == nil {
		hits = []dto.Article{}
	}

	return c.JSON(http.StatusOK, dto.SemanticSearchResponse{
		Hits:       hits,
		NextCursor: nextCursor,
		HasMore:    searchResult.HasMore,
	})
}

func (r *SearchRouter) defaultOperator() operator.Operator {
	return r.searcher.Capabilities().DefaultOperator
}

// contractReport is what a search response says about the contract it ran under.
type contractReport struct {
	effective dquery.Effective
	ignored   []dquery.Ignored
}

func (r *SearchRouter) buildResponse(c echo.Context, searchResult *storage.SearchResult, report contractReport) error {
	nextCursor, err := encodeCursor(searchResult.NextCursor)
	if err != nil {
		return err
	}

	hits := searchResult.Hits
	if hits == nil {
		hits = []dto.ArticleSearchResult{}
	}

	ignored := report.ignored
	if ignored == nil {
		ignored = []dquery.Ignored{}
	}

	return c.JSON(http.StatusOK, dto.SearchResponse{
		Hits:         hits,
		NextCursor:   nextCursor,
		HasMore:      searchResult.HasMore,
		MaxScore:     searchResult.MaxScore,
		PageMaxScore: searchResult.PageMaxScore,
		TotalMatches: searchResult.TotalMatches,
		Effective:    report.effective,
		Ignored:      ignored,
	})
}
