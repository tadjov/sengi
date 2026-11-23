package ingest

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"hash"
	"slices"
	"strconv"

	"github.com/tadjov/sengi/internal/datapackage"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/pkg/apis/mapping"
	"github.com/tadjov/sengi/pkg/apis/preprocess"
)

// signalAccumulator records, for each kept article, the group a signal puts it in and
// the value it counts, as digests, so the whole corpus never sits in memory.
type signalAccumulator struct {
	spec     preprocess.Signal
	group    []int
	distinct int
	keyHash  hash.Hash
	members  []signalMember
}

type signalMember struct {
	group       digest
	distinct    digest
	grouped     bool
	hasDistinct bool
}

func newSignalAccumulator(spec preprocess.Signal, digests *fieldDigests) *signalAccumulator {
	s := &signalAccumulator{spec: spec, distinct: digests.index(spec.Distinct), keyHash: sha256.New()}
	if spec.Within != "" {
		s.group = append(s.group, digests.index(spec.Within))
	}
	s.group = append(s.group, digests.index(spec.Sharing))
	return s
}

// add records the kept article whose digests are computed; an empty sharing value puts it in no group.
func (s *signalAccumulator) add(digests *fieldDigests) {
	member := signalMember{}
	sharing := s.group[len(s.group)-1]
	if digests.filled[sharing] {
		member.grouped = true
		if len(s.group) == 1 {
			member.group = digests.sums[sharing]
		} else {
			s.keyHash.Reset()
			s.keyHash.Write(digests.sums[s.group[0]][:])
			s.keyHash.Write(digests.sums[sharing][:])
			s.keyHash.Sum(member.group[:0])
		}
	}
	if digests.filled[s.distinct] {
		member.distinct, member.hasDistinct = digests.sums[s.distinct], true
	}
	s.members = append(s.members, member)
}

// signalGroup is one group of kept articles sharing a value; first is its earliest article.
type signalGroup struct {
	first    int
	value    int64
	articles int64
}

// signalValues is one signal over the kept articles: values[k] is 0 when article k is in no group.
type signalValues struct {
	spec   preprocess.Signal
	values []int64
	groups []signalGroup
}

func (s *signalAccumulator) values() signalValues {
	order := make([]int, 0, len(s.members))
	for k, m := range s.members {
		if m.grouped {
			order = append(order, k)
		}
	}
	slices.SortFunc(order, func(a, b int) int {
		ma, mb := s.members[a], s.members[b]
		if c := bytes.Compare(ma.group[:], mb.group[:]); c != 0 {
			return c
		}
		if c := bytes.Compare(ma.distinct[:], mb.distinct[:]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})

	result := signalValues{spec: s.spec, values: make([]int64, len(s.members))}
	for start := 0; start < len(order); {
		end, first, distinct := start, order[start], int64(0)
		for ; end < len(order) && s.members[order[end]].group == s.members[order[start]].group; end++ {
			member := s.members[order[end]]
			if member.hasDistinct && (end == start || !s.members[order[end-1]].hasDistinct || s.members[order[end-1]].distinct != member.distinct) {
				distinct++
			}
			first = min(first, order[end])
		}
		for _, k := range order[start:end] {
			result.values[k] = distinct
		}
		result.groups = append(result.groups, signalGroup{first: first, value: distinct, articles: int64(end - start)})
		start = end
	}
	return result
}

const (
	topSignalGroups   = 10
	signalSampleRunes = 160
)

// signalThresholds are the values AtLeast counts articles at, for choosing a rule's threshold.
var signalThresholds = []int64{2, 3, 5, 10, 20, 50, 100, 1000}

// stats describes the distribution; samples fills in the top groups' text later.
func (v signalValues) stats() datapackage.SignalStats {
	stats := datapackage.SignalStats{Distinct: v.spec.Distinct, Sharing: v.spec.Sharing, Within: v.spec.Within}
	sorted := make([]int64, 0, len(v.values))
	for _, value := range v.values {
		if value > 0 {
			sorted = append(sorted, value)
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
	for _, threshold := range signalThresholds {
		if threshold > stats.Max {
			break
		}
		below, _ := slices.BinarySearch(sorted, threshold)
		stats.AtLeast[strconv.FormatInt(threshold, 10)] = int64(len(sorted) - below)
	}
	for _, group := range v.topGroups() {
		stats.Top = append(stats.Top, datapackage.SignalGroup{Value: group.value, Articles: group.articles})
	}
	return stats
}

// percentile is the nearest-rank percentile of sorted values.
func percentile(sorted []int64, p float64) int64 {
	rank := int(float64(len(sorted))*p+0.999999) - 1
	return sorted[max(0, min(rank, len(sorted)-1))]
}

func (v signalValues) topGroups() []signalGroup {
	top := slices.Clone(v.groups)
	slices.SortFunc(top, func(a, b signalGroup) int {
		return cmp.Or(cmp.Compare(b.value, a.value), cmp.Compare(b.articles, a.articles), cmp.Compare(a.first, b.first))
	})
	return top[:min(len(top), topSignalGroups)]
}

// signalSamples fills in the text of each top group, in place, from its earliest article.
type signalSamples struct {
	spec  preprocess.Signal
	byKey map[int]int
	top   []datapackage.SignalGroup
}

func newSignalSamples(v signalValues, top []datapackage.SignalGroup) signalSamples {
	samples := signalSamples{spec: v.spec, byKey: map[int]int{}, top: top}
	for i, group := range v.topGroups() {
		samples.byKey[group.first] = i
	}
	return samples
}

func (s signalSamples) capture(k int, article document.Article) {
	i, ok := s.byKey[k]
	if !ok {
		return
	}
	group := &s.top[i]
	group.Sharing = sample(mapping.TextValue(article, s.spec.Sharing))
	if s.spec.Within != "" {
		group.Within = sample(mapping.TextValue(article, s.spec.Within))
	}
}

// sample is value with whitespace collapsed, cut to signalSampleRunes.
func sample(value string) string {
	runes := []rune(string(appendCollapsed(nil, value)))
	if len(runes) <= signalSampleRunes {
		return string(runes)
	}
	return string(runes[:signalSampleRunes]) + "…"
}
