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
)

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var _ progress.Counter = (*Progress)(nil)

// Progress is a status line redrawn in place on a terminal, or written plainly every ten seconds elsewhere.
type Progress struct {
	console
	label   string
	unit    string
	started time.Time

	mu     sync.Mutex
	stage  string
	total  int64
	count  int64
	detail []any

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
	return &Progress{console: c, label: label, unit: unit, started: now, lastLine: now}
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

func (p *Progress) Stage(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stage, p.total, p.count, p.detail = name, 0, 0, nil
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

// Done stops the line and clears it from the terminal; later calls do nothing.
func (p *Progress) Done() {
	p.once.Do(func() {
		if p.stopLoop != nil {
			p.stopLoop()
		}
		p.output.Lock()
		defer p.output.Unlock()
		p.clear()
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
	now := p.now()
	if !p.terminal {
		if now.Sub(p.lastLine) < plainInterval {
			return
		}
		p.lastLine = now
		fmt.Fprintln(p.out, p.line(now))
		return
	}
	frame := spinFrames[p.frame%len(spinFrames)]
	p.frame++
	// Two columns go to the spinner, and the last one is left empty so the line never wraps.
	text := truncate(p.line(now), p.width()-3)
	fmt.Fprintf(p.out, "%s%s %s", clearToLineStart, Accent.Sprint(frame), text)
	p.drawn = true
}

// clear needs p.output held.
func (p *Progress) clear() {
	if p.terminal && p.drawn {
		_, _ = io.WriteString(p.out, clearToLineStart)
		p.drawn = false
	}
}

func (p *Progress) line(now time.Time) string {
	p.mu.Lock()
	stage, total, count, detail := p.stage, p.total, p.count, p.detail
	p.mu.Unlock()

	parts := []string{p.label}
	if stage != "" {
		parts = append(parts, stage)
	}
	switch {
	case total > 0:
		parts = append(parts,
			withUnit(groupThousands(count)+" / "+groupThousands(total), p.unit),
			strconv.FormatInt(min(count*100/total, 100), 10)+"%")
	case count > 0:
		parts = append(parts, withUnit(groupThousands(count), p.unit))
	}
	parts = append(parts, detailPairs(detail)...)
	parts = append(parts, formatElapsed(now.Sub(p.started)))
	return strings.Join(parts, "  ")
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

// truncate leaves text alone when width is unknown or too small to matter.
func truncate(text string, width int) string {
	runes := []rune(text)
	if width < 2 || len(runes) <= width {
		return text
	}
	return string(runes[:width-1]) + "…"
}
