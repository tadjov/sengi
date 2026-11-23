package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/cli"
	"github.com/tadjov/sengi/internal/ingest"
	"github.com/tadjov/sengi/pkg/apis/preprocess"
	"github.com/tadjov/sengi/pkg/datapackage"
)

type preprocessConfig struct {
	SpecPath string
	// A rerun into an existing Version must come from the same specs and input.
	Version  string
	Packages datapackage.Options
}

func newPreprocessCmd() *cobra.Command {
	cfg := preprocessConfig{}
	cmd := &cobra.Command{
		Use:   "preprocess <preprocess.yaml>",
		Short: "Turn a raw data package into a canonical one, as a preprocess spec says",
		Long: `Reads the raw data package the spec names, maps it through the spec's mapping,
removes duplicates, applies the rules, checks the budgets and publishes
<output.path>/<version>/ with articles, rejects and changes.

  datapipe preprocess datasets/<ds>/preprocess.yaml --version v1

A check over its error budget publishes nothing. A rerun of the same specs over
the same input keeps the published version; any other change needs a new --version.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.SpecPath = args[0]
			cfg.Packages = datapackage.Options{S3: datapackage.S3ConfigFromEnv()}
			return runPreprocess(cmd.Context(), cmd.OutOrStdout(), cfg)
		},
	}
	cmd.Flags().StringVar(&cfg.Version, "version", datapackage.DefaultVersion, "Canonical package version, the folder name under output.path")
	return cmd
}

func runPreprocess(ctx context.Context, out io.Writer, cfg preprocessConfig) error {
	spec, err := preprocess.Load(cfg.SpecPath)
	if err != nil {
		return fmt.Errorf("load preprocess spec: %w", err)
	}
	raw, err := datapackage.OpenWith(ctx, spec.Input, cfg.Packages)
	if err != nil {
		return fmt.Errorf("open raw package: %w", err)
	}

	command := cli.Command{
		Title: "sengi preprocess",
		Fields: []cli.Field{
			{Label: "spec", Value: cfg.SpecPath},
			{Label: "raw", Value: raw.Location},
			cli.IntField("resources", len(raw.Package.Resources)),
			{Label: "output", Value: spec.OutputRoot},
			{Label: "version", Value: cfg.Version},
		},
		Label: "preprocess",
		Unit:  "records",
	}
	return command.Run(out, func(p *cli.Progress) (cli.Summary, error) {
		result, err := ingest.Preprocess(ctx, ingest.PreprocessOptions{
			Raw:        raw,
			Spec:       spec.Spec,
			Mapping:    spec.Mapping,
			OutputRoot: spec.OutputRoot,
			Version:    cfg.Version,
			ProducedBy: datapackage.ProducedBy{Tool: "datapipe preprocess"},
			Tool:       cliName + "/" + cliVersion,
			Progress:   p,
		})
		var conflict *datapackage.VersionConflictError
		if errors.As(err, &conflict) {
			return cli.Summary{}, fmt.Errorf("canonical version %s already holds a package made from other specs or input; publish this run under a new --version: %w", conflict.Version, err)
		}
		if err != nil {
			return cli.Summary{}, err
		}
		return preprocessSummary(result), nil
	})
}

func preprocessSummary(result datapackage.Published) cli.Summary {
	record := result.Package.Preprocess
	counts := record.Counts
	dropped, duplicates, cleared := total(counts.Dropped), total(counts.Duplicates), total(counts.Cleared)
	summary := cli.Summary{
		Message: "Preprocess complete",
		Fields: []cli.Field{
			{Label: "read", Value: strconv.FormatInt(counts.Read, 10)},
			{Label: "written", Value: strconv.FormatInt(counts.Written, 10)},
			{Label: "dropped", Value: strconv.FormatInt(dropped, 10)},
			{Label: "duplicates", Value: strconv.FormatInt(duplicates, 10)},
			{Label: "cleared", Value: strconv.FormatInt(cleared, 10)},
			{Label: "version", Value: result.Package.Version},
			{Label: "hash", Value: result.Package.Resources[0].Hash},
			{Label: "package", Value: result.Dir},
		},
	}
	if dropped > 0 {
		summary.Notes = append(summary.Notes, cli.Note{Text: fmt.Sprintf("%d dropped: %s", dropped, breakdown(counts.Dropped)), Warning: true})
	}
	if duplicates > 0 {
		summary.Notes = append(summary.Notes, cli.Note{Text: fmt.Sprintf("%d duplicates: %s", duplicates, breakdown(counts.Duplicates))})
	}
	if cleared > 0 {
		summary.Notes = append(summary.Notes, cli.Note{Text: fmt.Sprintf("%d fields cleared: %s", cleared, breakdown(counts.Cleared))})
	}
	for _, name := range slices.Sorted(maps.Keys(record.Checks)) {
		check := record.Checks[name]
		if check.Result == datapackage.CheckWarn {
			summary.Notes = append(summary.Notes, cli.Note{Text: fmt.Sprintf("check %s: %d (%.2f%%) is over its warn budget %s", name, check.Count, 100*check.Share, check.Warn), Warning: true})
		}
	}
	if result.Restored.Any() {
		summary.Notes = append(summary.Notes, cli.Note{Text: restoredNote(result.Package.Version, result.Restored), Warning: len(result.Restored.Mismatched) > 0})
	} else if result.Existing {
		summary.Notes = append(summary.Notes, cli.Note{Text: "version " + result.Package.Version + " was already published; kept it and discarded this run's copy"})
	}
	return summary
}

// restoredNote says what a rerun wrote into an existing version, such as
// "version v1: replaced articles.parquet, which did not match its descriptor; restored rejects.parquet".
func restoredNote(version string, restored datapackage.Restored) string {
	var parts []string
	if len(restored.Mismatched) > 0 {
		parts = append(parts, "replaced "+strings.Join(restored.Mismatched, ", ")+", which did not match its descriptor")
	}
	if len(restored.Missing) > 0 {
		parts = append(parts, "restored "+strings.Join(restored.Missing, ", "))
	}
	return "version " + version + ": " + strings.Join(parts, "; ")
}

func total(counts map[string]int64) int64 {
	var sum int64
	for _, n := range counts {
		sum += n
	}
	return sum
}

func breakdown(counts map[string]int64) string {
	reasons := slices.Sorted(maps.Keys(counts))
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%d %s", counts[reason], reason))
	}
	return strings.Join(parts, ", ")
}
