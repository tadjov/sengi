package cli

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const clearLine = "\r\033[K"

// neverTicks leaves every redraw to the test, so nothing races its clock.
func neverTicks(time.Duration) (<-chan time.Time, func()) { return nil, func() {} }

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

type progressFixture struct {
	progress *Progress
	status   *bytes.Buffer
	clock    *fakeClock
}

func newFixture(t *testing.T, terminal bool, width int) progressFixture {
	t.Helper()
	noColor(t)
	clock := &fakeClock{t: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)}
	status := &bytes.Buffer{}
	c := console{out: status, terminal: terminal, width: func() int { return width }, now: clock.now, ticker: neverTicks}
	return progressFixture{progress: newProgress(c, "preprocess", "records"), status: status, clock: clock}
}

// lastDraw is what a terminal shows after every redraw so far.
func lastDraw(status string) string {
	i := strings.LastIndex(status, clearLine)
	if i < 0 {
		return status
	}
	return status[i+len(clearLine):]
}

func TestProgress_TerminalRedrawsOneLineInPlace(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.SetTotal(708_241)
	f.progress.Add(312_000)
	f.clock.advance(12 * time.Second)
	f.progress.tick()

	assert.Contains(t, lastDraw(f.status.String()), "preprocess  ████████░░░░░░░░░░░░   44%  312,000 / 708,241 records  26.0k/s  12s  eta 15s")

	f.progress.Add(1_000)
	f.clock.advance(time.Second)
	f.progress.tick()

	assert.Contains(t, lastDraw(f.status.String()), "313,000 / 708,241 records")
	assert.NotContains(t, f.status.String(), "\n", "a terminal redraw must stay on one line")
	assert.Equal(t, 2, strings.Count(f.status.String(), clearLine))
}

func TestProgress_TerminalAnimatesTheSpinnerWithoutNewCounts(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.tick()
	first := lastDraw(f.status.String())
	f.progress.tick()

	assert.NotEqual(t, first, lastDraw(f.status.String()))
}

func TestProgress_BarFillsWithTheCount(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.SetTotal(100)
	f.progress.Add(50)
	f.progress.tick()

	assert.Contains(t, lastDraw(f.status.String()), "██████████░░░░░░░░░░   50%")
}

func TestProgress_CountKeepsItsColumnAsItGrows(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.SetTotal(708_241)
	f.progress.Add(5)
	f.progress.tick()
	early := lastDraw(f.status.String())
	f.progress.Add(499_995)
	f.progress.tick()
	late := lastDraw(f.status.String())

	assert.Equal(t, strings.Index(early, " / 708,241"), strings.Index(late, " / 708,241"), "the line must not shift as digits are added")
}

func TestProgress_ShowsRateAndTimeLeft(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.SetTotal(300_000)
	f.progress.Add(100_000)
	f.clock.advance(10 * time.Second)
	f.progress.tick()

	line := lastDraw(f.status.String())
	assert.Contains(t, line, "10.0k/s")
	assert.Contains(t, line, "eta 20s")
}

func TestProgress_WithoutTotalShowsCountRateAndElapsed(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.Add(5_000)
	f.clock.advance(3*time.Minute + 4*time.Second)
	f.progress.tick()

	line := lastDraw(f.status.String())
	assert.Contains(t, line, "preprocess  5,000 records  27/s  3m04s")
	assert.NotContains(t, line, "%")
	assert.NotContains(t, line, "eta")
	assert.NotContains(t, line, "░")
}

func TestProgress_NothingCountedShowsOnlyLabelAndElapsed(t *testing.T) {
	f := newFixture(t, true, 0)
	f.clock.advance(2 * time.Second)
	f.progress.tick()

	line := lastDraw(f.status.String())
	assert.Contains(t, line, "preprocess  2s")
	assert.NotContains(t, line, "records")
}

func TestProgress_PlainOutputWritesOneLineEveryTenSeconds(t *testing.T) {
	f := newFixture(t, false, 0)
	f.progress.Add(100)

	f.clock.advance(9 * time.Second)
	f.progress.tick()
	assert.Empty(t, f.status.String(), "nothing is due before ten seconds")

	f.clock.advance(time.Second)
	f.progress.tick()
	f.clock.advance(5 * time.Second)
	f.progress.tick()
	f.progress.Add(50)
	f.clock.advance(5 * time.Second)
	f.progress.tick()

	assert.Equal(t, "preprocess  100 records  10/s  10s\npreprocess  150 records  7.5/s  20s\n", f.status.String())
}

func TestProgress_PlainOutputHasNoTerminalControlsOrBar(t *testing.T) {
	f := newFixture(t, false, 0)
	f.progress.SetTotal(10)
	f.progress.Add(1)
	f.clock.advance(10 * time.Second)
	f.progress.tick()
	f.progress.Done()

	assert.Contains(t, f.status.String(), "10%")
	assert.NotContains(t, f.status.String(), "\r")
	assert.NotContains(t, f.status.String(), "\033")
	assert.NotContains(t, f.status.String(), "░")
}

func TestProgress_DoneClearsTheLine(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.Add(1)
	f.progress.tick()
	f.progress.Done()

	assert.True(t, strings.HasSuffix(f.status.String(), clearLine), "Done must leave the terminal line empty")
}

func TestProgress_DoneBeforeAnyDrawWritesNothing(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.Done()
	f.progress.Done()

	assert.Empty(t, f.status.String())
}

func TestProgress_TickAfterDoneWritesNothing(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.Done()
	f.progress.tick()

	assert.Empty(t, f.status.String())
}

func TestProgress_StageCountsAfreshAndShowsDetail(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.Stage("copy_articles")
	f.progress.Add(10)
	f.progress.tick()
	assert.Contains(t, lastDraw(f.status.String()), "copy_articles  10 records")

	f.progress.Stage("build_search_index")
	f.progress.Detail("phase", "building index", "tuples", "10/20")
	f.clock.advance(time.Minute)
	f.progress.tick()

	line := lastDraw(f.status.String())
	assert.Contains(t, line, "build_search_index  1m00s  phase building index  tuples 10/20")
	assert.NotContains(t, line, "records", "a new stage starts with nothing counted")
}

func TestProgress_AnEndedStageLeavesItsOwnLine(t *testing.T) {
	f := newFixture(t, true, 0)
	end := f.progress.Stage("copy_articles")
	f.progress.Add(708_241)
	f.clock.advance(time.Minute + 52*time.Second)
	end()
	f.progress.tick()

	status := f.status.String()
	assert.Contains(t, status, "✓ copy_articles  708,241 records  1m52s  6.3k/s\n")
	assert.Contains(t, lastDraw(status), "preprocess", "with no stage running the line names the command again")
	assert.NotContains(t, lastDraw(status), "copy_articles")
}

func TestProgress_PlainOutputWritesAnEndedStageRightAway(t *testing.T) {
	f := newFixture(t, false, 0)
	end := f.progress.Stage("schema")
	f.clock.advance(120 * time.Millisecond)
	end()
	f.progress.tick()

	assert.Equal(t, "✓ schema  0.1s\n", f.status.String())
}

func TestProgress_DoneWritesStagesThatEndedSinceTheLastDraw(t *testing.T) {
	f := newFixture(t, true, 0)
	end := f.progress.Stage("vacuum_analyze")
	f.clock.advance(3 * time.Second)
	end()
	f.progress.Done()

	assert.Contains(t, f.status.String(), "✓ vacuum_analyze  3.0s\n")
}

func TestProgress_AStageThatNeverEndsLeavesNoLine(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.Stage("copy_articles")
	f.progress.Add(10)
	f.progress.tick()
	f.progress.Done()

	assert.NotContains(t, f.status.String(), "✓")
}

func TestProgress_EndingAStageAfterTheNextBeganDoesNothing(t *testing.T) {
	f := newFixture(t, true, 0)
	endSchema := f.progress.Stage("schema")
	f.progress.Stage("copy_articles")
	endSchema()
	f.progress.Done()

	assert.NotContains(t, f.status.String(), "✓")
}

func TestProgress_DetailGroupsIntegerThousands(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.Add(312_000)
	f.progress.Detail("written", int64(300_000), "deduped", 1_204)
	f.progress.tick()

	assert.Contains(t, lastDraw(f.status.String()), "312,000 records  0s  written 300,000  deduped 1,204")
}

func TestProgress_TerminalLineFitsTheWidth(t *testing.T) {
	f := newFixture(t, true, 30)
	f.progress.Detail("phase", strings.Repeat("x", 100))
	f.progress.tick()

	assert.Less(t, utf8.RuneCountInString(lastDraw(f.status.String())), 30, "a wrapped line cannot be redrawn in place")
}

func TestProgress_AboveClearsTheLineBeforeOtherOutput(t *testing.T) {
	f := newFixture(t, true, 0)
	f.progress.Add(1)
	f.progress.tick()

	var stdout bytes.Buffer
	_, err := f.progress.Above(&stdout).Write([]byte("[q1] done\n"))
	require.NoError(t, err)

	assert.Equal(t, "[q1] done\n", stdout.String())
	assert.True(t, strings.HasSuffix(f.status.String(), clearLine), "the progress line must be gone before the line above it lands")

	f.progress.tick()
	assert.Contains(t, lastDraw(f.status.String()), "1 records", "the next tick draws the line again")
}

func TestProgress_CountsEveryAddFromConcurrentWorkers(t *testing.T) {
	f := newFixture(t, false, 0)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1_000 {
				f.progress.Add(1)
			}
		}()
	}
	wg.Wait()
	f.clock.advance(10 * time.Second)
	f.progress.tick()

	assert.Contains(t, f.status.String(), "8,000 records")
}

func TestCommand_PrintsHeaderThenSummaryWithDuration(t *testing.T) {
	f := newFixture(t, false, 0)
	var out bytes.Buffer
	cmd := Command{Title: "sengi preprocess", Fields: []Field{{Label: "raw", Value: "datasets/x/raw"}}, Label: "preprocess", Unit: "records"}

	err := cmd.run(&out, f.consoleFor(), func(p *Progress) (Summary, error) {
		p.Add(3)
		f.clock.advance(1500 * time.Millisecond)
		return Summary{
			Message: "Preprocess complete",
			Fields:  []Field{IntField("written", 3)},
			Notes:   []Note{{Text: "2 dropped", Warning: true}, {Text: "1 deduped"}},
		}, nil
	})
	require.NoError(t, err)

	text := out.String()
	assert.Less(t, strings.Index(text, "sengi preprocess"), strings.Index(text, "Preprocess complete"))
	assert.Contains(t, text, "datasets/x/raw")
	assert.Regexp(t, `duration\s+1\.50s`, text)
	assert.Less(t, strings.Index(text, "duration"), strings.Index(text, "2 dropped"), "notes follow the summary fields")
	assert.Less(t, strings.Index(text, "2 dropped"), strings.Index(text, "1 deduped"))
	assert.Empty(t, f.status.String(), "the progress line never reaches stdout, and a short plain run prints none")
}

func TestCommand_TimedSummaryKeepsItsOwnTiming(t *testing.T) {
	f := newFixture(t, false, 0)
	var out bytes.Buffer

	err := Command{Label: "load"}.run(&out, f.consoleFor(), func(*Progress) (Summary, error) {
		return Summary{Message: "Corpus loaded", Fields: []Field{{Label: "total", Value: "912.4s"}}, Timed: true}, nil
	})

	require.NoError(t, err)
	assert.Contains(t, out.String(), "912.4s")
	assert.NotContains(t, out.String(), "duration")
}

func TestCommand_ZeroSummaryAndNoTitlePrintNothing(t *testing.T) {
	f := newFixture(t, false, 0)
	var out bytes.Buffer

	err := Command{Label: "pool fts", Unit: "queries"}.run(&out, f.consoleFor(), func(*Progress) (Summary, error) {
		return Summary{}, nil
	})

	require.NoError(t, err)
	assert.Empty(t, out.String())
	assert.Empty(t, f.status.String())
}

func TestCommand_FailedWorkClearsTheLineAndPrintsNoSummary(t *testing.T) {
	f := newFixture(t, true, 0)
	var out bytes.Buffer
	failure := errors.New("engine unreachable")

	err := Command{Title: "sengi load", Label: "load"}.run(&out, f.consoleFor(), func(p *Progress) (Summary, error) {
		p.tick()
		return Summary{Message: "Corpus loaded"}, failure
	})

	require.ErrorIs(t, err, failure)
	assert.NotContains(t, out.String(), "Corpus loaded")
	assert.True(t, strings.HasSuffix(f.status.String(), clearLine))
}

func (f progressFixture) consoleFor() console {
	return f.progress.console
}
