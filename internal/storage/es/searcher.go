package es

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/elastic/go-elasticsearch/v9"
	"github.com/elastic/go-elasticsearch/v9/typedapi/esdsl"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/operator"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/sortorder"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/textquerytype"
	"github.com/tadjov/sengi/internal/api/dto"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/token"
	queryoperator "github.com/tadjov/sengi/internal/types/operator"
	dquery "github.com/tadjov/sengi/internal/types/query"
	"github.com/tadjov/sengi/internal/utils"
)

type Searcher struct {
	client    *elasticsearch.TypedClient
	indexName string
}

func NewSearcher(client *Client) *Searcher {
	return &Searcher{
		client:    client.typed,
		indexName: client.indexName,
	}
}

// DefaultOperator is what Elasticsearch's match and multi_match apply when a
// request names no operator.
const DefaultOperator = queryoperator.Or

// Capabilities reports every searchable field as isolated: each is its own
// field in the index mapping.
func (r *Searcher) Capabilities() dquery.Capabilities {
	return dquery.Capabilities{
		Engine:          "elasticsearch",
		DefaultOperator: DefaultOperator,
		Fuzziness:       true,
		PhraseSlop:      true,
		Boosts:          true,
		Fields:          dquery.SearchableFields(),
	}
}

// SearchStringQuery matches the query's SearchContract with a cross_fields
// multi_match, so each term may hit any contract field, as it does in the
// Postgres search_vector. The contract language is not applied: analysis is
// fixed per field by the index mapping.
func (r *Searcher) SearchStringQuery(ctx context.Context, query *dquery.String, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	contract := query.Contract()

	slog.Info("Executing es query_string search",
		"query", query.Query,
		"fields", contract.Fields,
		"operator", queryoperator.Resolve(contract.Operator, DefaultOperator),
		"language", contract.Language,
		"has_cursor", baseOpts.Cursor != nil,
		"size", baseOpts.Size)

	return r.search(ctx, "query_string", &types.Query{
		MultiMatch: &types.MultiMatchQuery{
			Query:    query.Query,
			Fields:   boostedFields(contract.Fields),
			Type:     &textquerytype.Crossfields,
			Operator: termOperator(contract.Operator),
		},
	}, baseOpts)
}

func (r *Searcher) SearchField(ctx context.Context, query *dquery.Match, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	slog.Info("Executing es match search",
		"query", query.Query,
		"field", query.Field,
		"operator", queryoperator.Resolve(query.Operator, DefaultOperator),
		"fuzziness", query.GetFuzziness(),
		"has_cursor", baseOpts.Cursor != nil,
		"size", baseOpts.Size)

	matchQuery := types.MatchQuery{
		Query:    query.Query,
		Operator: termOperator(query.Operator),
	}
	if fuzziness := query.GetFuzziness(); fuzziness != dquery.NoFuzziness {
		editDistance := string(fuzziness)
		matchQuery.Fuzziness = &editDistance
	}

	return r.search(ctx, "match", &types.Query{
		Match: map[string]types.MatchQuery{string(query.Field): matchQuery},
	}, baseOpts)
}

func (r *Searcher) SearchFields(ctx context.Context, query *dquery.MultiMatch, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	slog.Info("Executing es multi_match search",
		"query", query.Query,
		"fields", query.Fields,
		"operator", queryoperator.Resolve(query.Operator, DefaultOperator),
		"has_cursor", baseOpts.Cursor != nil,
		"size", baseOpts.Size)

	// cross_fields lets each term match any of the fields, as the Postgres
	// search_vector does; the default best_fields wants every term in one.
	return r.search(ctx, "multi_match", &types.Query{
		MultiMatch: &types.MultiMatchQuery{
			Query:    query.Query,
			Fields:   boostedFields(query.Params().Fields),
			Type:     &textquerytype.Crossfields,
			Operator: termOperator(query.Operator),
		},
	}, baseOpts)
}

// SearchPhrase matches the phrase, within slop, in at least one of the fields.
func (r *Searcher) SearchPhrase(ctx context.Context, query *dquery.Phrase, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	slop := query.GetSlop()

	slog.Info("Executing es phrase search",
		"query", query.Query,
		"fields", query.Fields,
		"slop", slop,
		"language", query.GetLanguage(),
		"has_cursor", baseOpts.Cursor != nil,
		"size", baseOpts.Size)

	perField := make([]types.Query, 0, len(query.Fields))
	for _, field := range query.Fields {
		matchPhrase := types.MatchPhraseQuery{Query: query.Query}
		if slop > 0 {
			matchPhrase.Slop = &slop
		}
		perField = append(perField, types.Query{
			MatchPhrase: map[string]types.MatchPhraseQuery{string(field): matchPhrase},
		})
	}

	return r.search(ctx, "phrase", &types.Query{
		Bool: &types.BoolQuery{Should: perField, MinimumShouldMatch: "1"},
	}, baseOpts)
}

func (r *Searcher) SearchBoolean(ctx context.Context, query *dquery.Boolean, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	slog.Info("Executing es boolean search",
		"expression", query.Expression,
		"has_cursor", baseOpts.Cursor != nil,
		"size", baseOpts.Size)

	if err := token.Validate(token.Tokenize(query.Expression)); err != nil {
		return nil, fmt.Errorf("invalid boolean query expression: %w", err)
	}

	return r.search(ctx, "boolean", &types.Query{
		QueryString: &types.QueryStringQuery{
			Query:  query.Expression,
			Fields: boostedFields(dquery.DefaultSearchContract().Fields),
		},
	}, baseOpts)
}

// search ranks by score, then id, and fetches one hit past the page to learn whether another follows.
func (r *Searcher) search(ctx context.Context, kind string, query *types.Query, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	cursor, size := baseOpts.Cursor, baseOpts.Size

	request := r.client.Search().
		Index(r.indexName).
		SourceExcludes_("embedding").
		Query(query).
		Size(size + 1).
		TrackScores(true).
		TrackTotalHits(esdsl.NewTrackHits().Bool(true))

	if cursor != nil {
		request = request.SearchAfter(
			esdsl.NewFieldValue().Float64(types.Float64(cursor.Score)),
			esdsl.NewFieldValue().String(cursor.ID.String()),
		)
	}

	descending := sortorder.Desc
	request = request.Sort(
		&types.SortOptions{SortOptions: map[string]types.FieldSort{"_score": {Order: &descending}}},
		&types.SortOptions{SortOptions: map[string]types.FieldSort{"id": {Order: &descending}}},
	)

	res, err := request.Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to execute %s search: %w", kind, err)
	}

	page, err := searchResultPage(res.Hits, size)
	if err != nil {
		return nil, fmt.Errorf("failed to map search results to types: %w", err)
	}

	slog.Info("ES search results fetched",
		"kind", kind,
		"total_matches", page.TotalMatches,
		"returned_count", len(page.Hits),
		"max_score", page.MaxScore)

	return page, nil
}

func boostedFields(fields []dquery.FieldWeight) []string {
	boosted := make([]string, 0, len(fields))
	for _, f := range fields {
		if f.Weight == 1.0 {
			boosted = append(boosted, string(f.Field))
			continue
		}
		boosted = append(boosted, fmt.Sprintf("%s^%g", f.Field, f.Weight))
	}
	return boosted
}

// termOperator is nil when the request names no operator, so Elasticsearch
// applies its own default.
func termOperator(op *queryoperator.Operator) *operator.Operator {
	if op == nil {
		return nil
	}
	termOp := operator.And
	if op.IsOr() {
		termOp = operator.Or
	}
	return &termOp
}

// searchResultPage expects hits fetched with size+1: the extra hit only signals
// that another page exists.
func searchResultPage(hits types.HitsMetadata, size int) (*storage.SearchResult, error) {
	articles, rawScores, err := mapToResult(hits.Hits, dquery.CalcSafeScore((*float64)(hits.MaxScore)))
	if err != nil {
		return nil, err
	}

	hasMore := len(articles) > size
	if hasMore {
		articles = articles[:size]
		rawScores = rawScores[:size]
	}

	page := &storage.SearchResult{
		Hits:    articles,
		HasMore: hasMore,
	}
	if hits.Total != nil {
		page.TotalMatches = hits.Total.Value
	}
	if hits.MaxScore != nil {
		page.MaxScore = utils.RoundFloat64(float64(*hits.MaxScore), dquery.ScoreDecimalPlaces)
	}
	if len(articles) > 0 {
		page.PageMaxScore = utils.RoundFloat64(rawScores[0], dquery.ScoreDecimalPlaces)
	}
	if hasMore && len(articles) > 0 {
		page.NextCursor = &dquery.Cursor{
			Score: rawScores[len(rawScores)-1],
			ID:    articles[len(articles)-1].Article.ID,
		}
	}
	return page, nil
}

func mapToResult(hits []types.Hit, maxScore float64) ([]dto.ArticleSearchResult, []float64, error) {
	articles := make([]dto.ArticleSearchResult, 0, len(hits))
	rawScores := make([]float64, 0, len(hits))

	for _, hit := range hits {
		article, err := decodeArticle(hit.Source_)
		if err != nil {
			return nil, nil, err
		}
		if hit.Score_ == nil {
			return nil, nil, fmt.Errorf("document %s has no _score", article.ID)
		}

		rawScore := float64(*hit.Score_)
		articles = append(articles, dto.ArticleSearchResult{
			Article:         article,
			ScoreNormalized: rawScore / maxScore,
			Score:           rawScore,
		})
		rawScores = append(rawScores, rawScore)
	}

	return articles, rawScores, nil
}

var (
	_ storage.FullTextBackend     = (*Searcher)(nil)
	_ storage.StringQuerySearcher = (*Searcher)(nil)
	_ storage.FieldSearcher       = (*Searcher)(nil)
	_ storage.MultiFieldSearcher  = (*Searcher)(nil)
	_ storage.PhraseSearcher      = (*Searcher)(nil)
	_ storage.BooleanSearcher     = (*Searcher)(nil)
)
