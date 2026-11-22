package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/bench/report"
	"github.com/tadjov/sengi/internal/bench/trackctx"
	"github.com/tadjov/sengi/internal/cli"
)

func newStatusCmd() *cobra.Command {
	var trackArg string
	cmd := &cobra.Command{
		Use:   "status [track]",
		Short: "Show what has been generated for a track",
		Long: `Prints a one-glance summary of a track's pipeline state: which artifacts
exist, when they were last generated, and how much they hold.`,
		Example: `  bench status tracks/cc-news/fts
  bench status          # walk-up from CWD`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeStatus(cmd.Context(), cmd.OutOrStdout(), trackArg, args)
		},
	}
	cmd.Flags().StringVar(&trackArg, "track", "", "Track path (relative to --track-root)")
	return cmd
}

func executeStatus(ctx context.Context, w io.Writer, track string, args []string) error {
	return forEachTrack(ctx, w, trackInputs(track, args), func(tr *trackctx.Track) error {
		return statusTrack(w, tr)
	})
}

func statusTrack(w io.Writer, tr *trackctx.Track) error {
	fmt.Fprintf(w, "%s %s\n", cli.Strong.Sprint("Track:"), cli.Strong.Sprint(tr.Name()))
	fmt.Fprintf(w, "  %s %s\n\n", cli.Muted.Sprint("root:"), tr.Root)

	summary, err := poolStatus(tr)
	printArtifact(w, "Pool", summary, err)

	annotations, err := filepath.Glob(filepath.Join(filepath.Dir(tr.Pool), "annotations.*.yaml"))
	if err != nil {
		return err
	}
	if len(annotations) == 0 {
		printArtifact(w, "Judgments", "", fmt.Errorf("none — run: bench judge %s", tr.Name()))
	}
	for i, path := range annotations {
		label := "Judgments"
		if i > 0 {
			label = ""
		}
		summary, err := judgmentsStatus(path)
		printArtifact(w, label, summary, err)
	}

	summary, err = latestReportStatus(tr)
	printArtifact(w, "Reports", summary, err)
	return nil
}

// printArtifact marks the artifact present with its summary, or missing with
// the reason it could not be read.
func printArtifact(w io.Writer, label, summary string, err error) {
	mark := cli.Success.Sprint("✓")
	if err != nil {
		mark, summary = cli.Failure.Sprint("✗"), cli.Muted.Sprint(err.Error())
	}
	fmt.Fprintf(w, "  %-10s %s  %s\n", label, mark, summary)
}

func poolStatus(tr *trackctx.Track) (string, error) {
	pf, err := pool.ReadPoolFile(tr.Pool)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("none — run: bench pool %s", tr.Name())
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s  %s  queries=%d", filepath.Base(tr.Pool), modifiedDay(tr.Pool), len(pf.Queries)), nil
}

func judgmentsStatus(path string) (string, error) {
	jf, err := judgment.ReadFile(path)
	if err != nil {
		return "", err
	}
	graded, unjudged := jf.GradeCounts()
	return fmt.Sprintf("%s  %s  graded=%d unjudged=%d", filepath.Base(path), modifiedDay(path), graded, unjudged), nil
}

func latestReportStatus(tr *trackctx.Track) (string, error) {
	rpt, err := report.ReadLatestReport(tr.LatestReportPath())
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("none — run: bench run %s", tr.Name())
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("latest → %s  (%s)", rpt.Provenance.RunID, rpt.Provenance.GeneratedAt.Format("2006-01-02 15:04")), nil
}

func modifiedDay(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return info.ModTime().Format("2006-01-02")
}
