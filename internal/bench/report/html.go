package report

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"math"
	"os"
	"path/filepath"
	"strings"
)

//go:embed report.gohtml
var htmlTemplate string

// WriteHTML renders r as a self-contained HTML file at path, creating parent
// directories as needed. The file contains inline CSS, inline SVG charts, and
// ~60 lines of vanilla JS for sortable tables and per-query filtering — no
// external dependencies, opens offline.
func WriteHTML(r *Report, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create report dir: %w", err)
	}
	buf, err := RenderHTML(r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, buf, 0644)
}

// RenderHTML returns the fully rendered HTML bytes for a report.
func RenderHTML(r *Report) ([]byte, error) {
	vm := buildViewModel(r)
	tmpl, err := template.New("report").Parse(htmlTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse html template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vm); err != nil {
		return nil, fmt.Errorf("render html: %w", err)
	}
	return buf.Bytes(), nil
}

type htmlReport struct {
	NDCGGain       string
	LinearNDCGGain string
	Title          string
	RunID          string
	Tool           string
	Generated      string
	SpecID         string
	Sources        *htmlSources
	Jobs           []htmlJob
}

type htmlSources struct {
	Spec, Suites, Pool, Judgments string
}

type htmlLegendItem struct {
	Name  string
	Color string
}

type htmlJob struct {
	Name         string
	Metrics      htmlTable
	Latency      htmlTable
	ServerTime   htmlTable
	Significance htmlTable
	PerQuery     htmlTable
	NDCGChart    template.HTML
	LatencyChart template.HTML
	EngineLegend []htmlLegendItem
}

type htmlTable struct {
	// ID is set on the table the per-query filter input targets.
	ID       string
	Sortable bool
	Columns  []string
	Rows     [][]htmlCell
}

// htmlCell styles its text with Class on the cell or SpanClass on a span inside it.
type htmlCell struct {
	Text      string
	Class     string
	SpanClass string
	Spread    string
}

func buildViewModel(r *Report) htmlReport {
	title := r.Provenance.SpecID
	if title == "" {
		title = "Benchmark Report"
	}
	vm := htmlReport{
		NDCGGain:       NDCGGain,
		LinearNDCGGain: LinearNDCGGain,
		Title:          title,
		RunID:          r.Provenance.RunID,
		Tool:           r.Provenance.Tool,
		Generated:      r.Provenance.GeneratedAt.Format("2006-01-02 15:04:05 UTC"),
		SpecID:         r.Provenance.SpecID,
	}
	if s := r.Provenance.Sources; s != nil {
		vm.Sources = &htmlSources{
			Spec:      s.Spec,
			Suites:    strings.Join(s.Suites, ", "),
			Pool:      s.Pool,
			Judgments: s.Judgments,
		}
	}

	for i, view := range jobViews(r) {
		aggregated := r.Jobs[i].Aggregated
		job := htmlJob{
			Name:         view.Name,
			Metrics:      htmlMetricsTable(view.Metrics),
			Latency:      newHTMLTable(view.Latency),
			ServerTime:   newHTMLTable(view.ServerTime),
			Significance: newHTMLTable(view.Significance),
			PerQuery:     newHTMLTable(view.PerQuery),
			NDCGChart:    buildNDCGChart(aggregated, r.Config.KValues),
			LatencyChart: buildLatencyChart(aggregated),
		}
		job.Significance.Sortable = false
		job.PerQuery.ID = fmt.Sprintf("pq-filter-%d", i)
		for e, agg := range aggregated {
			job.EngineLegend = append(job.EngineLegend, htmlLegendItem{Name: agg.EngineName, Color: engineColors[e%len(engineColors)]})
		}
		vm.Jobs = append(vm.Jobs, job)
	}
	return vm
}

func newHTMLTable(view tableView) htmlTable {
	t := htmlTable{Sortable: true}
	for _, c := range view.Columns {
		t.Columns = append(t.Columns, c.Title)
	}
	for _, cells := range view.Rows {
		row := make([]htmlCell, len(cells))
		for i, c := range cells {
			row[i] = htmlCell{Text: c.Text, SpanClass: markClass(c.Mark)}
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

func markClass(m mark) string {
	switch m {
	case markSignificant, markWeaklySignificant:
		return "sig-sig"
	case markNotSignificant:
		return "sig-ns"
	case markSucceeded:
		return "status-ok"
	case markFailed:
		return "status-err"
	default:
		return ""
	}
}

// htmlMetricsTable leaves P@K out and shows each NDCG mean with its spread.
func htmlMetricsTable(view tableView) htmlTable {
	t := htmlTable{Sortable: true}
	for _, c := range view.Columns {
		if c.Group != precisionColumns {
			t.Columns = append(t.Columns, c.Title)
		}
	}
	for _, cells := range view.Rows {
		var row []htmlCell
		for i, c := range cells {
			switch column := view.Columns[i]; {
			case column.Group == precisionColumns:
			case column.Group == ndcgColumns:
				row = append(row, htmlCell{Text: c.Text, SpanClass: "val-main", Spread: c.Spread})
			case column.Align == alignLeft:
				row = append(row, htmlCell{Text: c.Text})
			default:
				row = append(row, htmlCell{Text: c.Text, Class: "val-main"})
			}
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// ─── SVG charts ──────────────────────────────────────────────────────────────

var engineColors = []string{
	"#4C8EDA", "#E05C5C", "#52C878", "#F5A623", "#9B59B6", "#1ABC9C",
}

// buildNDCGChart renders a grouped bar chart of NDCG@K scores per engine.
// The engine legend is NOT included in the SVG; it is rendered in HTML below
// the chart using job.EngineLegend — this avoids text overflow for long names.
func buildNDCGChart(aggregated []AggregatedEntry, kVals []int) template.HTML {
	if len(aggregated) == 0 || len(kVals) == 0 {
		return ""
	}
	const (
		svgW      = 560
		svgH      = 190
		padLeft   = 46
		padRight  = 16
		padTop    = 20
		padBottom = 30 // only K-value labels, no legend
	)
	plotW := svgW - padLeft - padRight
	plotH := svgH - padTop - padBottom

	nGroups := len(kVals)
	nEngines := len(aggregated)
	groupW := float64(plotW) / float64(nGroups)
	barW := groupW / float64(nEngines+1)

	var sb strings.Builder
	// viewBox makes the chart responsive; width=100% fills the container.
	sb.WriteString(fmt.Sprintf(
		`<svg viewBox="0 0 %d %d" width="100%%" xmlns="http://www.w3.org/2000/svg">`,
		svgW, svgH))

	// Gridlines + Y-axis labels.
	for _, tick := range []float64{0, 0.25, 0.5, 0.75, 1.0} {
		y := padTop + plotH - int(tick*float64(plotH))
		sb.WriteString(fmt.Sprintf(
			`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#e5e7eb" stroke-width="1"/>`,
			padLeft, y, svgW-padRight, y))
		sb.WriteString(fmt.Sprintf(
			`<text x="%d" y="%d" text-anchor="end" font-size="10" fill="#9ca3af">%.2f</text>`,
			padLeft-4, y+4, tick))
	}

	// Bars + value labels.
	for gi, k := range kVals {
		groupX := padLeft + int(float64(gi)*groupW)
		for ei, agg := range aggregated {
			val := math.Min(agg.NDCG[k], 1.0)
			barH := int(val * float64(plotH))
			x := groupX + int(float64(ei)*barW) + int(barW*0.15)
			y := padTop + plotH - barH
			color := engineColors[ei%len(engineColors)]
			sb.WriteString(fmt.Sprintf(
				`<rect x="%d" y="%d" width="%d" height="%d" fill="%s" rx="2"/>`,
				x, y, max(int(barW*0.75), 1), barH, color))
			if barH > 14 {
				sb.WriteString(fmt.Sprintf(
					`<text x="%d" y="%d" text-anchor="middle" font-size="9" fill="#fff">%.2f</text>`,
					x+max(int(barW*0.75), 1)/2, y+12, val))
			}
		}
		// K-value label below group.
		labelX := groupX + int(groupW/2)
		sb.WriteString(fmt.Sprintf(
			`<text x="%d" y="%d" text-anchor="middle" font-size="12" fill="#374151">@%d</text>`,
			labelX, padTop+plotH+18, k))
	}

	sb.WriteString(`</svg>`)
	return template.HTML(sb.String())
}

// buildLatencyChart renders a horizontal bar chart of p50 latency per engine
// on a logarithmic scale, making order-of-magnitude differences visible.
func buildLatencyChart(aggregated []AggregatedEntry) template.HTML {
	if len(aggregated) == 0 {
		return ""
	}

	type latEntry struct {
		name   string
		us     float64 // p50 in microseconds
		valStr string
		color  string
	}
	var entries []latEntry
	for i, agg := range aggregated {
		us := float64(agg.Latency.P50().Microseconds())
		if us < 1 {
			us = 1
		}
		entries = append(entries, latEntry{
			name:   agg.EngineName,
			us:     us,
			valStr: FormatDuration(agg.Latency.P50()),
			color:  engineColors[i%len(engineColors)],
		})
	}

	const (
		svgW   = 560
		barH   = 26
		barGap = 10
		padL   = 120
		padR   = 80
		padV   = 10
	)
	plotW := svgW - padL - padR
	n := len(entries)
	svgH := padV*2 + n*(barH+barGap) - barGap

	// Log-scale range.
	minLog := math.Log10(entries[0].us)
	maxLog := minLog
	for _, e := range entries {
		l := math.Log10(e.us)
		if l < minLog {
			minLog = l
		}
		if l > maxLog {
			maxLog = l
		}
	}
	logRange := maxLog - minLog
	if logRange < 0.3 {
		logRange = 0.3 // minimum range so bars differ visually
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(
		`<svg viewBox="0 0 %d %d" width="100%%" xmlns="http://www.w3.org/2000/svg">`,
		svgW, svgH))

	for i, e := range entries {
		y := padV + i*(barH+barGap)
		cy := y + barH/2

		frac := (math.Log10(e.us) - minLog) / logRange
		bw := int(frac*float64(plotW)) + 6 // +6 minimum so fastest engine is visible

		// Engine name.
		sb.WriteString(fmt.Sprintf(
			`<text x="%d" y="%d" text-anchor="end" font-size="12" fill="#374151" dominant-baseline="middle">%s</text>`,
			padL-8, cy, template.HTMLEscapeString(e.name)))
		// Bar.
		sb.WriteString(fmt.Sprintf(
			`<rect x="%d" y="%d" width="%d" height="%d" fill="%s" rx="3" opacity="0.85"/>`,
			padL, y, bw, barH, e.color))
		// Value label.
		sb.WriteString(fmt.Sprintf(
			`<text x="%d" y="%d" font-size="11" fill="#374151" dominant-baseline="middle">%s</text>`,
			padL+bw+6, cy, e.valStr))
	}

	sb.WriteString(`</svg>`)
	return template.HTML(sb.String())
}
