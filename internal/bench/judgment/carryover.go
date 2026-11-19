package judgment

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/tadjov/tusker/internal/bench/meta"
	"github.com/tadjov/tusker/internal/bench/pool"
)

// CarryOverInput is the old judgments, the pool they graded, the new pool and the current judge.
type CarryOverInput struct {
	Prior     *File
	PriorPool *pool.PoolFile
	Pool      *pool.PoolFile
	Judge     meta.Judge
}

// CarriedGrades are the old grades still valid for the new pool.
type CarriedGrades struct {
	Grades         *File
	Count          int
	ChangedQueries []string
}

// CarryOver reuses a grade when the judge and the query's text, category and
// filters are unchanged. Docs new to the pool are left to grade.
func CarryOver(in CarryOverInput) (CarriedGrades, error) {
	if err := checkCarryOver(in); err != nil {
		return CarriedGrades{}, err
	}
	priorQueries := make(map[string]pool.PoolEntry, len(in.PriorPool.Queries))
	for _, entry := range in.PriorPool.Queries {
		priorQueries[entry.QueryID] = entry
	}
	priorGrades := indexExisting(in.Prior)

	carried := CarriedGrades{Grades: &File{Meta: in.Prior.Meta}}
	for _, entry := range in.Pool.Queries {
		before, ok := priorQueries[entry.QueryID]
		if !ok {
			continue
		}
		if !sameGradingQuery(before, entry) {
			carried.ChangedQueries = append(carried.ChangedQueries, entry.QueryID)
			continue
		}
		kept := Entry{QueryID: entry.QueryID}
		for _, doc := range entry.Docs {
			if grade, ok := priorGrades[entry.QueryID][doc.DocID]; ok && grade >= 0 {
				kept.Docs = append(kept.Docs, GradedDoc{DocID: doc.DocID, Grade: grade})
			}
		}
		carried.Count += len(kept.Docs)
		carried.Grades.Queries = append(carried.Grades.Queries, kept)
	}
	return carried, nil
}

func checkCarryOver(in CarryOverInput) error {
	graded := in.Prior.Meta.PoolRunID
	if graded == "" {
		return errors.New("old judgments have no pool_run_id")
	}
	if graded != in.PriorPool.Meta.RunID {
		return fmt.Errorf("old judgments graded pool %s, not %s", graded, in.PriorPool.Meta.RunID)
	}
	was := in.Prior.Judge()
	if (was == meta.Judge{}) {
		return errors.New("old judgments name no judge")
	}
	if differences := was.Differences(in.Judge); len(differences) > 0 {
		return fmt.Errorf("old judgments come from another judge: %s", strings.Join(differences, "; "))
	}
	if in.Judge.Batching != nil {
		return errors.New("batched grades depend on their batch, so they can't be carried over")
	}
	return nil
}

func sameGradingQuery(before, now pool.PoolEntry) bool {
	return before.QueryText == now.QueryText && before.Category == now.Category && before.Filters.Equal(now.Filters)
}

// MergeGrades lays later's grades over earlier's; an unjudged doc keeps its earlier grade.
func MergeGrades(earlier, later *File) *File {
	if earlier == nil {
		return later
	}
	if later == nil {
		return earlier
	}
	grades := indexExisting(earlier)
	for qid, docs := range indexExisting(later) {
		if grades[qid] == nil {
			grades[qid] = map[uuid.UUID]int{}
		}
		for id, grade := range docs {
			if grade >= 0 {
				grades[qid][id] = grade
			}
		}
	}
	merged := &File{Meta: later.Meta}
	for qid, docs := range grades {
		entry := Entry{QueryID: qid}
		for id, grade := range docs {
			entry.Docs = append(entry.Docs, GradedDoc{DocID: id, Grade: grade})
		}
		merged.Queries = append(merged.Queries, entry)
	}
	return merged
}
