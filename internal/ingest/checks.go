package ingest

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tadjov/sengi/internal/datapackage"
	"github.com/tadjov/sengi/pkg/apis/preprocess"
)

// ChecksFailedError reports a run over an error budget; nothing was published.
type ChecksFailedError struct {
	Preprocess datapackage.Preprocess
}

func (e *ChecksFailedError) Error() string {
	var failed []string
	for _, name := range slices.Sorted(maps.Keys(e.Preprocess.Checks)) {
		check := e.Preprocess.Checks[name]
		if check.Result == datapackage.CheckError {
			failed = append(failed, fmt.Sprintf("%s %.4f is over %.4f", name, check.Observed, *check.Error))
		}
	}
	return "checks failed, nothing published: " + strings.Join(failed, "; ")
}

// evaluateChecks shares dropped and duplicates out of the records read, cleared out of the articles written.
func evaluateChecks(checks preprocess.Checks, counts datapackage.Counts, clearedArticles int64) map[string]datapackage.CheckResult {
	results := map[string]datapackage.CheckResult{}
	for _, check := range []struct {
		name     string
		budget   *preprocess.Budget
		observed float64
	}{
		{"dropped", checks.Dropped, share(sum(counts.Dropped), counts.Read)},
		{"duplicates", checks.Duplicates, share(sum(counts.Duplicates), counts.Read)},
		{"cleared", checks.Cleared, share(clearedArticles, counts.Written)},
	} {
		if check.budget == nil {
			continue
		}
		result := datapackage.CheckResult{Observed: check.observed, Warn: check.budget.Warn, Error: check.budget.Error, Result: datapackage.CheckPass}
		switch {
		case check.budget.Error != nil && check.observed > *check.budget.Error:
			result.Result = datapackage.CheckError
		case check.budget.Warn != nil && check.observed > *check.budget.Warn:
			result.Result = datapackage.CheckWarn
		}
		results[check.name] = result
	}
	return results
}

func share(part, whole int64) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

func sum(counts map[string]int64) int64 {
	var total int64
	for _, n := range counts {
		total += n
	}
	return total
}

func failedChecks(results map[string]datapackage.CheckResult) bool {
	for _, result := range results {
		if result.Result == datapackage.CheckError {
			return true
		}
	}
	return false
}
