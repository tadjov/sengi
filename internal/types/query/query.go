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
	Query string

	// Language overrides the contract's analysis language when set.
	Language language.Code
}

// Contract returns the default search contract with the query's overrides applied.
func (q *String) Contract() SearchContract {
	contract := DefaultSearchContract()
	if q.Language != "" {
		contract.Language = q.Language
	}
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

func (q *Boolean) GetLanguage() language.Code {
	if q.Language == "" {
		return DefaultLanguage
	}
	return q.Language
}

// Effective reports the contract fields, which an expression matches, and the
// operator that joins adjacent words.
func (q *Boolean) Effective(engineDefault operator.Operator) Effective {
	contract := DefaultSearchContract()
	contract.Language = q.GetLanguage()
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

type PhraseOption func(q *Phrase)

// NewPhrase rejects empty text, no fields, a field outside SearchableFields and
// a slop above MaxPhraseSlop.
func NewPhrase(query string, fields []Field, opts ...PhraseOption) (*Phrase, error) {
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

	q := &Phrase{
		Query:    query,
		Fields:   fields,
		Language: DefaultLanguage,
	}

	for _, opt := range opts {
		opt(q)
	}

	if q.Slop < 0 || q.Slop > MaxPhraseSlop {
		return nil, fmt.Errorf("slop must be between 0 and %d, got %d", MaxPhraseSlop, q.Slop)
	}

	return q, nil
}

func WithPhraseSlop(slop int) PhraseOption {
	return func(q *Phrase) {
		q.Slop = slop
	}
}

func WithPhraseLanguage(lang language.Code) PhraseOption {
	return func(q *Phrase) {
		q.Language = lang
	}
}

func (q *Phrase) GetLanguage() language.Code {
	if q.Language == "" {
		return DefaultLanguage
	}
	return q.Language
}

func (q *Phrase) Params() Params {
	return Params{Fields: unboostedFields(q.Fields), Slop: q.GetSlop()}
}

// Apply narrows the query to params, a subset of its own Params.
func (q *Phrase) Apply(params Params) {
	q.Fields = fieldWeights(params.Fields).fields()
	q.Slop = params.Slop
}

func (q *Phrase) Effective() Effective {
	return Effective{Fields: unboostedFields(q.Fields).fields(), Language: q.GetLanguage()}
}

// GetSlop clamps Slop to 0..MaxPhraseSlop.
func (q *Phrase) GetSlop() int {
	if q.Slop < 0 {
		return 0
	}
	if q.Slop > MaxPhraseSlop {
		return MaxPhraseSlop
	}
	return q.Slop
}

var (
	DefaultFields = []string{"title", "description", "content"}

	DefaultFieldWeights = map[string]float64{
		"title":       1.0,
		"description": 1.0,
		"content":     1.0,
	}
)

type StringOption func(q *String)

func NewQueryString(query string, opts ...StringOption) *String {
	q := &String{Query: query}
	for _, opt := range opts {
		opt(q)
	}
	return q
}

func WithQueryStringLanguage(lang language.Code) StringOption {
	return func(q *String) {
		q.Language = lang
	}
}

// Match searches one field.
type Match struct {
	Query    string
	Field    Field
	Language language.Code
	// Operator combines the terms; nil leaves the engine's default.
	Operator  *operator.Operator
	Fuzziness Fuzziness
}

func (q *Match) GetLanguage() language.Code {
	if q.Language == "" {
		return DefaultLanguage
	}
	return q.Language
}

func (q *Match) Params() Params {
	return Params{Fields: unboostedFields([]Field{q.Field}), Fuzziness: q.GetFuzziness()}
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
		Language: q.GetLanguage(),
	}
}

func (q *Match) GetFuzziness() Fuzziness {
	if q.Fuzziness == "" {
		return NoFuzziness
	}
	return q.Fuzziness
}

type MatchQueryOption func(q *Match)

// NewMatch rejects empty text and a field outside SearchableFields.
func NewMatch(field Field, query string, opts ...MatchQueryOption) (*Match, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if _, err := ParseField(string(field)); err != nil {
		return nil, err
	}
	q := &Match{
		Field:     field,
		Query:     query,
		Language:  DefaultLanguage,
		Fuzziness: NoFuzziness,
	}

	for _, opt := range opts {
		opt(q)
	}

	return q, nil
}

func WithMatchLanguage(lang language.Code) MatchQueryOption {
	return func(q *Match) {
		q.Language = lang
	}
}

func WithMatchOperator(op operator.Operator) MatchQueryOption {
	return func(q *Match) {
		q.Operator = &op
	}
}

func WithMatchFuzziness(fuzziness Fuzziness) MatchQueryOption {
	return func(q *Match) {
		q.Fuzziness = fuzziness
	}
}

type MultiMatchField struct {
	Name   Field
	Weight float64
}

func NewMultiMatchBoostedField(name Field, weight float64) MultiMatchField {
	return MultiMatchField{
		Name:   name,
		Weight: weight,
	}
}

// MultiMatch searches several fields, each with its own ranking weight.
type MultiMatch struct {
	Query    string
	Fields   []MultiMatchField
	Language language.Code
	// Operator combines the terms; nil leaves the engine's default.
	Operator *operator.Operator
}

type MultiMatchQueryOption func(q *MultiMatch)

func NewMultiMatchQuery(query string, fields []string, opts ...MultiMatchQueryOption) (*MultiMatch, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("fields are required")
	}

	parsedFields, err := newMultiMatchFields(fields)
	if err != nil {
		return nil, err
	}

	q := &MultiMatch{
		Query:    query,
		Language: DefaultLanguage,
		Fields:   parsedFields,
	}

	for _, opt := range opts {
		opt(q)
	}

	return q, nil
}

func WithMultiMatchLanguage(lang language.Code) MultiMatchQueryOption {
	return func(q *MultiMatch) {
		q.Language = lang
	}
}

func WithMultiMatchOperator(op operator.Operator) MultiMatchQueryOption {
	return func(q *MultiMatch) {
		q.Operator = &op
	}
}

func newMultiMatchFields(specs []string) ([]MultiMatchField, error) {
	fields := make([]MultiMatchField, 0, len(specs))
	for _, spec := range specs {
		fieldBoost, err := ParseFieldBoost(spec)
		if err != nil {
			return nil, err
		}
		fields = append(fields, NewMultiMatchBoostedField(fieldBoost.Field, fieldBoost.Weight))
	}
	return fields, nil
}

func (q *MultiMatch) GetLanguage() language.Code {
	if q.Language == "" {
		return DefaultLanguage
	}
	return q.Language
}

func (q *MultiMatch) Params() Params {
	return Params{Fields: q.fieldWeights()}
}

// Apply narrows the query to params, a subset of its own Params.
func (q *MultiMatch) Apply(params Params) {
	fields := make([]MultiMatchField, 0, len(params.Fields))
	for _, f := range params.Fields {
		fields = append(fields, NewMultiMatchBoostedField(f.Field, f.Weight))
	}
	q.Fields = fields
}

func (q *MultiMatch) Effective(engineDefault operator.Operator) Effective {
	return Effective{
		Operator: operator.Resolve(q.Operator, engineDefault),
		Fields:   q.fieldWeights().fields(),
		Language: q.GetLanguage(),
	}
}

func (q *MultiMatch) fieldWeights() fieldWeights {
	weights := make(fieldWeights, 0, len(q.Fields))
	for _, f := range q.Fields {
		weights = append(weights, FieldWeight{Field: f.Name, Weight: f.Weight})
	}
	return weights
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

type HybridOption func(q *Hybrid)

func NewHybrid(query string, opts ...HybridOption) *Hybrid {
	q := &Hybrid{
		Query:    query,
		Language: DefaultLanguage,
		K:        DefaultRRFConstant,
	}

	for _, opt := range opts {
		opt(q)
	}

	return q
}

func WithHybridLanguage(lang language.Code) HybridOption {
	return func(q *Hybrid) {
		q.Language = lang
	}
}

func WithHybridK(k int) HybridOption {
	return func(q *Hybrid) {
		q.K = k
	}
}

func (q *Hybrid) GetLanguage() language.Code {
	if q.Language == "" {
		return DefaultLanguage
	}
	return q.Language
}

// Effective leaves Fields empty: each engine's lexical leg picks its own.
func (q *Hybrid) Effective(engineDefault operator.Operator) Effective {
	return Effective{Operator: engineDefault, Language: q.GetLanguage()}
}

func (q *Hybrid) GetK() int {
	if q.K <= 0 {
		return DefaultRRFConstant
	}
	return q.K
}
