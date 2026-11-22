package report

import (
	"fmt"
	"io"
	"slices"

	"github.com/fatih/color"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
)

var (
	tBold   = color.New(color.Bold)
	tGreen  = color.New(color.FgGreen, color.Bold)
	tRed    = color.New(color.FgRed, color.Bold)
	tYellow = color.New(color.FgYellow)
	tDim    = color.New(color.FgHiBlack)
)

func WriteTable(r *Report, w io.Writer) {
	title := r.Provenance.SpecID
	if title == "" {
		title = "Benchmark"
	}
	fmt.Fprintf(w, "\n%s\n", tBold.Sprintf("=== %s  run_id=%s ===", title, r.Provenance.RunID))
	writeScoringHeader(w, r)

	for _, job := range jobViews(r) {
		fmt.Fprintf(w, "\n%s\n", tBold.Sprintf("--- Job: %s ---", job.Name))
		if !job.Judged {
			fmt.Fprintf(w, "\n%s No relevance judgments found. Showing latency only.\n",
				tYellow.Sprint("WARNING:"))
			fmt.Fprintf(w, "  Run bench pool, then bench judge.\n\n")
			for _, t := range job.timingTables() {
				writeTerminalTable(w, t)
			}
			continue
		}
		for _, t := range job.qualityTables() {
			fmt.Fprintln(w)
			writeTerminalTable(w, t)
		}
		for _, t := range job.timingTables() {
			writeTerminalTable(w, t)
		}
		if job.Significance.hasRows() {
			significance := job.Significance
			significance.Note += " · " + starsLegend
			writeTerminalTable(w, significance)
		}
		writeTerminalTable(w, job.PerQuery)
	}
}

// The judge and the cutoff change what every number below them means.
func writeScoringHeader(w io.Writer, r *Report) {
	judge := "unknown"
	if r.Provenance.Judge != nil {
		judge = r.Provenance.Judge.String()
	}
	fmt.Fprintf(w, "%s\n", tDim.Sprintf("judge=%s  relevant=grade>=%d", judge, r.Config.RelevanceThreshold))
}

func writeTerminalTable(w io.Writer, view tableView) {
	fmt.Fprintf(w, "%s\n\n", tBold.Sprint(view.heading()))

	t := table.NewWriter()
	t.SetOutputMirror(w)
	t.SetStyle(table.StyleRounded)
	t.AppendHeader(headerRow(view))
	t.SetColumnConfigs(rightAligned(view, alignRightInTerminal, alignRight))
	for _, cells := range view.Rows {
		row := make(table.Row, len(cells))
		for i, c := range cells {
			row[i] = terminalCell(c)
		}
		t.AppendRow(row)
	}
	t.Render()
	fmt.Fprintln(w)
}

func terminalCell(c cell) string {
	switch c.Mark {
	case markBest, markSignificant, markSucceeded:
		return tGreen.Sprint(c.Text)
	case markWeaklySignificant:
		return tYellow.Sprint(c.Text)
	case markFailed:
		return tRed.Sprint(c.Text)
	case markAbsent, markNotSignificant:
		return tDim.Sprint(c.Text)
	default:
		return c.Text
	}
}

// WriteMarkdown renders the report as GitHub-Flavored Markdown, without color.
func WriteMarkdown(r *Report, w io.Writer) {
	title := r.Provenance.SpecID
	if title == "" {
		title = "Benchmark"
	}
	fmt.Fprintf(w, "# %s\n\n", title)
	fmt.Fprintf(w, "**run_id:** `%s`  \n", r.Provenance.RunID)
	if !r.Provenance.GeneratedAt.IsZero() {
		fmt.Fprintf(w, "**generated:** %s  \n", r.Provenance.GeneratedAt.Format("2006-01-02 15:04:05 UTC"))
	}
	fmt.Fprintln(w)

	for _, job := range jobViews(r) {
		fmt.Fprintf(w, "## Job: %s\n\n", job.Name)
		if !job.Judged {
			fmt.Fprintf(w, "> ⚠️ No relevance judgments — latency only.\n\n")
			for _, t := range job.timingTables() {
				writeMarkdownTable(w, t)
			}
			continue
		}
		for _, t := range job.qualityTables() {
			writeMarkdownTable(w, t)
		}
		for _, t := range job.timingTables() {
			writeMarkdownTable(w, t)
		}
		if job.Significance.hasRows() {
			writeMarkdownTable(w, job.Significance)
		}
	}
}

func writeMarkdownTable(w io.Writer, view tableView) {
	fmt.Fprintf(w, "### %s\n\n", view.heading())

	t := table.NewWriter()
	t.AppendHeader(headerRow(view))
	t.SetColumnConfigs(rightAligned(view, alignRight))
	for _, cells := range view.Rows {
		row := make(table.Row, len(cells))
		for i, c := range cells {
			row[i] = c.Text
		}
		t.AppendRow(row)
	}
	fmt.Fprintln(w, t.RenderMarkdown())
	fmt.Fprintln(w)
}

func headerRow(view tableView) table.Row {
	header := make(table.Row, len(view.Columns))
	for i, c := range view.Columns {
		header[i] = c.Title
	}
	return header
}

func rightAligned(view tableView, alignments ...alignment) []table.ColumnConfig {
	var configs []table.ColumnConfig
	for i, c := range view.Columns {
		if slices.Contains(alignments, c.Align) {
			configs = append(configs, table.ColumnConfig{Number: i + 1, Align: text.AlignRight})
		}
	}
	return configs
}
