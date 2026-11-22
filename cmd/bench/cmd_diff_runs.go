package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/bench/report"
	"github.com/tadjov/sengi/internal/bench/trackctx"
	"github.com/tadjov/sengi/internal/cli"
)

type diffFlags struct {
	trackArg string
	pathA    string
	pathB    string
}

func newDiffRunsCmd() *cobra.Command {
	var f diffFlags
	cmd := &cobra.Command{
		Use:   "runs [track]",
		Short: "Compare two benchmark runs side by side",
		Long: `Shows per-engine metric deltas between two reports and the per-query NDCG
regressions, largest first.

Without --b, B is latest.json's report. Without --a, A is the newest earlier
fully scored report, or the newest earlier one with a warning. --a and --b
take run IDs or report paths.`,
		Example: `  bench diff runs tracks/cc-news/fts                               # latest two runs
  bench diff runs tracks/cc-news/fts --b 2026-05-26T23-39-05-run-7e0750
  bench diff runs --a /tmp/before.json --b /tmp/after.json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeDiff(cmd.OutOrStdout(), f, args)
		},
	}
	cmd.Flags().StringVar(&f.trackArg, "track", "", "Track path (relative to --track-root)")
	cmd.Flags().StringVar(&f.pathA, "a", "", "Before run: run ID or path to report.json")
	cmd.Flags().StringVar(&f.pathB, "b", "", "After run: run ID or path to report.json")
	return cmd
}

func executeDiff(w io.Writer, f diffFlags, args []string) error {
	tr, err := trackctx.Resolve(trackInputs(f.trackArg, args))
	if err != nil {
		// If both paths are explicit we don't need a track.
		if f.pathA != "" && f.pathB != "" {
			tr = nil
		} else {
			return err
		}
	}

	rptB, err := afterReport(tr, f.pathB)
	if err != nil {
		return fmt.Errorf("resolve after report: %w", err)
	}
	rptA, err := beforeReport(tr, f.pathA, rptB)
	if err != nil {
		return fmt.Errorf("resolve before report: %w", err)
	}
	if rptA.Provenance.RunID == rptB.Provenance.RunID {
		return fmt.Errorf("both runs resolve to the same report: %s", rptA.Provenance.RunID)
	}

	printDiff(w, rptA, rptB)
	return nil
}

// afterReport defaults to latest.json's report, else the newest fully scored one.
func afterReport(tr *trackctx.Track, flag string) (*report.Report, error) {
	if flag != "" {
		return readExplicitReport(tr, flag)
	}
	if tr == nil {
		return nil, fmt.Errorf("no path or run ID given and no track available")
	}
	rpt, err := report.ReadLatestReport(tr.LatestReportPath())
	if errors.Is(err, fs.ErrNotExist) {
		return previousScoredReport(baselineSearch{ReportsDir: tr.ReportsDir()})
	}
	return rpt, err
}

// beforeReport defaults to the newest fully scored report older than after.
func beforeReport(tr *trackctx.Track, flag string, after *report.Report) (*report.Report, error) {
	if flag != "" {
		return readExplicitReport(tr, flag)
	}
	if tr == nil {
		return nil, fmt.Errorf("no path or run ID given and no track available")
	}
	return previousScoredReport(baselineSearch{ReportsDir: tr.ReportsDir(), BeforeRunID: after.Provenance.RunID})
}

// readExplicitReport reads a report named by a run ID or a path.
func readExplicitReport(tr *trackctx.Track, flag string) (*report.Report, error) {
	if file, ok := fileArg([]string{flag}); ok {
		return report.ReadJSON(file)
	}
	if tr == nil {
		return nil, fmt.Errorf("run ID %q given but no track resolved", flag)
	}
	return report.ReadJSON(tr.ReportPath(flag))
}

// baselineSearch scopes the hunt for a report to diff against.
type baselineSearch struct {
	ReportsDir string
	// BeforeRunID limits the search to older reports; empty searches them all.
	BeforeRunID string
}

// previousScoredReport returns the newest fully scored report, else the newest readable one.
func previousScoredReport(search baselineSearch) (*report.Report, error) {
	dir := search.ReportsDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read reports dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && e.Name() != "latest.json" {
			names = append(names, e.Name())
		}
	}
	// Run IDs are timestamped, so lexicographic order is chronological.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	var newestReadable *report.Report
	for _, name := range names {
		if search.BeforeRunID != "" && strings.TrimSuffix(name, ".json") >= search.BeforeRunID {
			continue
		}
		rpt, err := report.ReadJSON(filepath.Join(dir, name))
		if err != nil {
			slog.Warn("skipping unreadable report in baseline search", "report", name, "error", err)
			continue
		}
		if fullyScored(rpt) {
			return rpt, nil
		}
		if newestReadable == nil {
			newestReadable = rpt
		}
	}
	if newestReadable == nil {
		return nil, fmt.Errorf("no earlier readable report in %s", dir)
	}
	slog.Warn("no earlier report is complete; diffing against the newest one",
		"report", newestReadable.Provenance.RunID)
	return newestReadable, nil
}

// fullyScored reports whether a run can be a baseline: scored, no failed cells, nothing incomplete.
func fullyScored(r *report.Report) bool {
	if len(r.Jobs) == 0 || len(r.Settings.Incomplete) > 0 {
		return false
	}
	for _, job := range r.Jobs {
		judged := false
		for _, agg := range job.Aggregated {
			if agg.ErrorCount > 0 {
				return false
			}
			judged = judged || agg.JudgedCount > 0
		}
		for _, e := range job.PerQuery {
			if e.Error != "" {
				return false
			}
		}
		if !judged {
			return false
		}
	}
	return true
}

func printDiff(w io.Writer, a, b *report.Report) {
	fmt.Fprintf(w, "\n%s\n", cli.Strong.Sprint("Comparing:"))
	fmt.Fprintf(w, "  %s %s\n", cli.Muted.Sprint("A (before):"), a.Provenance.RunID)
	fmt.Fprintf(w, "  %s %s\n\n", cli.Muted.Sprint("B (after): "), cli.Strong.Sprint(b.Provenance.RunID))

	printProvenanceDiff(w, report.DiffProvenance(a, b))
	printScoringWarnings(w, a, b)

	// Find common jobs by name.
	jobsA := indexJobs(a)
	jobsB := indexJobs(b)

	names := unionKeys(jobsA, jobsB)
	sort.Strings(names)

	ndcgK, haveSharedK := largestSharedK(a.Config.KValues, b.Config.KValues)
	if !haveSharedK {
		fmt.Fprintf(w, "%s\n\n", cli.Muted.Sprint("No K value in both runs: NDCG deltas skipped."))
	}

	for _, name := range names {
		jA, okA := jobsA[name]
		jB, okB := jobsB[name]
		if !okA || !okB {
			missing := "B"
			if !okA {
				missing = "A"
			}
			fmt.Fprintf(w, "%s\n", cli.Muted.Sprintf("--- Job: %s (only in %s, skipped) ---", name, missing))
			continue
		}

		fmt.Fprintf(w, "%s\n\n", cli.Strong.Sprintf("--- Job: %s ---", name))
		printAggregateDiff(w, jA, jB, ndcgK)
		if haveSharedK {
			printQueryDiff(w, jA, jB, ndcgK)
		}
		fmt.Fprintln(w)
	}
}

func printProvenanceDiff(w io.Writer, changes []report.ProvenanceChange) {
	if len(changes) == 0 {
		fmt.Fprintf(w, "%s\n\n", cli.Muted.Sprint("Index provenance: unchanged"))
		return
	}
	fmt.Fprintf(w, "%s\n\n", cli.Strong.Sprint("Index provenance changes:"))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Engine\tField\tA\tB")
	fmt.Fprintln(tw, "---\t---\t---\t---")
	for _, c := range changes {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Engine, c.Field, orAbsent(c.Before), orAbsent(c.After))
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func orAbsent(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

// printAggregateDiff shows NDCG at ndcgK, or not at all when ndcgK is 0.
func printAggregateDiff(w io.Writer, jA, jB *report.JobReport, ndcgK int) {
	aggA := indexAgg(jA)
	aggB := indexAgg(jB)
	engines := unionKeys(aggA, aggB)
	sort.Strings(engines)

	ndcgHeader := "NDCG  A→B"
	if ndcgK > 0 {
		ndcgHeader = fmt.Sprintf("NDCG@%d (gain %s)  A→B", ndcgK, report.NDCGGain)
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "Engine\t%s\tMAP  A→B\tMRR  A→B\tp50  A→B\n", ndcgHeader)
	fmt.Fprintln(tw, "---\t---\t---\t---\t---")

	for _, eng := range engines {
		eA, okA := aggA[eng]
		eB, okB := aggB[eng]
		if !okA || !okB {
			fmt.Fprintf(tw, "%s\t(only in one run)\n", eng)
			continue
		}
		ndcg := "—"
		if ndcgK > 0 {
			ndcg = fmtDelta(eA.NDCG[ndcgK], eB.NDCG[ndcgK])
		}
		mapA, mapB := eA.MAP, eB.MAP
		mrrA, mrrB := eA.MRR, eB.MRR
		latA, latB := eA.Latency.P50(), eB.Latency.P50()

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			eng,
			ndcg,
			fmtDelta(mapA, mapB),
			fmtDelta(mrrA, mrrB),
			fmtDurationDelta(latA, latB), // last column — safe to color
		)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func printQueryDiff(w io.Writer, jA, jB *report.JobReport, k int) {
	type delta struct {
		query, engine string
		a, b          float64
		diff          float64
	}

	perA := indexPerQuery(jA)
	perB := indexPerQuery(jB)

	var deltas []delta
	for key, eA := range perA {
		eB, ok := perB[key]
		if !ok {
			continue
		}
		ndcgA, scoredA := scoredNDCG(eA, k)
		ndcgB, scoredB := scoredNDCG(eB, k)
		if !scoredA || !scoredB {
			continue
		}
		d := ndcgB - ndcgA
		if d == 0 {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		deltas = append(deltas, delta{parts[0], parts[1], ndcgA, ndcgB, d})
	}
	if len(deltas) == 0 {
		return
	}
	// Largest regression first, then largest improvement; ties go by query, then engine.
	slices.SortFunc(deltas, func(a, b delta) int {
		return cmp.Or(cmp.Compare(a.diff, b.diff), cmp.Compare(a.query, b.query), cmp.Compare(a.engine, b.engine))
	})

	fmt.Fprintf(w, "%s\n\n", cli.Strong.Sprintf("Per-Query NDCG@%d (gain %s) Deltas (largest regression first):", k, report.NDCGGain))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Query\tEngine\tA\tB\tΔ")
	fmt.Fprintln(tw, "---\t---\t---\t---\t---")
	for _, d := range deltas {
		// Δ is the last column — safe to color.
		var delta string
		if d.diff > 0 {
			delta = cli.Success.Sprintf("↑%+.4f", d.diff)
		} else {
			delta = cli.Failure.Sprintf("↓%+.4f", d.diff)
		}
		fmt.Fprintf(tw, "%s\t%s\t%.4f\t%.4f\t%s\n",
			d.query, d.engine, d.a, d.b, delta)
	}
	tw.Flush()
}

// scoredNDCG scores a failed judged cell 0, as the aggregates do, and skips unjudged cells.
func scoredNDCG(e report.Entry, k int) (float64, bool) {
	if e.Error != "" {
		return 0, e.Judged
	}
	return e.NDCG[k], e.Judged
}

func indexJobs(r *report.Report) map[string]*report.JobReport {
	m := make(map[string]*report.JobReport, len(r.Jobs))
	for i := range r.Jobs {
		m[r.Jobs[i].JobName] = &r.Jobs[i]
	}
	return m
}

func indexAgg(jr *report.JobReport) map[string]report.AggregatedEntry {
	m := make(map[string]report.AggregatedEntry, len(jr.Aggregated))
	for _, a := range jr.Aggregated {
		m[a.EngineName] = a
	}
	return m
}

func indexPerQuery(jr *report.JobReport) map[string]report.Entry {
	m := make(map[string]report.Entry, len(jr.PerQuery))
	for _, e := range jr.PerQuery {
		key := e.QueryID + "\x00" + e.EngineName
		m[key] = e
	}
	return m
}

func unionKeys[V any](a, b map[string]V) []string {
	seen := make(map[string]struct{})
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

func fmtDelta(a, b float64) string {
	d := b - a
	if d == 0 {
		return fmt.Sprintf("%.4f (=)", a)
	}
	arrow := "↑"
	if d < 0 {
		arrow = "↓"
	}
	return fmt.Sprintf("%.4f→%.4f %s%s", a, b, arrow, fmtRelativeChange(d, a))
}

// fmtRelativeChange has no percentage to give for a change from zero.
func fmtRelativeChange(change, baseline float64) string {
	if baseline == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%+.1f%%", change/baseline*100)
}

func fmtDurationDelta(a, b time.Duration) string {
	if a == 0 && b == 0 {
		return "—"
	}
	d := b - a
	if d == 0 {
		return cli.Muted.Sprint(report.FormatDuration(a) + " (=)")
	}
	// For latency: lower is better, so improvement = d < 0.
	improved := d < 0
	arrow := "↑" // up = slower = worse
	if improved {
		arrow = "↓" // down = faster = better
	}
	s := fmt.Sprintf("%s→%s %s%s", report.FormatDuration(a), report.FormatDuration(b), arrow, fmtRelativeChange(float64(d), float64(a)))
	if improved {
		return cli.Success.Sprint(s)
	}
	return cli.Failure.Sprint(s)
}

// largestSharedK is the largest cutoff both runs computed.
func largestSharedK(a, b []int) (int, bool) {
	shared := 0
	for _, k := range a {
		if slices.Contains(b, k) && k > shared {
			shared = k
		}
	}
	return shared, shared > 0
}

// printScoringWarnings flags runs scored against different judgments.
func printScoringWarnings(w io.Writer, a, b *report.Report) {
	var differences []string
	if judgeOf(a) != judgeOf(b) {
		differences = append(differences, fmt.Sprintf("judge: %s → %s", judgeOf(a), judgeOf(b)))
	}
	if a.Config.RelevanceThreshold != b.Config.RelevanceThreshold {
		differences = append(differences, fmt.Sprintf("relevance threshold: grade>=%d → grade>=%d",
			a.Config.RelevanceThreshold, b.Config.RelevanceThreshold))
	}
	if judgmentsOf(a) != judgmentsOf(b) {
		differences = append(differences, fmt.Sprintf("judgments: %s → %s", judgmentsOf(a), judgmentsOf(b)))
	}
	if len(differences) == 0 {
		return
	}
	fmt.Fprintf(w, "%s the runs were scored differently, so metric deltas are not engine changes alone:\n", cli.Caution.Sprint("WARNING:"))
	for _, d := range differences {
		fmt.Fprintf(w, "  %s\n", d)
	}
	fmt.Fprintln(w)
}

const scoringNotRecorded = "not recorded"

func judgeOf(r *report.Report) string {
	judge := r.Provenance.Judge
	if judge == nil {
		return scoringNotRecorded
	}
	if judge.PromptVersion == "" {
		return judge.String()
	}
	return judge.String() + " prompt " + judge.PromptVersion
}

// judgmentsOf names the judgments file relative to the track, so a moved track compares equal.
func judgmentsOf(r *report.Report) string {
	sources := r.Provenance.Sources
	if sources == nil || sources.Judgments == "" {
		return scoringNotRecorded
	}
	return filepath.ToSlash(filepath.Clean(sources.Judgments))
}
