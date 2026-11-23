package agreement

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/tadjov/sengi/internal/bench/suite"
	"github.com/tadjov/sengi/pkg/irmetrics"
)

// Statistic is a value that is NaN when undefined, and null in JSON.
type Statistic float64

func (s Statistic) MarshalJSON() ([]byte, error) {
	if math.IsNaN(float64(s)) {
		return []byte("null"), nil
	}
	return json.Marshal(float64(s))
}

// Kappa is a κ and its 95% bootstrap interval.
type Kappa struct {
	Value Statistic `json:"value"`
	Low   Statistic `json:"low"`
	High  Statistic `json:"high"`
}

// SummaryOptions configures Summarize.
type SummaryOptions struct {
	// Labels name the sets in the order of each pair's grades.
	Labels []string
	// Threshold is the lowest grade that counts as relevant.
	Threshold int
	Seed      uint64
	// Reference, when set, is the label the other sets are tabulated against.
	Reference  string
	Categories map[string]suite.Category
}

// Summary is how alike the sets grade one list of pairs.
type Summary struct {
	Pairs         int            `json:"pairs"`
	Distributions []Distribution `json:"distributions"`
	// Comparisons pair each set with every later one, in set order.
	Comparisons []Comparison `json:"comparisons"`
	Reference   *Reference   `json:"reference,omitempty"`
}

// Distribution counts one set's pairs at each grade, 0 to 3.
type Distribution struct {
	Label  string `json:"label"`
	Grades [4]int `json:"grades"`
}

// Comparison is the agreement of two sets over the pairs both graded.
type Comparison struct {
	First          string  `json:"first"`
	Second         string  `json:"second"`
	Pairs          int     `json:"pairs"`
	Exact          float64 `json:"exact"`
	WithinOne      float64 `json:"within_one"`
	Relevant       float64 `json:"relevant"`
	QuadraticKappa Kappa   `json:"quadratic_kappa"`
	BinaryKappa    Kappa   `json:"binary_kappa"`
}

// Reference tabulates every other set against one set.
type Reference struct {
	Label      string      `json:"label"`
	Against    []Confusion `json:"against"`
	ByCategory []Category  `json:"by_category"`
}

// Confusion is one set's grades against the reference's, rows being the reference's.
type Confusion struct {
	Label string                   `json:"label"`
	Table irmetrics.GradeConfusion `json:"table"`
	// GradedRelevant counts reference-irrelevant pairs graded relevant; GradedIrrelevant the reverse.
	ReferenceIrrelevant int `json:"reference_irrelevant"`
	GradedRelevant      int `json:"graded_relevant"`
	ReferenceRelevant   int `json:"reference_relevant"`
	GradedIrrelevant    int `json:"graded_irrelevant"`
}

// Category is the agreement over one query category's pairs.
type Category struct {
	Category    suite.Category `json:"category"`
	Queries     int            `json:"queries"`
	Pairs       int            `json:"pairs"`
	Comparisons []Comparison   `json:"comparisons"`
}

// Summarize measures every set's agreement over pairs, which must not be empty.
func Summarize(pairs []Pair, opts SummaryOptions) (Summary, error) {
	summary := Summary{
		Pairs:         len(pairs),
		Distributions: distributions(pairs, opts.Labels),
		Comparisons:   opts.compareAll(pairs),
	}
	if opts.Reference == "" {
		return summary, nil
	}
	reference := slices.Index(opts.Labels, opts.Reference)
	if reference < 0 {
		return Summary{}, fmt.Errorf("--reference %q names none of the sets %s", opts.Reference, strings.Join(opts.Labels, ", "))
	}
	summary.Reference = &Reference{
		Label:      opts.Reference,
		Against:    opts.confusions(pairs, reference),
		ByCategory: opts.byCategory(pairs),
	}
	return summary, nil
}

func distributions(pairs []Pair, labels []string) []Distribution {
	out := make([]Distribution, len(labels))
	for i, label := range labels {
		out[i].Label = label
		for _, p := range pairs {
			out[i].Grades[p.Grades[i]]++
		}
	}
	return out
}

// setPair indexes two sets in SummaryOptions.Labels.
type setPair struct {
	first, second int
}

func (o SummaryOptions) compareAll(pairs []Pair) []Comparison {
	byQuery := groupByQuery(pairs)
	var out []Comparison
	for i := range o.Labels {
		for j := i + 1; j < len(o.Labels); j++ {
			out = append(out, o.compare(byQuery, setPair{first: i, second: j}))
		}
	}
	return out
}

func (o SummaryOptions) compare(byQuery [][]Pair, sets setPair) Comparison {
	tables := make([]irmetrics.GradeConfusion, len(byQuery))
	var all irmetrics.GradeConfusion
	for q, queryPairs := range byQuery {
		for _, p := range queryPairs {
			tables[q][p.Grades[sets.first]][p.Grades[sets.second]]++
			all[p.Grades[sets.first]][p.Grades[sets.second]]++
		}
	}
	binary := func(c irmetrics.GradeConfusion) float64 { return c.BinaryKappa(o.Threshold) }
	return Comparison{
		First:          o.Labels[sets.first],
		Second:         o.Labels[sets.second],
		Pairs:          all.Pairs(),
		Exact:          all.ExactAgreement(),
		WithinOne:      all.WithinOneAgreement(),
		Relevant:       all.RelevantAgreement(o.Threshold),
		QuadraticKappa: kappa(irmetrics.BootstrapKappa(irmetrics.KappaBootstrap{ByQuery: tables, Kappa: irmetrics.GradeConfusion.QuadraticKappa, Seed: o.Seed})),
		BinaryKappa:    kappa(irmetrics.BootstrapKappa(irmetrics.KappaBootstrap{ByQuery: tables, Kappa: binary, Seed: o.Seed})),
	}
}

func kappa(interval irmetrics.KappaInterval) Kappa {
	return Kappa{Value: Statistic(interval.Kappa), Low: Statistic(interval.Low), High: Statistic(interval.High)}
}

// groupByQuery sorts queries by id, so a seed resamples the same queries whatever the input order.
func groupByQuery(pairs []Pair) [][]Pair {
	byID := make(map[string][]Pair)
	for _, p := range pairs {
		byID[p.QueryID] = append(byID[p.QueryID], p)
	}
	out := make([][]Pair, 0, len(byID))
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		out = append(out, byID[id])
	}
	return out
}

func (o SummaryOptions) confusions(pairs []Pair, reference int) []Confusion {
	var out []Confusion
	for other, label := range o.Labels {
		if other == reference {
			continue
		}
		c := Confusion{Label: label}
		for _, p := range pairs {
			referenceGrade, grade := p.Grades[reference], p.Grades[other]
			c.Table[referenceGrade][grade]++
			referenceRelevant, relevant := referenceGrade >= o.Threshold, grade >= o.Threshold
			if referenceRelevant {
				c.ReferenceRelevant++
				if !relevant {
					c.GradedIrrelevant++
				}
			} else {
				c.ReferenceIrrelevant++
				if relevant {
					c.GradedRelevant++
				}
			}
		}
		out = append(out, c)
	}
	return out
}

func (o SummaryOptions) byCategory(pairs []Pair) []Category {
	members := make(map[suite.Category][]Pair)
	for _, p := range pairs {
		category := o.Categories[p.QueryID]
		members[category] = append(members[category], p)
	}
	out := make([]Category, 0, len(members))
	for _, category := range slices.SortedFunc(maps.Keys(members), func(a, b suite.Category) int { return cmp.Compare(a, b) }) {
		categoryPairs := members[category]
		out = append(out, Category{
			Category:    category,
			Queries:     len(groupByQuery(categoryPairs)),
			Pairs:       len(categoryPairs),
			Comparisons: o.compareAll(categoryPairs),
		})
	}
	return out
}
