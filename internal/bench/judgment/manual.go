package judgment

import "github.com/tadjov/sengi/internal/bench/meta"

// ManualJudge is the judge a hand-graded file records.
func ManualJudge() meta.Judge {
	return meta.Judge{Strategy: string(StrategyManual)}
}
