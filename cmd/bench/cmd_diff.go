package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/tadjov/tusker/internal/bench/agreement"
	"github.com/tadjov/tusker/internal/bench/pool"
	"github.com/tadjov/tusker/internal/bench/report"
	"github.com/tadjov/tusker/internal/bench/spec"
	"github.com/tadjov/tusker/internal/bench/suite"
	"github.com/tadjov/tusker/internal/bench/trackctx"
)

func newDiffCmd() *cobra.Command { return newDiffCmdWithReader(openArticleReader) }

func newDiffCmdWithReader(openReader articleReaderOpener) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff runs|judgments [track]",
		Short: "Compare two benchmark runs, or a track's judgment sets",
		Long: `bench diff runs compares two benchmark runs of a track.
bench diff judgments compares two or more judgment sets of a track.`,
		// Without RunE, cobra prints the help and exits 0 for `bench diff fts`.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return fmt.Errorf("unknown command %[1]q for %[2]q: compare two runs with bench diff runs %[1]s, or judgment sets with bench diff judgments %[1]s --judgments <set,...>",
				args[0], cmd.CommandPath())
		},
	}
	cmd.AddCommand(newDiffRunsCmd(), newDiffJudgmentsCmd(openReader))
	return cmd
}

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
		Example: `  bench diff runs tracks/global-news-dataset/fts_quality                               # latest two runs
  bench diff runs tracks/global-news-dataset/fts_quality --b 2026-05-26T23-39-05-run-7e0750
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
		return previousScoredReport(baselineSearch{ReportsDir: reportsDir(tr)})
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
	return previousScoredReport(baselineSearch{ReportsDir: reportsDir(tr), BeforeRunID: after.Provenance.RunID})
}

func reportsDir(tr *trackctx.Track) string {
	return filepath.Dir(tr.LatestReportPath())
}

// readExplicitReport reads a report named by a run ID or a path.
func readExplicitReport(tr *trackctx.Track, flag string) (*report.Report, error) {
	path := flag
	if !looksLikePath(flag) {
		if tr == nil {
			return nil, fmt.Errorf("run ID %q given but no track resolved", flag)
		}
		path = tr.ReportPath(flag)
	}
	return report.ReadJSON(path)
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
	fmt.Fprintf(w, "\n%s\n", cBold.Sprint("Comparing:"))
	fmt.Fprintf(w, "  %s %s\n", cDim.Sprint("A (before):"), a.Provenance.RunID)
	fmt.Fprintf(w, "  %s %s\n\n", cDim.Sprint("B (after): "), cBold.Sprint(b.Provenance.RunID))

	printProvenanceDiff(w, report.DiffProvenance(a, b))
	printScoringWarnings(w, a, b)

	// Find common jobs by name.
	jobsA := indexJobs(a)
	jobsB := indexJobs(b)

	names := unionKeys(jobsA, jobsB)
	sort.Strings(names)

	ndcgK, haveSharedK := largestSharedK(a.Config.KValues, b.Config.KValues)
	if !haveSharedK {
		fmt.Fprintf(w, "%s\n\n", cDim.Sprint("No K value in both runs: NDCG deltas skipped."))
	}

	for _, name := range names {
		jA, okA := jobsA[name]
		jB, okB := jobsB[name]
		if !okA || !okB {
			missing := "B"
			if !okA {
				missing = "A"
			}
			fmt.Fprintf(w, "%s\n", cDim.Sprintf("--- Job: %s (only in %s, skipped) ---", name, missing))
			continue
		}

		fmt.Fprintf(w, "%s\n\n", cBold.Sprintf("--- Job: %s ---", name))
		printAggregateDiff(w, jA, jB, ndcgK)
		if haveSharedK {
			printQueryDiff(w, jA, jB, ndcgK)
		}
		fmt.Fprintln(w)
	}
}

func printProvenanceDiff(w io.Writer, changes []report.ProvenanceChange) {
	if len(changes) == 0 {
		fmt.Fprintf(w, "%s\n\n", cDim.Sprint("Index provenance: unchanged"))
		return
	}
	fmt.Fprintf(w, "%s\n\n", cBold.Sprint("Index provenance changes:"))
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
			ndcg = fmtDelta("%.4f", eA.NDCG[ndcgK], eB.NDCG[ndcgK], true)
		}
		mapA, mapB := eA.MAP, eB.MAP
		mrrA, mrrB := eA.MRR, eB.MRR
		latA, latB := eA.Latency.P50(), eB.Latency.P50()

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			eng,
			ndcg,
			fmtDelta("%.4f", mapA, mapB, true),
			fmtDelta("%.4f", mrrA, mrrB, true),
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

	fmt.Fprintf(w, "%s\n\n", cBold.Sprintf("Per-Query NDCG@%d (gain %s) Deltas (largest regression first):", k, report.NDCGGain))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Query\tEngine\tA\tB\tΔ")
	fmt.Fprintln(tw, "---\t---\t---\t---\t---")
	for _, d := range deltas {
		// Δ is the last column — safe to color.
		var delta string
		if d.diff > 0 {
			delta = cOK.Sprintf("↑%+.4f", d.diff)
		} else {
			delta = cFail.Sprintf("↓%+.4f", d.diff)
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

func fmtDelta(format string, a, b float64, higherBetter bool) string {
	d := b - a
	if d == 0 {
		return fmt.Sprintf(format+" (=)", a)
	}
	improved := (d > 0) == higherBetter
	arrow := "↑"
	if !improved {
		arrow = "↓"
	}
	return fmt.Sprintf(format+"→"+format+" %s%s", a, b, arrow, fmtRelativeChange(d, a))
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
		return cDim.Sprint(durStr(a) + " (=)")
	}
	// For latency: lower is better, so improvement = d < 0.
	improved := d < 0
	arrow := "↑" // up = slower = worse
	if improved {
		arrow = "↓" // down = faster = better
	}
	s := fmt.Sprintf("%s→%s %s%s", durStr(a), durStr(b), arrow, fmtRelativeChange(float64(d), float64(a)))
	if improved {
		return cOK.Sprint(s)
	}
	return cFail.Sprint(s)
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
	fmt.Fprintf(w, "%s the runs were scored differently, so metric deltas are not engine changes alone:\n", cWarn.Sprint("WARNING:"))
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
	if !filepath.IsAbs(sources.Judgments) {
		return filepath.ToSlash(filepath.Clean(sources.Judgments))
	}
	if sources.Spec != "" {
		// An older report named its files absolutely, under the track it ran in.
		ranIn := &trackctx.Track{Root: filepath.Dir(sources.Spec)}
		return ranIn.Relative(sources.Judgments)
	}
	return filepath.Clean(sources.Judgments)
}

func durStr(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.1fµs", float64(d.Microseconds()))
	}
	if d < time.Second {
		return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000)
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

type diffJudgmentsFlags struct {
	judgments []string
	reference string
	pairs     bool
	minGap    int
	report    string
	pg        string
	seed      uint64
	output    string
}

type diffJudgmentsCommand struct {
	flags      diffJudgmentsFlags
	openReader articleReaderOpener
}

func newDiffJudgmentsCmd(openReader articleReaderOpener) *cobra.Command {
	var f diffJudgmentsFlags
	cmd := &cobra.Command{
		Use:   "judgments [track] --judgments <[label=]set,...>",
		Short: "Measure how alike judgment sets grade the same pairs",
		Long: `Compares judgment sets over the (query, doc) pairs they all graded.

Each --judgments entry is a set name or an annotations path, optionally
labelled (opus=cross-judge/annotations.opus.yaml). Without a label, a set is
named after its judge's model.

For each pair of sets it prints exact, within-one and relevant agreement, and
quadratic and binary kappa with 95% intervals. Relevant uses the spec's
relevance_threshold.`,
		Example: `  bench diff judgments tracks/cc-news/fts --judgments sonnet=claude-cli,jev
  bench diff judgments tracks/cc-news/fts --judgments sonnet=claude-cli,jev,opus=trec/cross-judge/annotations.opus.yaml --reference opus
  bench diff judgments tracks/cc-news/fts --judgments sonnet=claude-cli,jev --pairs --min-gap 3
  bench diff judgments tracks/cc-news/fts --judgments sonnet=claude-cli,jev --report latest --output /tmp/agreement.json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return diffJudgmentsCommand{flags: f, openReader: openReader}.execute(cmd, args)
		},
	}
	cmd.Flags().StringSliceVar(&f.judgments, "judgments", nil, "Judgment sets to compare: [label=]<set name or annotations path>, comma-separated")
	cmd.Flags().StringVar(&f.reference, "reference", "", "Label to tabulate the other sets against, and to rank engines against with --report")
	cmd.Flags().BoolVar(&f.pairs, "pairs", false, "List the pairs whose grades span at least --min-gap")
	cmd.Flags().IntVar(&f.minGap, "min-gap", 2, "Smallest grade span --pairs lists")
	cmd.Flags().StringVar(&f.report, "report", "", "Run report to rescore under each set: a path, or latest")
	cmd.Flags().StringVar(&f.pg, "pg", "", "Postgres connection to read the articles from and re-run the gate")
	cmd.Flags().Uint64Var(&f.seed, "seed", 1, "Seed of the bootstrap that sets each kappa's interval")
	cmd.Flags().StringVar(&f.output, "output", "", "Also write the results as JSON to this path")
	_ = cmd.MarkFlagRequired("judgments")
	return cmd
}

func (d diffJudgmentsCommand) execute(cmd *cobra.Command, args []string) error {
	if d.flags.minGap < 0 {
		return fmt.Errorf("--min-gap must be 0 or more, got %d", d.flags.minGap)
	}
	if cmd.Flags().Changed("min-gap") && !d.flags.pairs {
		return errors.New("--min-gap sets what --pairs lists; pass --pairs too")
	}
	c, err := d.compare(cmd, args)
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	c.print(w)
	if d.flags.output == "" {
		return nil
	}
	return c.writeArtifact(w)
}

type judgmentComparison struct {
	flags  diffJudgmentsFlags
	sets   []agreement.Set
	result agreement.Artifact
}

func (d diffJudgmentsCommand) compare(cmd *cobra.Command, args []string) (judgmentComparison, error) {
	f := d.flags
	tr, err := trackctx.Resolve(trackInputs("", args))
	if err != nil {
		return judgmentComparison{}, err
	}
	bs, err := spec.LoadFromFile(tr.Spec)
	if err != nil {
		return judgmentComparison{}, fmt.Errorf("load spec %s: %w", tr.Spec, err)
	}
	sets, err := agreement.Load(tr, f.judgments)
	if err != nil {
		return judgmentComparison{}, err
	}
	pairs, err := agreement.Align(sets)
	if err != nil {
		return judgmentComparison{}, err
	}
	var pf *pool.PoolFile
	if f.reference != "" || f.pg != "" {
		if pf, err = pool.ReadPoolFile(tr.Pool); err != nil {
			return judgmentComparison{}, fmt.Errorf("read the pool %s for each query's category and gate: %w", tr.Pool, err)
		}
	}
	opts := agreement.SummaryOptions{
		Labels:     setLabels(sets),
		Threshold:  bs.Metrics.RelevanceThreshold,
		Seed:       f.seed,
		Reference:  f.reference,
		Categories: poolCategories(pf),
	}
	all, err := agreement.Summarize(pairs, opts)
	if err != nil {
		return judgmentComparison{}, err
	}

	var gate *agreement.GateSummary
	var titles map[uuid.UUID]string
	if f.pg != "" {
		reader, err := d.openReader(cmd, f.pg)
		if err != nil {
			return judgmentComparison{}, err
		}
		gated, err := agreement.Gate(cmd.Context(), agreement.GateOptions{Pairs: pairs, Pool: pf, Reader: reader})
		if err != nil {
			return judgmentComparison{}, err
		}
		if gate, err = gateSummary(gated, opts); err != nil {
			return judgmentComparison{}, err
		}
		titles = gated.Titles
	}
	var disagreements []agreement.Disagreement
	if f.pairs {
		disagreements = agreement.Disagreements(pairs, f.minGap)
		for i := range disagreements {
			disagreements[i].Title = titles[disagreements[i].DocID]
		}
	}
	var rescoring *agreement.Rescoring
	reportRunID := ""
	if f.report != "" {
		rpt, err := readRescoredReport(tr, f.report)
		if err != nil {
			return judgmentComparison{}, err
		}
		rescored, err := agreement.Rescore(agreement.RescoreOptions{
			Report:    rpt,
			Sets:      sets,
			Reference: cmp.Or(f.reference, sets[0].Label),
			KValues:   bs.Metrics.KValues,
			Threshold: bs.Metrics.RelevanceThreshold,
		})
		if err != nil {
			return judgmentComparison{}, err
		}
		rescoring, reportRunID = &rescored, rescored.ReportRunID
	}

	result := agreement.NewArtifact(sets, reportRunID)
	result.RelevanceThreshold, result.Seed = opts.Threshold, f.seed
	result.AllPairs, result.Gate, result.Disagreements, result.Rescoring = all, gate, disagreements, rescoring
	return judgmentComparison{flags: f, sets: sets, result: result}, nil
}

func gateSummary(gated agreement.Gated, opts agreement.SummaryOptions) (*agreement.GateSummary, error) {
	gate := &agreement.GateSummary{Refused: gated.Refused}
	if len(gated.Admitted) == 0 {
		return gate, nil
	}
	admitted, err := agreement.Summarize(gated.Admitted, opts)
	if err != nil {
		return nil, err
	}
	gate.Admitted = &admitted
	return gate, nil
}

func (c judgmentComparison) print(w io.Writer) {
	r := c.result
	printJudgmentSets(w, c.sets)
	fmt.Fprintf(w, "%d (query, doc) pairs graded 0 to 3 by every set; relevant means a grade of %d or more (metrics.relevance_threshold)\n",
		r.AllPairs.Pairs, r.RelevanceThreshold)
	if r.Gate == nil {
		printWarn(w, "without --pg, gated zeros are included: every gated judge grades a doc the phrase, boolean or filter gate refuses 0 without a model seeing it, which inflates agreement")
		printAgreementSummary(w, fmt.Sprintf("Agreement over all %d common pairs", r.AllPairs.Pairs), r.AllPairs)
	} else {
		printAgreementSummary(w, fmt.Sprintf("Agreement over all %d common pairs, gated zeros included", r.AllPairs.Pairs), r.AllPairs)
		c.printGate(w)
	}
	if c.flags.pairs {
		c.printDisagreements(w)
	}
	if r.Rescoring != nil {
		c.printRescoring(w)
	}
}

func setLabels(sets []agreement.Set) []string {
	labels := make([]string, len(sets))
	for i, s := range sets {
		labels[i] = s.Label
	}
	return labels
}

func poolCategories(pf *pool.PoolFile) map[string]suite.Category {
	if pf == nil {
		return nil
	}
	categories := make(map[string]suite.Category, len(pf.Queries))
	for _, entry := range pf.Queries {
		categories[entry.QueryID] = entry.Category
	}
	return categories
}

func readRescoredReport(tr *trackctx.Track, value string) (*report.Report, error) {
	if value == "latest" {
		return report.ReadLatestReport(tr.LatestReportPath())
	}
	return report.ReadJSON(value)
}

func printJudgmentSets(w io.Writer, sets []agreement.Set) {
	fmt.Fprintf(w, "\n%s\n\n", cBold.Sprint("Judgment sets"))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Label\tJudge\tGraded\tRun ID\tPath")
	fmt.Fprintln(tw, "---\t---\t---\t---\t---")
	for _, s := range sets {
		graded, _ := s.File.GradeCounts()
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", s.Label, s.File.Judge(), graded, s.File.Meta.RunID, s.Path)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func printAgreementSummary(w io.Writer, title string, s agreement.Summary) {
	fmt.Fprintf(w, "\n%s\n\n", cBold.Sprint(title))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Set\tGrade 0\tGrade 1\tGrade 2\tGrade 3")
	fmt.Fprintln(tw, "---\t---\t---\t---\t---")
	for _, d := range s.Distributions {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\n", d.Label, d.Grades[0], d.Grades[1], d.Grades[2], d.Grades[3])
	}
	tw.Flush()
	fmt.Fprintln(w)

	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Pair\tn\tExact\tWithin 1\tRelevant\tQuadratic κ [95% CI]\tBinary κ [95% CI]")
	fmt.Fprintln(tw, "---\t---\t---\t---\t---\t---\t---")
	for _, c := range s.Comparisons {
		fmt.Fprintf(tw, "%s\t%s\n", comparisonName(c), comparisonCells(c))
	}
	tw.Flush()
	if s.Reference != nil {
		printReference(w, s.Reference)
	}
}

func comparisonName(c agreement.Comparison) string { return c.First + " vs " + c.Second }

func comparisonCells(c agreement.Comparison) string {
	return fmt.Sprintf("%d\t%.4f\t%.4f\t%.4f\t%s\t%s", c.Pairs, c.Exact, c.WithinOne, c.Relevant, formatKappa(c.QuadraticKappa), formatKappa(c.BinaryKappa))
}

func formatKappa(k agreement.Kappa) string {
	if math.IsNaN(float64(k.Value)) {
		return "n/a"
	}
	return fmt.Sprintf("%.4f [%s, %s]", k.Value, formatStatistic(k.Low), formatStatistic(k.High))
}

func formatStatistic(s agreement.Statistic) string {
	if math.IsNaN(float64(s)) {
		return "n/a"
	}
	return fmt.Sprintf("%.4f", float64(s))
}

func printReference(w io.Writer, ref *agreement.Reference) {
	for _, c := range ref.Against {
		fmt.Fprintf(w, "\n%s\n", cBold.Sprintf("%s against %s (rows are %s's grades)", c.Label, ref.Label, ref.Label))
		fmt.Fprintf(w, "%s graded %d of %s's %d irrelevant pairs relevant and %d of its %d relevant pairs irrelevant\n\n",
			c.Label, c.GradedRelevant, ref.Label, c.ReferenceIrrelevant, c.GradedIrrelevant, c.ReferenceRelevant)
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "%s \\ %s\t0\t1\t2\t3\n", ref.Label, c.Label)
		fmt.Fprintln(tw, "---\t---\t---\t---\t---")
		for grade, row := range c.Table {
			fmt.Fprintf(tw, "%d\t%d\t%d\t%d\t%d\n", grade, row[0], row[1], row[2], row[3])
		}
		tw.Flush()
	}

	fmt.Fprintf(w, "\n%s\n\n", cBold.Sprint("Agreement by query category"))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Category\tQueries\tPair\tn\tExact\tWithin 1\tRelevant\tQuadratic κ [95% CI]\tBinary κ [95% CI]")
	fmt.Fprintln(tw, "---\t---\t---\t---\t---\t---\t---\t---\t---")
	for _, category := range ref.ByCategory {
		name := cmp.Or(string(category.Category), "(none)")
		for _, c := range category.Comparisons {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", name, category.Queries, comparisonName(c), comparisonCells(c))
		}
	}
	tw.Flush()
}

func (c judgmentComparison) printGate(w io.Writer) {
	gate := c.result.Gate
	fmt.Fprintf(w, "\nThe gate refused %d of %d common pairs on their phrase, boolean or filter: every gated judge graded those 0 without a model seeing them.\n",
		gate.Refused, c.result.AllPairs.Pairs)
	if gate.Admitted == nil {
		printWarn(w, "the gate refused every common pair, so there is no agreement without its zeros")
		return
	}
	printAgreementSummary(w, fmt.Sprintf("Agreement over the %d pairs the gate admits", gate.Admitted.Pairs), *gate.Admitted)
}

func (c judgmentComparison) printDisagreements(w io.Writer) {
	rows, withTitles := c.result.Disagreements, c.result.Gate != nil
	fmt.Fprintf(w, "\n%s\n\n", cBold.Sprintf("%d pairs whose grades span %d or more", len(rows), c.flags.minGap))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	header := append([]string{"Query", "Doc", "Span"}, setLabels(c.sets)...)
	if withTitles {
		header = append(header, "Title")
	}
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	fmt.Fprintln(tw, strings.TrimSuffix(strings.Repeat("---\t", len(header)), "\t"))
	for _, row := range rows {
		cells := []string{row.QueryID, row.DocID.String(), strconv.Itoa(row.Span)}
		for _, grade := range row.Grades {
			cells = append(cells, strconv.Itoa(grade))
		}
		if withTitles {
			cells = append(cells, row.Title)
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	tw.Flush()
}

func (c judgmentComparison) printRescoring(w io.Writer) {
	r, reference := c.result.Rescoring, cmp.Or(c.flags.reference, c.sets[0].Label)
	fmt.Fprintf(w, "\n%s\n\n", cBold.Sprintf("Report %s rescored under each set", r.ReportRunID))
	fmt.Fprintf(w, "Rescoring under %s, the set the report was scored with, gives back every stored value of: %s\n",
		r.ReportSet, orNone(strings.Join(r.Metrics, ", ")))
	if len(r.LeftOut) > 0 {
		fmt.Fprintf(w, "Left out, since %s does not give back the stored value:\n", r.ReportSet)
		for _, e := range r.LeftOut {
			fmt.Fprintf(w, "  %s: %s\n", e.Name, e.Reason)
		}
	}
	if len(r.Refused) > 0 {
		fmt.Fprintln(w, "Refused, since a set rescores only when it grades every doc in each ranking's top max(k_values):")
		for _, e := range r.Refused {
			fmt.Fprintf(w, "  %s: %s\n", e.Name, e.Reason)
		}
	}
	if r.Reference == "" {
		printWarn(w, fmt.Sprintf("τ-b needs the reference set rescored, and %s was refused", reference))
	} else {
		fmt.Fprintf(w, "τ-b compares each set's engine means with %s's.\n", r.Reference)
	}
	for _, job := range r.Jobs {
		c.printJobRescoring(w, job)
	}
}

func orNone(s string) string { return cmp.Or(s, "none") }

func (c judgmentComparison) printJobRescoring(w io.Writer, job agreement.JobRescoring) {
	fmt.Fprintf(w, "\n%s\n\n", cBold.Sprintf("--- Job: %s (%d engines, %d judged queries) ---", job.Job, len(job.Engines), job.Queries))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "Metric\tSet\t%s\tOrder\tτ-b\n", strings.Join(job.Engines, "\t"))
	fmt.Fprintln(tw, strings.TrimSuffix(strings.Repeat("---\t", len(job.Engines)+4), "\t"))
	for _, m := range job.Metrics {
		for i, order := range m.Sets {
			metric := m.Metric
			if i > 0 {
				metric = ""
			}
			cells := make([]string, 0, len(job.Engines)+4)
			cells = append(cells, metric, order.Label)
			for _, engine := range job.Engines {
				cells = append(cells, fmt.Sprintf("%.4f", order.Means[engine]))
			}
			tau := formatStatistic(order.TauB)
			if order.Label == c.result.Rescoring.Reference {
				tau = "—"
			}
			cells = append(cells, formatEngineOrder(order), tau)
			fmt.Fprintln(tw, strings.Join(cells, "\t"))
		}
	}
	tw.Flush()
}

// formatEngineOrder joins tied engines with =, since τ-b counts ties.
func formatEngineOrder(order agreement.EngineOrder) string {
	var b strings.Builder
	for i, engine := range order.Order {
		if i > 0 {
			separator := " > "
			if order.Means[engine] == order.Means[order.Order[i-1]] {
				separator = " = "
			}
			b.WriteString(separator)
		}
		b.WriteString(engine)
	}
	return b.String()
}

func (c judgmentComparison) writeArtifact(w io.Writer) error {
	path := c.flags.output
	data, err := json.MarshalIndent(c.result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	printDone(w, "Wrote "+path)
	return nil
}
