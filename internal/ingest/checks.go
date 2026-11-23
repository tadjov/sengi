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
			failed = append(failed, fmt.Sprintf("%s %d (%.2f%%) is over %s", name, check.Count, 100*check.Share, check.Error))
		}
	}
	return "checks failed, nothing published: " + strings.Join(failed, "; ")
}

// checkCounts are what the checks measure and the bases they share them of.
type checkCounts struct {
	read, dropped, duplicates int64
	written, cleared          int64
}

func evaluateChecks(checks preprocess.Checks, observed checkCounts) map[string]datapackage.CheckResult {
	results := map[string]datapackage.CheckResult{}
	for _, check := range []struct {
		name         string
		budget       *preprocess.Budget
		count, total int64
	}{
		{"droppedOfRead", checks.DroppedOfRead, observed.dropped, observed.read},
		{"duplicatesOfRead", checks.DuplicatesOfRead, observed.duplicates, observed.read},
		{"clearedOfWritten", checks.ClearedOfWritten, observed.cleared, observed.written},
	} {
		if check.budget == nil {
			continue
		}
		result := datapackage.CheckResult{
			Count: check.count, Share: share(check.count, check.total),
			Warn: check.budget.Warn, Error: check.budget.Error, Result: datapackage.CheckPass,
		}
		switch {
		case check.budget.Error != nil && check.budget.Error.Exceeded(check.count, check.total):
			result.Result = datapackage.CheckError
		case check.budget.Warn != nil && check.budget.Warn.Exceeded(check.count, check.total):
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
