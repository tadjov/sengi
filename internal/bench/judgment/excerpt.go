package judgment

import (
	"fmt"
	"unicode/utf8"

	"github.com/tadjov/sengi/internal/bench/meta"
)

// ContentExcerpt is what every grader reads of an article's content: its
// first runes runes, then "...", or all of it when runes is 0.
func ContentExcerpt(content string, runes int) string {
	if runes == 0 || utf8.RuneCountInString(content) <= runes {
		return content
	}
	return string([]rune(content)[:runes]) + "..."
}

// CheckContentCut refuses grades read at another content cut than contentRunes,
// so graders compared with each other read the same text. Judges that read no
// excerpt pass.
func CheckContentCut(judge meta.Judge, contentRunes int) error {
	switch StrategyKind(judge.Strategy) {
	case StrategyLLM, StrategyJev, StrategyManual:
	default:
		return nil
	}
	if judge.ContentRunes == contentRunes {
		return nil
	}
	return fmt.Errorf("graded on %s, but the spec's judging.content_runes shows %s, so grades compared with them would judge other text",
		describeCut(judge.ContentRunes), describeCut(contentRunes))
}

func describeCut(contentRunes int) string {
	if contentRunes == 0 {
		return "the whole content"
	}
	return fmt.Sprintf("content cut at %d runes", contentRunes)
}
