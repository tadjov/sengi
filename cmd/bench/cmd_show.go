package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/bench/report"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/bench/trackctx"
)

func newReportCmd() *cobra.Command {
	cmd := newShowReportCmd()
	cmd.Short = "Show the latest report for a track (alias: bench show report)"
	return cmd
}

func newShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Inspect bench artifacts (pool, judgments, spec, report)",
		Long: `Pretty-prints a one-page summary of a bench artifact: query counts, grade
histograms, engine coverage, dedup ratios.`,
	}
	cmd.AddCommand(newShowPoolCmd(), newShowJudgmentsCmd(), newShowSpecCmd(), newShowReportCmd())
	return cmd
}

func newShowPoolCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "pool [track|file]",
		Short:   "Summarise a pool file",
		Args:    cobra.MaximumNArgs(1),
		Example: "  bench show pool tracks/cc-news/fts\n  bench show pool /path/to/pool.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := artifactPath(args, func(tr *trackctx.Track) string { return tr.Pool })
			if err != nil {
				return err
			}
			pf, err := pool.ReadPoolFile(path)
			if err != nil {
				return err
			}
			showPool(cmd.OutOrStdout(), pf)
			return nil
		},
	}
}

func newShowJudgmentsCmd() *cobra.Command {
	var strategy string
	cmd := &cobra.Command{
		Use:     "judgments [track|file]",
		Short:   "Summarise a judgments file",
		Args:    cobra.MaximumNArgs(1),
		Example: "  bench show judgments tracks/cc-news/fts --strategy claude-api\n  bench show judgments /path/to/annotations.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := artifactPath(args, func(tr *trackctx.Track) string { return tr.JudgmentsPath(strategy) })
			if err != nil {
				return err
			}
			jf, err := judgment.ReadFile(path)
			if err != nil {
				return err
			}
			showJudgments(cmd.OutOrStdout(), jf)
			return nil
		},
	}
	cmd.Flags().StringVar(&strategy, "strategy", string(judgment.StrategyKeyword), "Judgment set to summarise when the arg is a track")
	return cmd
}

func newShowSpecCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "spec [track|file]",
		Short:   "Summarise a bench spec",
		Args:    cobra.MaximumNArgs(1),
		Example: "  bench show spec tracks/cc-news/fts\n  bench show spec /path/to/spec.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := artifactPath(args, func(tr *trackctx.Track) string { return tr.Spec })
			if err != nil {
				return err
			}
			bs, err := spec.LoadFromFile(path)
			if err != nil {
				return err
			}
			showSpec(cmd.OutOrStdout(), bs)
			return nil
		},
	}
}

func newShowReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report [track|file]",
		Short: "Summarise the latest (or given) report",
		Long: `Prints a report's provenance followed by the metrics, latency and
significance tables of each job. A track shows the report reports/latest.json
points at; a file is read as the report itself.`,
		Args:    cobra.MaximumNArgs(1),
		Example: "  bench show report tracks/cc-news/fts\n  bench show report /path/to/report.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpt, err := readReportArg(args)
			if err != nil {
				return err
			}
			showReport(cmd.OutOrStdout(), rpt)
			return nil
		},
	}
}

func readReportArg(args []string) (*report.Report, error) {
	if file, ok := fileArg(args); ok {
		return report.ReadJSON(file)
	}
	tr, err := trackctx.Resolve(trackInputs("", args))
	if err != nil {
		return nil, err
	}
	return report.ReadLatestReport(tr.LatestReportPath())
}

// artifactPath is the arg itself when it names a file, else the artifact
// inTrack picks from the track the arg resolves to.
func artifactPath(args []string, inTrack func(*trackctx.Track) string) (string, error) {
	if file, ok := fileArg(args); ok {
		return file, nil
	}
	tr, err := trackctx.Resolve(trackInputs("", args))
	if err != nil {
		return "", err
	}
	return inTrack(tr), nil
}

func fileArg(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	info, err := os.Stat(args[0])
	return args[0], err == nil && info.Mode().IsRegular()
}

func showPool(w io.Writer, pf *pool.PoolFile) {
	fmt.Fprintf(w, "Pool: %s\n", pf.SuiteName)
	fmt.Fprintf(w, "Queries: %d\n\n", len(pf.Queries))

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "QUERY\tDOCS\tSOURCES\tDEDUP")
	fmt.Fprintln(tw, "-----\t----\t-------\t-----")

	var totalDocs, totalSourceHits int
	engineCounts := map[string]int{}

	for _, e := range pf.Queries {
		sources := map[string]int{}
		sourceHits := 0
		for _, d := range e.Docs {
			for _, s := range d.Sources {
				sources[s]++
				sourceHits++
				engineCounts[s]++
			}
		}
		totalDocs += len(e.Docs)
		totalSourceHits += sourceHits

		dedup := "—"
		if sourceHits > 0 {
			dedup = fmt.Sprintf("%.0f%%", 100*float64(sourceHits-len(e.Docs))/float64(sourceHits))
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", e.QueryID, len(e.Docs), formatSources(sources), dedup)
	}
	tw.Flush()

	fmt.Fprintf(w, "\nTotal unique docs: %d\n", totalDocs)
	fmt.Fprintf(w, "Total engine hits: %d\n", totalSourceHits)
	fmt.Fprintln(w, "\nPer-engine contribution:")
	for _, e := range slices.Sorted(maps.Keys(engineCounts)) {
		fmt.Fprintf(w, "  %s: %d docs\n", e, engineCounts[e])
	}
}

func showJudgments(w io.Writer, jf *judgment.File) {
	fmt.Fprintf(w, "Judgments (judge=%s)\n", jf.Judge())
	fmt.Fprintf(w, "Queries: %d\n\n", len(jf.Queries))

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "QUERY\tTOTAL\tGRADED\tUNJUDGED\t3 (HI)\t2 (REL)\t1 (MARG)\t0 (NO)")
	fmt.Fprintln(tw, "-----\t-----\t------\t--------\t------\t-------\t--------\t------")

	totals := map[int]int{}
	allTotal, allGraded, allUnjudged := 0, 0, 0

	for _, qe := range jf.Queries {
		h := map[int]int{}
		graded, unjudged := 0, 0
		for _, d := range qe.Docs {
			if d.Grade < 0 {
				unjudged++
				continue
			}
			graded++
			h[d.Grade]++
			totals[d.Grade]++
		}
		allTotal += len(qe.Docs)
		allGraded += graded
		allUnjudged += unjudged
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
			qe.QueryID, len(qe.Docs), graded, unjudged, h[3], h[2], h[1], h[0])
	}
	tw.Flush()

	fmt.Fprintf(w, "\nTotal: %d docs across %d queries\n", allTotal, len(jf.Queries))
	fmt.Fprintf(w, "Graded: %d  Unjudged: %d\n", allGraded, allUnjudged)
	fmt.Fprintf(w, "Distribution: 3=%d  2=%d  1=%d  0=%d\n", totals[3], totals[2], totals[1], totals[0])
}

func showSpec(w io.Writer, bs *spec.BenchSpec) {
	fmt.Fprintln(w, "Engines:")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tTYPE\tCONNECTION\tINDEX\tQUERIES FROM\tPARAMS")
	for _, name := range slices.Sorted(maps.Keys(bs.Engines)) {
		e := bs.Engines[name]
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			name, e.Type, maskConn(e.Connection), e.Index, e.QueriesFrom, formatEngineParams(e.Params))
	}
	tw.Flush()

	fmt.Fprintln(w, "\nJobs:")
	for _, j := range bs.Jobs {
		fmt.Fprintf(w, "  %s\n    suite:   %s\n    engines: %v\n", j.Name, j.Suite, j.Engines)
	}

	fmt.Fprintf(w, "\nMetrics: k=%v threshold=%d\n",
		bs.Metrics.KValues, bs.Metrics.RelevanceThreshold)
	fmt.Fprintf(w, "Runs: warmup=%d iterations=%d\n", bs.Runs.Warmup, bs.Runs.Iterations)
}

func showReport(w io.Writer, rpt *report.Report) {
	p := rpt.Provenance
	fmt.Fprintf(w, "Run ID:    %s\n", p.RunID)
	fmt.Fprintf(w, "Tool:      %s\n", p.Tool)
	fmt.Fprintf(w, "Generated: %s\n", p.GeneratedAt.Format("2006-01-02 15:04:05 UTC"))
	if p.SpecID != "" {
		fmt.Fprintf(w, "Spec:      %s\n", p.SpecID)
	}
	if p.Sources != nil {
		fmt.Fprintln(w, "\nSources:")
		if p.Sources.Spec != "" {
			fmt.Fprintf(w, "  spec:      %s\n", p.Sources.Spec)
		}
		if len(p.Sources.Suites) > 0 {
			fmt.Fprintf(w, "  suites:    %s\n", strings.Join(p.Sources.Suites, ", "))
		}
		if p.Sources.Pool != "" {
			fmt.Fprintf(w, "  pool:      %s\n", p.Sources.Pool)
		}
		if p.Sources.Judgments != "" {
			fmt.Fprintf(w, "  judgments: %s\n", p.Sources.Judgments)
		}
	}
	showEngineIndexes(w, rpt.Environment.Engines)
	fmt.Fprintln(w)
	report.WriteTable(rpt, w)
}

func showEngineIndexes(w io.Writer, engines map[string]report.EngineInfo) {
	if len(engines) == 0 {
		return
	}
	fmt.Fprintln(w, "\nEngines:")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, name := range slices.Sorted(maps.Keys(engines)) {
		info := engines[name]
		summary := "index provenance not recorded"
		if info.Index != nil {
			summary = info.Index.Summary()
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", name, info.Type, summary)
	}
	tw.Flush()
}

func formatSources(sources map[string]int) string {
	if len(sources) == 0 {
		return "—"
	}
	keys := slices.Sorted(maps.Keys(sources))
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = fmt.Sprintf("%s:%d", k, sources[k])
	}
	return strings.Join(pairs, " ")
}

// formatEngineParams renders engine params key-sorted so two arms of an A/B can
// be compared line by line.
func formatEngineParams(params map[string]any) string {
	if len(params) == 0 {
		return ""
	}
	keys := slices.Sorted(maps.Keys(params))
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = fmt.Sprintf("%s=%v", k, params[k])
	}
	return strings.Join(pairs, " ")
}

func maskConn(s string) string {
	if len(s) > 60 {
		return s[:30] + "…" + s[len(s)-15:]
	}
	return s
}
