package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/bench/agreement"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/bench/report"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/bench/suite"
	"github.com/tadjov/sengi/internal/bench/trackctx"
	"github.com/tadjov/sengi/internal/cli"
)

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
	tr, err := trackctx.Resolve(trackInputs(args))
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
		cli.Warn(w, "without --pg, gated zeros are included: every gated judge grades a doc the phrase, boolean or filter gate refuses 0 without a model seeing it, which inflates agreement")
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
	fmt.Fprintf(w, "\n%s\n\n", cli.Strong.Sprint("Judgment sets"))
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
	fmt.Fprintf(w, "\n%s\n\n", cli.Strong.Sprint(title))
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
		fmt.Fprintf(w, "\n%s\n", cli.Strong.Sprintf("%s against %s (rows are %s's grades)", c.Label, ref.Label, ref.Label))
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

	fmt.Fprintf(w, "\n%s\n\n", cli.Strong.Sprint("Agreement by query category"))
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
		cli.Warn(w, "the gate refused every common pair, so there is no agreement without its zeros")
		return
	}
	printAgreementSummary(w, fmt.Sprintf("Agreement over the %d pairs the gate admits", gate.Admitted.Pairs), *gate.Admitted)
}

func (c judgmentComparison) printDisagreements(w io.Writer) {
	rows, withTitles := c.result.Disagreements, c.result.Gate != nil
	fmt.Fprintf(w, "\n%s\n\n", cli.Strong.Sprintf("%d pairs whose grades span %d or more", len(rows), c.flags.minGap))
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
	fmt.Fprintf(w, "\n%s\n\n", cli.Strong.Sprintf("Report %s rescored under each set", r.ReportRunID))
	fmt.Fprintf(w, "Rescoring under %s, the set the report was scored with, gives back every stored value of: %s\n",
		r.ReportSet, cmp.Or(strings.Join(r.Metrics, ", "), "none"))
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
		cli.Warn(w, fmt.Sprintf("τ-b needs the reference set rescored, and %s was refused", reference))
	} else {
		fmt.Fprintf(w, "τ-b compares each set's engine means with %s's.\n", r.Reference)
	}
	for _, job := range r.Jobs {
		c.printJobRescoring(w, job)
	}
}

func (c judgmentComparison) printJobRescoring(w io.Writer, job agreement.JobRescoring) {
	fmt.Fprintf(w, "\n%s\n\n", cli.Strong.Sprintf("--- Job: %s (%d engines, %d judged queries) ---", job.Job, len(job.Engines), job.Queries))
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
	cli.Done(w, "Wrote "+path)
	return nil
}
