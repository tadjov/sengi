package judgment

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/suite"
)

func TestGateAdmits(t *testing.T) {
	tests := []struct {
		name  string
		query GradingQuery
		doc   GradingDoc
		want  bool
	}{
		{
			name:  "a phrase doc lacking the phrase",
			query: GradingQuery{ID: "q", Text: "climate change", Category: suite.CategoryPhrase},
			doc:   GradingDoc{Title: "change to the climate"},
			want:  false,
		},
		{
			name:  "a phrase doc holding the phrase",
			query: GradingQuery{ID: "q", Text: "climate change", Category: suite.CategoryPhrase},
			doc:   GradingDoc{Title: "climate change summit"},
			want:  true,
		},
		{
			name:  "a boolean doc holding a negated term",
			query: GradingQuery{ID: "q", Text: "climate AND NOT politics", Category: suite.CategoryBoolean},
			doc:   GradingDoc{Title: "climate politics"},
			want:  false,
		},
		{
			name:  "a keyword doc from another publisher than the filter's",
			query: GradingQuery{ID: "q", Text: "climate", Filters: suite.Filters{PublisherDomain: "reuters.com"}},
			doc:   GradingDoc{Title: "climate", PublisherDomain: "bbc.co.uk"},
			want:  false,
		},
		{
			name:  "a keyword doc without a gate",
			query: GradingQuery{ID: "q", Text: "climate"},
			doc:   GradingDoc{Title: "weather"},
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admitted, err := GateAdmits(tt.query, tt.doc)
			require.NoError(t, err)
			assert.Equal(t, tt.want, admitted)
		})
	}
}

func TestGateAdmits_RefusesAQueryWithoutTerms(t *testing.T) {
	_, err := GateAdmits(GradingQuery{ID: "q", Text: "will not", Category: suite.CategoryPhrase}, GradingDoc{Title: "will not"})

	var noTerms *NoQueryTermsError
	assert.ErrorAs(t, err, &noTerms)
}
