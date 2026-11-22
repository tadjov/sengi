package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/cli"
	"github.com/tadjov/sengi/internal/datapackage"
	"github.com/tadjov/sengi/internal/ingest"
	"github.com/tadjov/sengi/internal/ingest/reader"
	"github.com/tadjov/sengi/pkg/apis/datamapping"
	"github.com/tadjov/sengi/pkg/config/env"
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
		Use:   "preprocess",
		Short: "Map a raw data package into a canonical data package",
		RunE: func(cmd *cobra.Command, _ []string) error {
			applyPreprocessEnvDefaults(&cfg)
			if cfg.InputPath == "" || cfg.OutputPath == "" || cfg.MappingPath == "" {
				return errors.New("--input, --output and --mapping are required")
			}
			if err := validateFileExt(cfg.MappingPath, preprocessMappingExtensions); err != nil {
				return fmt.Errorf("invalid mapping file ext: %w", err)
			}
			cfg.Packages = datapackage.Options{S3: datapackage.S3ConfigFromEnv()}
			return runPreprocess(cmd.Context(), cmd.OutOrStdout(), cfg)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.InputPath, "input", "", "Raw data package: a datapackage.json or its folder, https:// or s3://")
	f.StringVar(&cfg.OutputPath, "output", "", "Folder that receives the canonical package as <version>/, e.g. datasets/<ds>/canonical")
	f.StringVar(&cfg.MappingPath, "mapping", "", "Path to the YAML field-mapping config")
	f.StringVar(&cfg.Version, "version", datapackage.DefaultVersion, "Canonical package version, the folder name under --output")
	return cmd
}

var preprocessMappingExtensions = map[FileExt]struct{}{
	ExtYAML: {},
}

func applyPreprocessEnvDefaults(cfg *preprocessConfig) {
	if err := env.LoadDotEnv(os.Getenv("ENV"), "cmd/datapipe/preprocess.env"); err != nil {
		slog.Info("Skipping .env environment variables...", "error", err)
	}
	if cfg.InputPath == "" {
		cfg.InputPath = os.Getenv("INPUT_PATH")
	}
	if cfg.OutputPath == "" {
		cfg.OutputPath = os.Getenv("OUTPUT_PATH")
	}
	if cfg.MappingPath == "" {
		cfg.MappingPath = os.Getenv("MAPPING_CONFIG_PATH")
	}
}

func runPreprocess(ctx context.Context, out io.Writer, cfg preprocessConfig) error {
	start := time.Now()

	mapping, err := loadPreprocessMapping(cfg.MappingPath)
	if err != nil {
		return err
	}
	raw, err := datapackage.OpenWith(ctx, cfg.InputPath, cfg.Packages)
	if err != nil {
		return fmt.Errorf("open raw package: %w", err)
	}

	cli.Header(out, "sengi preprocess",
		cli.Field{Label: "raw", Value: raw.Location},
		cli.IntField("resources", len(raw.Package.Resources)),
		cli.Field{Label: "mapping", Value: cfg.MappingPath},
		cli.Field{Label: "output", Value: cfg.OutputPath},
		cli.Field{Label: "version", Value: cfg.Version},
	)

	result, err := ingest.Preprocess(ctx, ingest.PreprocessOptions{
		Raw:        raw,
		Mapping:    mapping,
		OutputRoot: cfg.OutputPath,
		Version:    cfg.Version,
		ProducedBy: datapackage.ProducedBy{Tool: "datapipe preprocess"},
	})
	var conflict *datapackage.VersionConflictError
	if errors.As(err, &conflict) {
		return fmt.Errorf("canonical version %s already holds other articles; publish this run under a new --version: %w", conflict.Version, err)
	}
	if err != nil {
		return err
	}

	renderSummary(out, result, time.Since(start))
	return nil
}

func loadPreprocessMapping(path string) (*datamapping.DataMapper, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open mapping config: %w", err)
	}
	defer f.Close()
	mapping, err := reader.NewYAMLConfigLoader(f).Load(true)
	if err != nil {
		return nil, fmt.Errorf("load mapping %s: %w", path, err)
	}
	return mapping, nil
}

func renderSummary(w io.Writer, result datapackage.Published, elapsed time.Duration) {
	report := result.Package.Report
	dropped, deduped := total(report.Dropped), total(report.Deduped)
	cli.Summary(w, "Preprocess complete",
		cli.Field{Label: "read", Value: strconv.FormatInt(report.Read, 10)},
		cli.Field{Label: "written", Value: strconv.FormatInt(report.Written, 10)},
		cli.Field{Label: "dropped", Value: strconv.FormatInt(dropped, 10)},
		cli.Field{Label: "deduped", Value: strconv.FormatInt(deduped, 10)},
		cli.Field{Label: "version", Value: result.Package.Version},
		cli.Field{Label: "hash", Value: result.Package.Resources[0].Hash},
		cli.Field{Label: "package", Value: result.Dir},
		cli.Field{Label: "duration", Value: fmt.Sprintf("%.2fs", elapsed.Seconds())},
	)
	if dropped > 0 {
		cli.Warn(w, fmt.Sprintf("%d dropped: %s", dropped, breakdown(report.Dropped)))
	}
	if deduped > 0 {
		cli.Done(w, fmt.Sprintf("%d deduped: %s", deduped, breakdown(report.Deduped)))
	}
	if result.Restored {
		cli.Done(w, "version "+result.Package.Version+" was published without its resource; restored it from this run")
	} else if result.Existing {
		cli.Done(w, "version "+result.Package.Version+" was already published; kept it and discarded this run's copy")
	}
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
