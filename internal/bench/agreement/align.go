package agreement

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/tadjov/tusker/internal/bench/judgment"
)

// Pair is a (query, doc) every set graded, with each set's grade in set order.
type Pair struct {
	QueryID string    `json:"query_id"`
	DocID   uuid.UUID `json:"doc_id"`
	Grades  []int     `json:"grades"`
}

type pairKey struct {
	queryID string
	docID   uuid.UUID
}

// Align returns the pairs every set graded 0 to 3, sorted by query then doc.
// Sets match on (query, doc) alone: a sample is pooled on its own, so its
// pool_run_id never equals the full pool's.
func Align(sets []Set) ([]Pair, error) {
	grades := make([]map[pairKey]int, len(sets))
	for i, s := range sets {
		grades[i] = gradesOf(s.File)
	}
	var pairs []Pair
	for key := range grades[0] {
		pair := Pair{QueryID: key.queryID, DocID: key.docID, Grades: make([]int, len(sets))}
		gradedByAll := true
		for i := range sets {
			grade, ok := grades[i][key]
			gradedByAll = gradedByAll && ok
			pair.Grades[i] = grade
		}
		if gradedByAll {
			pairs = append(pairs, pair)
		}
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("%s share no (query, doc) pair graded 0 to 3 in every set", strings.Join(labels(sets), ", "))
	}
	slices.SortFunc(pairs, func(a, b Pair) int {
		return cmp.Or(cmp.Compare(a.QueryID, b.QueryID), compareDocs(a.DocID, b.DocID))
	})
	return pairs, nil
}

func gradesOf(file *judgment.File) map[pairKey]int {
	grades := make(map[pairKey]int)
	for _, entry := range file.Queries {
		for _, doc := range entry.Docs {
			if doc.Grade >= judgment.GradeNotRelev && doc.Grade <= judgment.GradeHighly {
				grades[pairKey{queryID: entry.QueryID, docID: doc.DocID}] = doc.Grade
			}
		}
	}
	return grades
}

func labels(sets []Set) []string {
	out := make([]string, len(sets))
	for i, s := range sets {
		out[i] = s.Label
	}
	return out
}
