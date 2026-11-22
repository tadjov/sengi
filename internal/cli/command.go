package cli

import (
	"fmt"
	"io"
	"log"
	"slices"
)

// Command prints a header, shows a progress line on stderr while its work runs, then prints the summary.
type Command struct {
	// Title heads the output; empty prints no header.
	Title  string
	Fields []Field
	// Label names the work on the progress line, and Unit is what it counts, such as "records".
	Label string
	Unit  string
}

// Summary is what a command prints when its work succeeds; a zero Summary prints nothing.
type Summary struct {
	Message string
	Fields  []Field
	// Notes print after the fields.
	Notes []Note
	// Timed means Fields already say how long the work took, so no duration is added.
	Timed bool
}

type Note struct {
	Text    string
	Warning bool
}

// Run prints the summary work returns with how long the work took.
func (c Command) Run(out io.Writer, work func(*Progress) (Summary, error)) error {
	return c.run(out, stderrConsole(), work)
}

func (c Command) run(out io.Writer, status console, work func(*Progress) (Summary, error)) error {
	if c.Title != "" {
		printHeader(out, c.Title, c.Fields)
	}
	p := newProgress(status, c.Label, c.Unit)
	p.start()
	defer p.Done()
	if status.terminal {
		// slog's default handler writes through log; on a terminal its lines would land inside the progress line.
		previous := log.Writer()
		log.SetOutput(p.Above(previous))
		defer log.SetOutput(previous)
	}

	summary, err := work(p)
	p.Done()
	if err != nil {
		return err
	}
	if summary.Message != "" {
		elapsed := status.now().Sub(p.started)
		printSummary(out, summary, elapsed.Seconds())
	}
	return nil
}

func printSummary(w io.Writer, s Summary, seconds float64) {
	Done(w, s.Message)
	fields := s.Fields
	if !s.Timed {
		fields = append(slices.Clone(fields), Field{Label: "duration", Value: fmt.Sprintf("%.2fs", seconds)})
	}
	writeFields(w, fields)
	for _, note := range s.Notes {
		if note.Warning {
			Warn(w, note.Text)
		} else {
			Done(w, note.Text)
		}
	}
}
