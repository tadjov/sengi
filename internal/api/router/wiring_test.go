package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	apiserver "github.com/tadjov/sengi/internal/api/server"
	"github.com/tadjov/sengi/internal/apperr"
	"github.com/tadjov/sengi/internal/storage"
	dquery "github.com/tadjov/sengi/internal/types/query"
)

type capabilitiesOnly struct{}

func (capabilitiesOnly) Capabilities() dquery.Capabilities {
	return elasticsearchCaps
}

type stringQueryOnly struct{ capabilitiesOnly }

func (stringQueryOnly) SearchStringQuery(context.Context, *dquery.String, *dquery.BaseOptions) (*storage.SearchResult, error) {
	return &storage.SearchResult{}, nil
}

type matchOnly struct{ capabilitiesOnly }

func (matchOnly) SearchField(context.Context, *dquery.Match, *dquery.BaseOptions) (*storage.SearchResult, error) {
	return &storage.SearchResult{}, nil
}

type multiMatchOnly struct{ capabilitiesOnly }

func (multiMatchOnly) SearchFields(context.Context, *dquery.MultiMatch, *dquery.BaseOptions) (*storage.SearchResult, error) {
	return &storage.SearchResult{}, nil
}

type phraseOnly struct{ capabilitiesOnly }

func (phraseOnly) SearchPhrase(context.Context, *dquery.Phrase, *dquery.BaseOptions) (*storage.SearchResult, error) {
	return &storage.SearchResult{}, nil
}

type booleanOnly struct{ capabilitiesOnly }

func (booleanOnly) SearchBoolean(context.Context, *dquery.Boolean, *dquery.BaseOptions) (*storage.SearchResult, error) {
	return &storage.SearchResult{}, nil
}

// queryTypeRequests holds one valid request per query type, named by its
// /v1/capabilities key.
var queryTypeRequests = []struct {
	queryType string
	method    string
	target    string
	body      string
}{
	{queryType: "query_string", method: http.MethodGet, target: "/v1/articles/search?q=climate"},
	{queryType: "match", method: http.MethodPost, target: "/v1/articles/_search", body: `{"query":{"match":{"field":"title","query":"climate"}}}`},
	{queryType: "multi_match", method: http.MethodPost, target: "/v1/articles/_search", body: `{"query":{"multi_match":{"fields":["title","content"],"query":"climate"}}}`},
	{queryType: "phrase", method: http.MethodPost, target: "/v1/articles/_search", body: `{"query":{"phrase":{"fields":["title"],"query":"climate change"}}}`},
	{queryType: "boolean", method: http.MethodPost, target: "/v1/articles/_search", body: `{"query":{"boolean":{"expression":"climate AND change"}}}`},
	{queryType: "semantic", method: http.MethodGet, target: "/v1/articles/semantic_search?q=climate"},
	{queryType: "hybrid", method: http.MethodPost, target: "/v1/articles/_search", body: `{"query":{"hybrid":{"query":"climate"}}}`},
}

func TestSearchRouterServesOnlyTheParadigmItIsWiredWith(t *testing.T) {
	backends := []struct {
		queryType string
		backend   storage.FullTextBackend
	}{
		{queryType: "query_string", backend: stringQueryOnly{}},
		{queryType: "match", backend: matchOnly{}},
		{queryType: "multi_match", backend: multiMatchOnly{}},
		{queryType: "phrase", backend: phraseOnly{}},
		{queryType: "boolean", backend: booleanOnly{}},
	}

	for _, b := range backends {
		t.Run(b.queryType, func(t *testing.T) {
			e := echo.New()
			(&apiserver.Server{Echo: e}).SetupValidator()
			e.HTTPErrorHandler = apperr.GlobalErrorHandler()
			e.Use(middleware.Recover())
			NewSearchRouter(e, b.backend).Bind()

			for _, call := range queryTypeRequests {
				req := httptest.NewRequest(call.method, call.target, strings.NewReader(call.body))
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)

				switch {
				case call.queryType == b.queryType:
					if rec.Code != http.StatusOK {
						t.Errorf("%s: status = %d, want 200 (body: %s)", call.queryType, rec.Code, rec.Body.String())
					}
				case call.method == http.MethodGet:
					if rec.Code != http.StatusNotFound {
						t.Errorf("%s: status = %d, want 404 (body: %s)", call.queryType, rec.Code, rec.Body.String())
					}
				default:
					var got struct {
						Error string `json:"error"`
					}
					_ = json.Unmarshal(rec.Body.Bytes(), &got)
					if rec.Code != http.StatusBadRequest || !strings.Contains(got.Error, call.queryType) {
						t.Errorf("%s: status = %d, error = %q, want 400 naming %s", call.queryType, rec.Code, got.Error, call.queryType)
					}
				}
			}

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil))
			var capabilities struct {
				QueryTypes map[string]bool `json:"query_types"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &capabilities); err != nil {
				t.Fatalf("decode capabilities: %v (body: %s)", err, rec.Body.String())
			}
			for _, call := range queryTypeRequests {
				if want := call.queryType == b.queryType; capabilities.QueryTypes[call.queryType] != want {
					t.Errorf("query_types[%q] = %v, want %v", call.queryType, capabilities.QueryTypes[call.queryType], want)
				}
			}
		})
	}
}
