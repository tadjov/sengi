package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tadjov/sengi/internal/progress"
)

const (
	clearToLineStart = "\r\033[K"
	redrawInterval   = 100 * time.Millisecond
	plainInterval    = 10 * time.Second
	plainPoll        = time.Second
	barCells         = 20
)

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var _ progress.Counter = (*Progress)(nil)

// Progress is a status line redrawn in place on a terminal, or written plainly every ten seconds elsewhere.
// Each stage that ends leaves a line of its own above it.
type Progress struct {
	console
	label   string
	unit    string
	started time.Time

	mu           sync.Mutex
	stage        string
	stageID      int
	stageStarted time.Time
	total        int64
	count        int64
	detail       []any
	// ended holds the lines of stages that ended since the last draw.
	ended []string

	// output orders writes to the console; counting never waits on it, so a stalled reader cannot slow the work.
	output   sync.Mutex
	frame    int
	drawn    bool
	lastLine time.Time
	finished bool

	stopLoop func()
	once     sync.Once
}

type console struct {
	out      io.Writer
	terminal bool
	// width is the terminal's columns, 0 when unknown.
	width  func() int
	now    func() time.Time
	ticker func(period time.Duration) (ticks <-chan time.Time, stop func())
}

func stderrConsole() console {
	return console{
		out:      os.Stderr,
		terminal: isTerminal(os.Stderr),
		width:    func() int { return terminalWidth(os.Stderr) },
		now:      time.Now,
		ticker: func(period time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(period)
			return t.C, t.Stop
		},
	}
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func newProgress(c console, label, unit string) *Progress {
	now := c.now()
	return &Progress{console: c, label: label, unit: unit, started: now, stageStarted: now, lastLine: now}
}

func (p *Progress) SetTotal(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total = n
}

func (p *Progress) Add(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count += n
}

func (p *Progress) Stage(name string) (end func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stageID++
	p.stage, p.stageStarted = name, p.now()
	p.total, p.count, p.detail = 0, 0, nil
	id := p.stageID
	return func() { p.endStage(id) }
}

func (p *Progress) endStage(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id != p.stageID || p.stage == "" {
		return
	}
	now := p.now()
	p.ended = append(p.ended, p.endedLine(now))
	p.stage, p.stageStarted = "", now
	p.total, p.count, p.detail = 0, 0, nil
}

func (p *Progress) Detail(attrs ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.detail = slices.Clone(attrs)
}

// Above returns a writer whose lines land above the progress line instead of through it.
func (p *Progress) Above(w io.Writer) io.Writer {
	return aboveWriter{progress: p, w: w}
}

type aboveWriter struct {
	progress *Progress
	w        io.Writer
}

func (a aboveWriter) Write(b []byte) (int, error) {
	a.progress.output.Lock()
	defer a.progress.output.Unlock()
	a.progress.clear()
	return a.w.Write(b)
}

// Done stops the line, writes the lines of stages that ended and clears the line from the terminal; later calls do nothing.
func (p *Progress) Done() {
	p.once.Do(func() {
		if p.stopLoop != nil {
			p.stopLoop()
		}
		p.output.Lock()
		defer p.output.Unlock()
		p.clear()
		p.writeEnded(p.takeEnded())
		p.finished = true
	})
}

func (p *Progress) start() {
	period := plainPoll
	if p.terminal {
		period = redrawInterval
	}
	ticks, stopTicker := p.ticker(period)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			case <-ticks:
				p.tick()
			}
		}
	}()
	p.stopLoop = func() {
		close(stop)
		<-stopped
		stopTicker()
	}
}

func (p *Progress) tick() {
	p.output.Lock()
	defer p.output.Unlock()
	if p.finished {
		return
	}
	if ended := p.takeEnded(); len(ended) > 0 {
		p.clear()
		p.writeEnded(ended)
	}
	now := p.now()
	if !p.terminal {
		if now.Sub(p.lastLine) < plainInterval {
			return
		}
		p.lastLine = now
		fmt.Fprintln(p.out, p.line(now, false))
		return
	}
	frame := spinFrames[p.frame%len(spinFrames)]
	p.frame++
	// Two columns go to the spinner, and the last one is left empty so the line never wraps.
	text := truncate(p.line(now, true), p.width()-3)
	fmt.Fprintf(p.out, "%s%s %s", clearToLineStart, Accent.Sprint(frame), text)
	p.drawn = true
}

func (p *Progress) takeEnded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ended := p.ended
	p.ended = nil
	return ended
}

// writeEnded needs p.output held.
func (p *Progress) writeEnded(lines []string) {
	for _, line := range lines {
		fmt.Fprintf(p.out, "%s %s\n", Success.Sprint("✓"), line)
	}
}

// clear needs p.output held.
func (p *Progress) clear() {
	if p.terminal && p.drawn {
		_, _ = io.WriteString(p.out, clearToLineStart)
		p.drawn = false
	}
}

func (p *Progress) line(now time.Time, withBar bool) string {
	p.mu.Lock()
	stage, started, total, count, detail := p.stage, p.stageStarted, p.total, p.count, p.detail
	p.mu.Unlock()

	name, elapsed := p.label, now.Sub(p.started)
	if stage != "" {
		name, elapsed = stage, now.Sub(started)
	}
	parts := []string{name}
	switch {
	case total > 0:
		if withBar {
			parts = append(parts, bar(count, total))
		}
		width := len(groupThousands(total))
		parts = append(parts,
			fmt.Sprintf("%3d%%", min(count*100/total, 100)),
			withUnit(fmt.Sprintf("%*s / %s", width, groupThousands(count), groupThousands(total)), p.unit))
	case count > 0:
		parts = append(parts, withUnit(groupThousands(count), p.unit))
	}
	rate, measured := ratePerSecond(count, elapsed)
	if measured {
		parts = append(parts, formatRate(rate))
	}
	parts = append(parts, formatElapsed(elapsed))
	if measured && total > count {
		parts = append(parts, "eta "+formatElapsed(time.Duration(float64(total-count)/rate*float64(time.Second))))
	}
	parts = append(parts, detailPairs(detail)...)
	return strings.Join(parts, "  ")
}

// endedLine needs p.mu held.
func (p *Progress) endedLine(now time.Time) string {
	took := now.Sub(p.stageStarted)
	parts := []string{p.stage}
	if p.count > 0 {
		parts = append(parts, withUnit(groupThousands(p.count), p.unit))
	}
	parts = append(parts, formatTook(took))
	if rate, measured := ratePerSecond(p.count, took); measured {
		parts = append(parts, formatRate(rate))
	}
	return strings.Join(parts, "  ")
}

// ratePerSecond waits for a second of work, so the first few items cannot claim a wild rate.
func ratePerSecond(count int64, elapsed time.Duration) (float64, bool) {
	if count == 0 || elapsed < time.Second {
		return 0, false
	}
	return float64(count) / elapsed.Seconds(), true
}

func bar(count, total int64) string {
	filled := int(min(count*barCells/total, barCells))
	return strings.Repeat("█", filled) + strings.Repeat("░", barCells-filled)
}

func withUnit(count, unit string) string {
	if unit == "" {
		return count
	}
	return count + " " + unit
}

func detailPairs(attrs []any) []string {
	pairs := make([]string, 0, (len(attrs)+1)/2)
	for i := 0; i < len(attrs); i += 2 {
		pair := fmt.Sprint(attrs[i])
		if i+1 < len(attrs) {
			pair += " " + formatValue(attrs[i+1])
		}
		pairs = append(pairs, pair)
	}
	return pairs
}

func formatValue(v any) string {
	switch n := v.(type) {
	case int:
		return groupThousands(int64(n))
	case int64:
		return groupThousands(n)
	}
	return fmt.Sprint(v)
}

func groupThousands(n int64) string {
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if n < 0 {
		sign, digits = "-", digits[1:]
	}
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	return sign + b.String()
}

func formatRate(perSecond float64) string {
	switch {
	case perSecond >= 1e6:
		return fmt.Sprintf("%.1fM/s", perSecond/1e6)
	case perSecond >= 1e3:
		return fmt.Sprintf("%.1fk/s", perSecond/1e3)
	case perSecond >= 10:
		return fmt.Sprintf("%.0f/s", perSecond)
	}
	return fmt.Sprintf("%.1f/s", perSecond)
}

func formatElapsed(d time.Duration) string {
	d = d.Truncate(time.Second)
	hours, minutes, seconds := int(d/time.Hour), int(d%time.Hour/time.Minute), int(d%time.Minute/time.Second)
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// formatTook keeps a tenth of a second for short stages, as the load summary does.
func formatTook(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return formatElapsed(d)
}

// truncate leaves text alone when width is unknown or too small to matter.
func truncate(text string, width int) string {
	runes := []rune(text)
	if width < 2 || len(runes) <= width {
		return text
	}
	return string(runes[:width-1]) + "…"
}
