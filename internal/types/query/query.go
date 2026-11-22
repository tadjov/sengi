package query

import (
	"fmt"

	"github.com/tadjov/sengi/internal/types/language"
	"github.com/tadjov/sengi/internal/types/operator"
)

type Kind string

const (
	MatchType      Kind = "match"
	MultiMatchType Kind = "multi_match"
	BooleanType    Kind = "boolean"
	PhraseType     Kind = "phrase"
	// HybridType fuses lexical FTS with vector similarity via RRF.
	HybridType Kind = "hybrid"
)

// String is a free-text query whose fields, weights and operator come from the
// SearchContract rather than from the caller.
type String struct {
	Query    string
	Language language.Code
}

type StringOptions struct {
	// Language defaults to DefaultLanguage.
	Language language.Code
}

func NewString(query string, opts StringOptions) *String {
	return &String{Query: query, Language: languageOrDefault(opts.Language)}
}

func languageOrDefault(lang language.Code) language.Code {
	if lang == "" {
		return DefaultLanguage
	}
	return lang
}

// Contract is the default search contract in the query's language.
func (q *String) Contract() SearchContract {
	contract := DefaultSearchContract()
	contract.Language = q.Language
	return contract
}

func (q *String) Effective(engineDefault operator.Operator) Effective {
	return q.Contract().Effective(engineDefault)
}

// Boolean is a structured query using logical operators.
type Boolean struct {
	// Expression combines terms with AND, OR, NOT and parentheses, as in
	// "(renewable OR sustainable) AND NOT coal".
	Expression string
	Language   language.Code
}

type BooleanOptions struct {
	// Language defaults to DefaultLanguage.
	Language language.Code
}

func NewBoolean(expression string, opts BooleanOptions) *Boolean {
	return &Boolean{Expression: expression, Language: languageOrDefault(opts.Language)}
}

// Effective reports the contract fields, which an expression matches, and the
// operator that joins adjacent words.
func (q *Boolean) Effective(engineDefault operator.Operator) Effective {
	contract := DefaultSearchContract()
	contract.Language = q.Language
	return contract.Effective(engineDefault)
}

// Phrase matches the words of Query in order.
type Phrase struct {
	Query  string
	Fields []Field
	// Slop is how many positions may separate the words: 0 is the exact
	// phrase, MaxPhraseSlop the most.
	Slop     int
	Language language.Code
}

const MaxPhraseSlop = 3

type PhraseOptions struct {
	Slop int
	// Language defaults to DefaultLanguage.
	Language language.Code
}

// NewPhrase rejects empty text, no fields, a field outside SearchableFields and
// a slop outside 0..MaxPhraseSlop.
func NewPhrase(query string, fields []Field, opts PhraseOptions) (*Phrase, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("fields are required")
	}
	for _, field := range fields {
		if _, err := ParseField(string(field)); err != nil {
			return nil, err
		}
	}
	if opts.Slop < 0 || opts.Slop > MaxPhraseSlop {
		return nil, fmt.Errorf("slop must be between 0 and %d, got %d", MaxPhraseSlop, opts.Slop)
	}
	return &Phrase{Query: query, Fields: fields, Slop: opts.Slop, Language: languageOrDefault(opts.Language)}, nil
}

func (q *Phrase) Params() Params {
	return Params{Fields: unboostedFields(q.Fields), Slop: q.Slop}
}

// Apply narrows the query to params, a subset of its own Params.
func (q *Phrase) Apply(params Params) {
	q.Fields = fieldWeights(params.Fields).fields()
	q.Slop = params.Slop
}

func (q *Phrase) Effective() Effective {
	return Effective{Fields: unboostedFields(q.Fields).fields(), Language: q.Language}
}

var (
	DefaultFields = []string{"title", "description", "content"}

	DefaultFieldWeights = map[string]float64{
		"title":       1.0,
		"description": 1.0,
		"content":     1.0,
	}
)

// Match searches one field.
type Match struct {
	Query    string
	Field    Field
	Language language.Code
	// Operator combines the terms; nil leaves the engine's default.
	Operator  *operator.Operator
	Fuzziness Fuzziness
}

type MatchOptions struct {
	// Language defaults to DefaultLanguage.
	Language language.Code
	// Operator combines the terms; nil leaves the engine's default.
	Operator *operator.Operator
	// Fuzziness defaults to NoFuzziness.
	Fuzziness Fuzziness
}

// NewMatch rejects empty text and a field outside SearchableFields.
func NewMatch(field Field, query string, opts MatchOptions) (*Match, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if _, err := ParseField(string(field)); err != nil {
		return nil, err
	}
	fuzziness := opts.Fuzziness
	if fuzziness == "" {
		fuzziness = NoFuzziness
	}
	return &Match{
		Field:     field,
		Query:     query,
		Language:  languageOrDefault(opts.Language),
		Operator:  opts.Operator,
		Fuzziness: fuzziness,
	}, nil
}

func (q *Match) Params() Params {
	return Params{Fields: unboostedFields([]Field{q.Field}), Fuzziness: q.Fuzziness}
}

// Apply narrows the query to params, a subset of its own Params. Its one field
// always survives: a check that would drop it rejects the request instead.
func (q *Match) Apply(params Params) {
	q.Fuzziness = params.Fuzziness
}

func (q *Match) Effective(engineDefault operator.Operator) Effective {
	return Effective{
		Operator: operator.Resolve(q.Operator, engineDefault),
		Fields:   []Field{q.Field},
		Language: q.Language,
	}
}

// MultiMatch searches several fields, each with its own ranking weight.
type MultiMatch struct {
	Query    string
	Fields   []FieldWeight
	Language language.Code
	// Operator combines the terms; nil leaves the engine's default.
	Operator *operator.Operator
}

type MultiMatchOptions struct {
	// Language defaults to DefaultLanguage.
	Language language.Code
	// Operator combines the terms; nil leaves the engine's default.
	Operator *operator.Operator
}

// NewMultiMatch reads each field as "name" or "name^weight" and rejects empty
// text, no fields and a field outside SearchableFields.
func NewMultiMatch(query string, fields []string, opts MultiMatchOptions) (*MultiMatch, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("fields are required")
	}
	weighted := make([]FieldWeight, 0, len(fields))
	for _, spec := range fields {
		fieldBoost, err := ParseFieldBoost(spec)
		if err != nil {
			return nil, err
		}
		weighted = append(weighted, FieldWeight{Field: fieldBoost.Field, Weight: fieldBoost.Weight})
	}
	return &MultiMatch{
		Query:    query,
		Fields:   weighted,
		Language: languageOrDefault(opts.Language),
		Operator: opts.Operator,
	}, nil
}

func (q *MultiMatch) Params() Params {
	return Params{Fields: q.Fields}
}

// Apply narrows the query to params, a subset of its own Params.
func (q *MultiMatch) Apply(params Params) {
	q.Fields = params.Fields
}

func (q *MultiMatch) Effective(engineDefault operator.Operator) Effective {
	return Effective{
		Operator: operator.Resolve(q.Operator, engineDefault),
		Fields:   fieldWeights(q.Fields).fields(),
		Language: q.Language,
	}
}

type Semantic struct {
	Query     string
	Threshold float64
}

func NewSemantic(query string) *Semantic {
	return &Semantic{
		Query: query,
	}
}

// DefaultRRFConstant is the standard RRF constant (k=60 per Cormack et al.).
const DefaultRRFConstant = 60

// Hybrid fuses lexical FTS with vector similarity via RRF.
// score = 1/(k + lex_rank) + 1/(k + vec_rank)
type Hybrid struct {
	Query    string
	Language language.Code
	// K is the RRF constant; higher values flatten rank contribution differences.
	K int
}

type HybridOptions struct {
	// Language defaults to DefaultLanguage.
	Language language.Code
	// K defaults to DefaultRRFConstant.
	K int
}

func NewHybrid(query string, opts HybridOptions) *Hybrid {
	k := opts.K
	if k <= 0 {
		k = DefaultRRFConstant
	}
	return &Hybrid{Query: query, Language: languageOrDefault(opts.Language), K: k}
}

// Effective leaves Fields empty: each engine's lexical leg picks its own.
func (q *Hybrid) Effective(engineDefault operator.Operator) Effective {
	return Effective{Operator: engineDefault, Language: q.Language}
}
