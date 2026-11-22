// Package cli renders operator-facing command output. Diagnostics belong in
// slog; what the operator reads at the end of a run comes through here.
package cli

import (
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/fatih/color"
)

// The palette every sengi command colours its output with.
var (
	Success = color.New(color.FgGreen, color.Bold)
	Failure = color.New(color.FgRed, color.Bold)
	Caution = color.New(color.FgYellow)
	Muted   = color.New(color.FgHiBlack)
	Strong  = color.New(color.Bold)
	Accent  = color.New(color.FgCyan)
)

// Field is one labelled value in a summary block.
type Field struct {
	Label string
	Value string
}

func IntField(label string, value int) Field {
	return Field{Label: label, Value: strconv.Itoa(value)}
}

func Done(w io.Writer, msg string) {
	fmt.Fprintf(w, "%s %s\n", Success.Sprint("✓"), msg)
}

func Warn(w io.Writer, msg string) {
	fmt.Fprintf(w, "%s %s\n", Caution.Sprint("⚠"), msg)
}

func printHeader(w io.Writer, title string, fields []Field) {
	fmt.Fprintf(w, "%s\n", Strong.Sprint(title))
	writeFields(w, fields)
	fmt.Fprintln(w)
}

func writeFields(w io.Writer, fields []Field) {
	width := 0
	for _, f := range fields {
		if n := utf8.RuneCountInString(f.Label); n > width {
			width = n
		}
	}
	for _, f := range fields {
		fmt.Fprintf(w, "  %s  %s\n", Muted.Sprintf("%-*s", width, f.Label), f.Value)
	}
}
