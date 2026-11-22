package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/cli"
	"github.com/tadjov/sengi/internal/datapackage"
	"github.com/tadjov/sengi/internal/ingest"
	"github.com/tadjov/sengi/internal/ingest/datamapping"
)

type preprocessConfig struct {
	InputPath   string
	OutputPath  string
	MappingPath string
	// A rerun into an existing Version must yield the same articles bytes.
	Version  string
	Packages datapackage.Options
}

func newPreprocessCmd() *cobra.Command {
	var cfg preprocessConfig
	cmd := &cobra.Command{
		Use:   "preprocess <raw-package>",
		Short: "Map a raw data package into a canonical data package",
		Long: `preprocess reads a raw data package (a datapackage.json or its folder, https://
or s3://) and maps it through --mapping into a canonical package under --output.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.InputPath = args[0]
			if ext := strings.ToLower(filepath.Ext(cfg.MappingPath)); ext != ".yaml" {
				return fmt.Errorf("mapping %s has extension %q, want .yaml", cfg.MappingPath, ext)
			}
			cfg.Packages = datapackage.Options{S3: datapackage.S3ConfigFromEnv()}
			return runPreprocess(cmd.Context(), cmd.OutOrStdout(), cfg)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.OutputPath, "output", "", "Folder that receives the canonical package as <version>/, e.g. datasets/<ds>/canonical")
	f.StringVar(&cfg.MappingPath, "mapping", "", "Path to the YAML field-mapping config")
	f.StringVar(&cfg.Version, "version", datapackage.DefaultVersion, "Canonical package version, the folder name under --output")
	for _, required := range []string{"output", "mapping"} {
		_ = cmd.MarkFlagRequired(required)
	}
	return cmd
}

func runPreprocess(ctx context.Context, out io.Writer, cfg preprocessConfig) error {
	mapping, err := loadPreprocessMapping(cfg.MappingPath)
	if err != nil {
		return err
	}
	raw, err := datapackage.OpenWith(ctx, cfg.InputPath, cfg.Packages)
	if err != nil {
		return fmt.Errorf("open raw package: %w", err)
	}

	command := cli.Command{
		Title: "sengi preprocess",
		Fields: []cli.Field{
			{Label: "raw", Value: raw.Location},
			cli.IntField("resources", len(raw.Package.Resources)),
			{Label: "mapping", Value: cfg.MappingPath},
			{Label: "output", Value: cfg.OutputPath},
			{Label: "version", Value: cfg.Version},
		},
		Label: "preprocess",
		Unit:  "records",
	}
	return command.Run(out, func(p *cli.Progress) (cli.Summary, error) {
		result, err := ingest.Preprocess(ctx, ingest.PreprocessOptions{
			Raw:        raw,
			Mapping:    mapping,
			OutputRoot: cfg.OutputPath,
			Version:    cfg.Version,
			ProducedBy: datapackage.ProducedBy{Tool: "datapipe preprocess"},
			Progress:   p,
		})
		var conflict *datapackage.VersionConflictError
		if errors.As(err, &conflict) {
			return cli.Summary{}, fmt.Errorf("canonical version %s already holds other articles; publish this run under a new --version: %w", conflict.Version, err)
		}
		if err != nil {
			return cli.Summary{}, err
		}
		return preprocessSummary(result), nil
	})
}

func loadPreprocessMapping(path string) (*datamapping.DataMapper, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open mapping config: %w", err)
	}
	defer f.Close()
	mapping, err := datamapping.Load(f)
	if err != nil {
		return nil, fmt.Errorf("load mapping %s: %w", path, err)
	}
	return mapping, nil
}

func preprocessSummary(result datapackage.Published) cli.Summary {
	report := result.Package.Report
	dropped, deduped := report.DroppedTotal(), report.DedupedTotal()
	summary := cli.Summary{
		Message: "Preprocess complete",
		Fields: []cli.Field{
			cli.CountField("read", report.Read),
			cli.CountField("written", report.Written),
			cli.CountField("dropped", dropped),
			cli.CountField("deduped", deduped),
			{Label: "version", Value: result.Package.Version},
			{Label: "hash", Value: result.Package.Resources[0].Hash},
			{Label: "package", Value: result.Dir},
		},
	}
	if dropped > 0 {
		summary.Notes = append(summary.Notes, cli.Note{Text: fmt.Sprintf("%d dropped: %s", dropped, breakdown(report.Dropped)), Warning: true})
	}
	if deduped > 0 {
		summary.Notes = append(summary.Notes, cli.Note{Text: fmt.Sprintf("%d deduped: %s", deduped, breakdown(report.Deduped))})
	}
	if result.Restored {
		summary.Notes = append(summary.Notes, cli.Note{Text: "version " + result.Package.Version + " was published without its resource; restored it from this run"})
	} else if result.Existing {
		summary.Notes = append(summary.Notes, cli.Note{Text: "version " + result.Package.Version + " was already published; kept it and discarded this run's copy"})
	}
	return summary
}

func breakdown(counts map[string]int64) string {
	reasons := slices.Sorted(maps.Keys(counts))
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%d %s", counts[reason], reason))
	}
	return strings.Join(parts, ", ")
}
