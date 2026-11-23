package ingest

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"hash"
	"slices"
	"strconv"
	"strings"

	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/pkg/apis/mapping"
	"github.com/tadjov/sengi/pkg/apis/preprocess"
	"github.com/tadjov/sengi/pkg/datapackage"
)

// groupCounter records, for each kept article, the group a condition puts it in and the
// value it counts, as digests, so the whole corpus never sits in memory.
type groupCounter struct {
	condition preprocess.Condition
	groupBy   []int
	distinct  int
	keyHash   hash.Hash
	members   []groupMember
}

type groupMember struct {
	group       digest
	distinct    digest
	grouped     bool
	hasDistinct bool
}

// countKey names what a condition counts, so rules that count the same share one counter.
func countKey(c preprocess.Condition) string {
	return c.CountDistinct + " by " + strings.Join(c.GroupBy, ",")
}

func newGroupCounter(condition preprocess.Condition, digests *fieldDigests) *groupCounter {
	g := &groupCounter{condition: condition, distinct: digests.index(condition.CountDistinct), keyHash: sha256.New()}
	for _, field := range condition.GroupBy {
		g.groupBy = append(g.groupBy, digests.index(field))
	}
	return g
}

// add records the kept article whose digests are computed; any empty groupBy value puts it in no group.
func (g *groupCounter) add(digests *fieldDigests) {
	member := groupMember{}
	member.group, member.grouped = digests.combined(g.keyHash, g.groupBy)
	if digests.filled[g.distinct] {
		member.distinct, member.hasDistinct = digests.sums[g.distinct], true
	}
	g.members = append(g.members, member)
}

// countedGroup is one group of kept articles; first is its earliest article.
type countedGroup struct {
	first    int
	distinct int64
	articles int64
}

// groupCounts is a counter's result: counts[k] is the distinct count of article k's group, 0 when it is in no group.
type groupCounts struct {
	condition preprocess.Condition
	counts    []int64
	groups    []countedGroup
}

func (g *groupCounter) count() groupCounts {
	order := make([]int, 0, len(g.members))
	for k, m := range g.members {
		if m.grouped {
			order = append(order, k)
		}
	}
	slices.SortFunc(order, func(a, b int) int {
		ma, mb := g.members[a], g.members[b]
		if c := bytes.Compare(ma.group[:], mb.group[:]); c != 0 {
			return c
		}
		if c := bytes.Compare(ma.distinct[:], mb.distinct[:]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})

	result := groupCounts{condition: g.condition, counts: make([]int64, len(g.members))}
	for start := 0; start < len(order); {
		end, first, distinct := start, order[start], int64(0)
		for ; end < len(order) && g.members[order[end]].group == g.members[order[start]].group; end++ {
			member := g.members[order[end]]
			if member.hasDistinct && (end == start || !g.members[order[end-1]].hasDistinct || g.members[order[end-1]].distinct != member.distinct) {
				distinct++
			}
			first = min(first, order[end])
		}
		for _, k := range order[start:end] {
			result.counts[k] = distinct
		}
		result.groups = append(result.groups, countedGroup{first: first, distinct: distinct, articles: int64(end - start)})
		start = end
	}
	return result
}

const (
	topGroups        = 10
	groupSampleRunes = 160
)

// atLeastBuckets are the counts RuleStats.AtLeast reports, for choosing a rule's threshold.
var atLeastBuckets = []int64{2, 3, 5, 10, 20, 50, 100, 1000}

// stats describes the distribution; groupSamples fills in the top groups' values later.
func (c groupCounts) stats() datapackage.RuleStats {
	stats := datapackage.RuleStats{CountDistinct: c.condition.CountDistinct, GroupBy: c.condition.GroupBy}
	sorted := make([]int64, 0, len(c.counts))
	for _, count := range c.counts {
		if count > 0 {
			sorted = append(sorted, count)
		}
	}
	if len(sorted) == 0 {
		return stats
	}
	slices.Sort(sorted)
	stats.Articles = int64(len(sorted))
	stats.Median, stats.P90, stats.P99 = percentile(sorted, 0.5), percentile(sorted, 0.9), percentile(sorted, 0.99)
	stats.Max = sorted[len(sorted)-1]
	stats.AtLeast = map[string]int64{}
	for _, bucket := range atLeastBuckets {
		if bucket > stats.Max {
			break
		}
		below, _ := slices.BinarySearch(sorted, bucket)
		stats.AtLeast[strconv.FormatInt(bucket, 10)] = int64(len(sorted) - below)
	}
	for _, group := range c.topGroups() {
		stats.Top = append(stats.Top, datapackage.Group{CountDistinct: group.distinct, Articles: group.articles})
	}
	return stats
}

// percentile is the nearest-rank percentile of sorted values.
func percentile(sorted []int64, p float64) int64 {
	rank := int(float64(len(sorted))*p+0.999999) - 1
	return sorted[max(0, min(rank, len(sorted)-1))]
}

func (c groupCounts) topGroups() []countedGroup {
	top := slices.Clone(c.groups)
	slices.SortFunc(top, func(a, b countedGroup) int {
		return cmp.Or(cmp.Compare(b.distinct, a.distinct), cmp.Compare(b.articles, a.articles), cmp.Compare(a.first, b.first))
	})
	return top[:min(len(top), topGroups)]
}

// groupSamples fills in the values of each top group, in place, from its earliest article.
type groupSamples struct {
	condition preprocess.Condition
	byArticle map[int]int
	top       []datapackage.Group
}

func newGroupSamples(c groupCounts, top []datapackage.Group) groupSamples {
	samples := groupSamples{condition: c.condition, byArticle: map[int]int{}, top: top}
	for i, group := range c.topGroups() {
		samples.byArticle[group.first] = i
	}
	return samples
}

func (s groupSamples) capture(k int, article document.Article) {
	i, ok := s.byArticle[k]
	if !ok {
		return
	}
	values := make([]string, len(s.condition.GroupBy))
	for j, field := range s.condition.GroupBy {
		values[j] = sample(mapping.TextValue(article, field))
	}
	s.top[i].Values = values
}

// sample is value with whitespace collapsed, cut to groupSampleRunes.
func sample(value string) string {
	runes := []rune(string(appendCollapsed(nil, value)))
	if len(runes) <= groupSampleRunes {
		return string(runes)
	}
	return string(runes[:groupSampleRunes]) + "…"
}
