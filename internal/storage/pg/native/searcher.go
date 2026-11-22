package native

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tadjov/sengi/internal/api/dto"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/pg"
	"github.com/tadjov/sengi/internal/types/operator"
	dquery "github.com/tadjov/sengi/internal/types/query"
	"github.com/tadjov/sengi/internal/utils"
)

type Searcher struct {
	db *pgxpool.Pool
}

var articleColumns = pg.ArticleColumnList("")

func NewSearcher(pool *pg.ConnectionPool) *Searcher {
	return &Searcher{db: pool.Pool()}
}

// Capabilities reports every searchable field as isolated: each fills a weight
// band of its own. Postgres full-text search has no typo tolerance.
func (r *Searcher) Capabilities() dquery.Capabilities {
	return dquery.Capabilities{
		Engine:          "postgres",
		DefaultOperator: DefaultOperator,
		PhraseSlop:      true,
		Boosts:          true,
		Fields:          dquery.SearchableFields(),
	}
}

// SearchStringQuery matches the query's SearchContract against the weighted search_vector.
func (r *Searcher) SearchStringQuery(ctx context.Context, query *dquery.String, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	contract := query.Contract()
	op := operator.Resolve(contract.Operator, DefaultOperator)
	slog.Info("Executing pg query_string search",
		"query", query.Query,
		"fields", contract.Fields,
		"operator", op,
		"language", contract.Language,
		"has_cursor", baseOpts.Cursor != nil,
		"size", baseOpts.Size)

	return r.search(ctx, rankedSearch{
		method: "SearchStringQuery",
		where:  buildTsWhereClause(contract.Fields, contract.Language, op, 1),
		// The contract's boosts reach Elasticsearch only. Postgres ranks with its
		// default weights, as the benchmark's Postgres arms do.
		rank: buildRankExpression(nil, contract.Language, op, 1),
		arg:  query.Query,
	}, baseOpts)
}

func (r *Searcher) SearchField(ctx context.Context, query *dquery.Match, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	lang := query.GetLanguage()
	op := operator.Resolve(query.Operator, DefaultOperator)
	slog.Info("Executing pg match search",
		"query", query.Query,
		"field", query.Field,
		"operator", op,
		"language", lang,
		"has_cursor", baseOpts.Cursor != nil,
		"size", baseOpts.Size)

	fieldBoosts := query.Params().Fields
	return r.search(ctx, rankedSearch{
		method: "SearchField",
		where:  buildTsWhereClause(fieldBoosts, lang, op, 1),
		rank:   buildRankExpression(fieldBoosts, lang, op, 1),
		arg:    query.Query,
	}, baseOpts)
}

func (r *Searcher) SearchFields(ctx context.Context, query *dquery.MultiMatch, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	lang := query.GetLanguage()
	op := operator.Resolve(query.Operator, DefaultOperator)
	slog.Info("Executing pg multi_match search",
		"query", query.Query,
		"fields", query.Fields,
		"operator", op,
		"language", lang,
		"has_cursor", baseOpts.Cursor != nil,
		"size", baseOpts.Size)

	fieldBoosts := query.Params().Fields
	return r.search(ctx, rankedSearch{
		method: "SearchFields",
		where:  buildTsWhereClause(fieldBoosts, lang, op, 1),
		rank:   buildRankExpression(fieldBoosts, lang, op, 1),
		arg:    query.Query,
	}, baseOpts)
}

// SearchPhrase matches the phrase within the requested fields. Slop 0 uses
// phraseto_tsquery; a larger slop ORs every allowed distance between the
// phrase's lexemes, bound as a ready tsquery so they are not stemmed again.
func (r *Searcher) SearchPhrase(ctx context.Context, query *dquery.Phrase, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	lang := query.GetLanguage()
	slop := query.GetSlop()

	slog.Info("Executing pg phrase search",
		"query", query.Query,
		"fields", query.Fields,
		"slop", slop,
		"language", lang,
		"has_cursor", baseOpts.Cursor != nil,
		"size", baseOpts.Size)

	regconfig := lang.Analysis().PostgresConfig
	phraseExpr := fmt.Sprintf("phraseto_tsquery('%s'::regconfig, $1)", regconfig)
	phraseArg := query.Query
	if slop > 0 {
		var plainQuery string
		lexemeSQL := fmt.Sprintf("SELECT plainto_tsquery('%s'::regconfig, $1)::text", regconfig)
		if err := r.db.QueryRow(ctx, lexemeSQL, query.Query).Scan(&plainQuery); err != nil {
			return nil, fmt.Errorf("SearchPhrase: failed to tokenize phrase: %w", err)
		}
		if lexemes := quotedLexemes(plainQuery); len(lexemes) >= 2 {
			phraseExpr = "$1::tsquery"
			phraseArg = buildPhraseSlopQuery(lexemes, slop)
		}
	}

	return r.search(ctx, rankedSearch{
		method: "SearchPhrase",
		where:  buildPhraseFieldMatch(query.Fields, phraseExpr),
		rank:   fmt.Sprintf("ts_rank(search_vector, %s)", phraseExpr),
		arg:    phraseArg,
	}, baseOpts)
}

func (r *Searcher) SearchBoolean(ctx context.Context, query *dquery.Boolean, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	lang := query.GetLanguage()

	slog.Info("Executing pg boolean search",
		"expression", query.Expression,
		"language", lang,
		"has_cursor", baseOpts.Cursor != nil,
		"size", baseOpts.Size)

	tsquery, err := booleanTsQuery(query.Expression)
	if err != nil {
		return nil, fmt.Errorf("failed to parse boolean expression: %w", err)
	}

	match := buildBooleanMatch(tsquery, lang)
	return r.search(ctx, rankedSearch{
		method: "SearchBoolean",
		where:  match.where,
		rank:   match.rank,
		arg:    match.arg,
	}, baseOpts)
}

// rankedSearch is the WHERE clause and rank expression of one search, both
// reading arg as $1.
type rankedSearch struct {
	method string
	where  string
	rank   string
	arg    any
}

// search counts every match and its best score, then fetches the page after the cursor.
func (r *Searcher) search(ctx context.Context, search rankedSearch, baseOpts *dquery.BaseOptions) (*storage.SearchResult, error) {
	cursor, size := baseOpts.Cursor, baseOpts.Size

	var globalMaxScore float64
	var count int64
	maxSQL := fmt.Sprintf(`
		SELECT COALESCE(MAX(%s), 0.0) as max_score, COUNT(*)
		FROM articles
		WHERE %s
	`, search.rank, search.where)
	if err := r.db.QueryRow(ctx, maxSQL, search.arg).Scan(&globalMaxScore, &count); err != nil {
		return nil, fmt.Errorf("cannot fetch global max score: %w", err)
	}
	if count == 0 {
		return &storage.SearchResult{}, nil
	}

	var searchSQL string
	var args []any

	if cursor == nil {
		searchSQL = fmt.Sprintf(`
			SELECT
				%s,
				%s as rank
			FROM articles
			WHERE %s
			ORDER BY rank DESC, id DESC
			LIMIT $2
		`, articleColumns, search.rank, search.where)
		args = []any{search.arg, size + 1}
	} else {
		searchSQL = fmt.Sprintf(`
			SELECT
				%s,
				%s as rank
			FROM articles
			WHERE %s
			  AND (%s, id) < ($2, $3)
			ORDER BY rank DESC, id DESC
			LIMIT $4
		`, articleColumns, search.rank, search.where, search.rank)
		args = []any{search.arg, cursor.Score, cursor.ID, size + 1}
	}

	return r.fetchPage(ctx, pageQuery{
		method:       search.method,
		sql:          searchSQL,
		args:         args,
		size:         size,
		maxScore:     globalMaxScore,
		totalMatches: count,
	})
}

// pageQuery is a page of a ranked search whose SQL fetches size+1 rows: the
// extra row only signals that another page exists. maxScore and totalMatches
// describe every match, not just this page.
type pageQuery struct {
	method       string
	sql          string
	args         []any
	size         int
	maxScore     float64
	totalMatches int64
}

// fetchPage returns an empty page when the cursor sits past every match, which
// the match count, computed without the cursor, cannot tell.
func (r *Searcher) fetchPage(ctx context.Context, q pageQuery) (*storage.SearchResult, error) {
	rows, err := r.db.Query(ctx, q.sql, q.args...)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to execute page query: %w", q.method, err)
	}
	defer rows.Close()

	var hits []dto.ArticleSearchResult
	var rawScores []float64
	for rows.Next() {
		var rawScore float64
		article, err := pg.ScanArticle(rows, &rawScore)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", q.method, err)
		}
		hits = append(hits, dto.ArticleSearchResult{
			Article:         dto.NewArticle(article),
			Score:           utils.RoundFloat64(rawScore, dquery.ScoreDecimalPlaces),
			ScoreNormalized: normalizedScore(rawScore, q.maxScore),
		})
		rawScores = append(rawScores, rawScore)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: failed to read page rows: %w", q.method, err)
	}

	page := &storage.SearchResult{
		MaxScore:     utils.RoundFloat64(q.maxScore, dquery.ScoreDecimalPlaces),
		TotalMatches: q.totalMatches,
	}
	if len(hits) == 0 {
		return page, nil
	}

	page.HasMore = len(hits) > q.size
	if page.HasMore {
		hits, rawScores = hits[:q.size], rawScores[:q.size]
		page.NextCursor = &dquery.Cursor{
			Score: rawScores[len(rawScores)-1],
			ID:    hits[len(hits)-1].Article.ID,
		}
	}
	page.Hits = hits
	page.PageMaxScore = utils.RoundFloat64(rawScores[0], dquery.ScoreDecimalPlaces)

	slog.Info("PG search page fetched", "page_hits", len(hits), "total_matches", q.totalMatches)
	return page, nil
}

// normalizedScore is 0 when maxScore is not positive, NaN included, where the
// ratio would be NaN or out of range.
func normalizedScore(rawScore, maxScore float64) float64 {
	if !(maxScore > 0) {
		return 0
	}
	return utils.RoundFloat64(rawScore/maxScore, dquery.ScoreDecimalPlaces)
}

var (
	_ storage.FullTextBackend     = (*Searcher)(nil)
	_ storage.StringQuerySearcher = (*Searcher)(nil)
	_ storage.FieldSearcher       = (*Searcher)(nil)
	_ storage.MultiFieldSearcher  = (*Searcher)(nil)
	_ storage.PhraseSearcher      = (*Searcher)(nil)
	_ storage.BooleanSearcher     = (*Searcher)(nil)
)
