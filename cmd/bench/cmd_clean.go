package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/bench/report"
	"github.com/tadjov/sengi/internal/bench/trackctx"
	"github.com/tadjov/sengi/internal/cli"
)

type cleanFlags struct {
	trackArg string
	keep     int
	dryRun   bool
}

func newCleanCmd() *cobra.Command {
	var f cleanFlags
	cmd := &cobra.Command{
		Use:   "clean [track]",
		Short: "Remove old report artifacts, keeping the N most recent",
		Long: `Deletes old JSON and HTML report files from <track>/reports/,
keeping the --keep most-recent runs.  latest.json is never deleted.

For JSON reports, the one latest.json points at is always kept, whatever
--keep says. The remaining slots go to the newest complete runs first; failed,
partial and latency-only reports are removed before any complete one. HTML and
Markdown exports keep only the --keep newest, even the latest run's export.
A deleted report takes its exported run files, reports/<run_id>.run/, with it.
Files are dated by the run timestamp in their name (lexicographic =
chronological).`,
		Example: `  bench clean tracks/cc-news/fts            # keep 5 most recent (default)
  bench clean tracks/cc-news/fts --keep 3
  bench clean tracks/cc-news/fts --dry-run  # show what would be deleted`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeClean(cmd, f, args)
		},
	}
	cmd.Flags().StringVar(&f.trackArg, "track", "", "Track path (relative to --track-root)")
	cmd.Flags().IntVar(&f.keep, "keep", 5, "Number of most-recent reports to keep")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "Print what would be deleted without deleting")
	return cmd
}

func executeClean(cmd *cobra.Command, f cleanFlags, args []string) error {
	if f.keep < 0 {
		return fmt.Errorf("--keep must be 0 or more, got %d", f.keep)
	}
	tr, err := trackctx.Resolve(trackInputs(f.trackArg, args))
	if err != nil {
		return err
	}

	reportsDir := tr.ReportsDir()
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		return fmt.Errorf("read reports dir %s: %w", reportsDir, err)
	}

	var jsons, htmls []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "latest.json" {
			continue
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".json":
			jsons = append(jsons, filepath.Join(reportsDir, name))
		case ".html", ".md":
			htmls = append(htmls, filepath.Join(reportsDir, name))
		}
	}

	// Run IDs are timestamped, so reverse lexicographic order is newest first.
	slices.SortFunc(jsons, func(a, b string) int { return strings.Compare(b, a) })
	slices.SortFunc(htmls, func(a, b string) int { return strings.Compare(b, a) })

	// Without a readable pointer no report is pinned.
	latest, _ := report.LatestTarget(tr.LatestReportPath())
	toDelete := withRunFiles(staleReports(jsons, reportRetention{keep: f.keep, latest: latest}))
	toDelete = append(toDelete, htmls[min(f.keep, len(htmls)):]...)

	if len(toDelete) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "%s Nothing to clean in %s\n",
			cli.Muted.Sprint("✓"), reportsDir)
		return nil
	}

	w := cmd.OutOrStdout()
	for _, path := range toDelete {
		if f.dryRun {
			fmt.Fprintf(w, "%s %s\n", cli.Muted.Sprint("would delete"), filepath.Base(path))
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			fmt.Fprintf(w, "%s %s: %v\n", cli.Failure.Sprint("ERR"), filepath.Base(path), err)
			continue
		}
		fmt.Fprintf(w, "%s %s\n", cli.Success.Sprint("deleted"), filepath.Base(path))
	}

	if f.dryRun {
		fmt.Fprintf(w, "\n%s Pass without --dry-run to delete.\n", cli.Muted.Sprint("Dry run:"))
	} else {
		cli.Done(w, fmt.Sprintf("Cleaned %d file(s) from %s", len(toDelete), reportsDir))
	}
	return nil
}

// withRunFiles adds each stale report's exported run files, reports/<run_id>.run/:
// they reproduce that report's metrics and nothing else.
func withRunFiles(staleReports []string) []string {
	paths := slices.Clone(staleReports)
	for _, reportPath := range staleReports {
		runDir := strings.TrimSuffix(reportPath, ".json") + ".run"
		if info, err := os.Stat(runDir); err == nil && info.IsDir() {
			paths = append(paths, runDir)
		}
	}
	return paths
}

// reportRetention is how many reports bench clean keeps and which one it must.
type reportRetention struct {
	keep   int
	latest string
}

// staleReports picks the reports to delete from newestFirst, keeping
// retention.keep of them: the latest one, then complete runs, then incomplete
// ones, newest first within each.
func staleReports(newestFirst []string, retention reportRetention) []string {
	keep, latest := retention.keep, retention.latest
	kept := make(map[string]bool)
	if slices.Contains(newestFirst, latest) {
		kept[latest] = true
	}
	var complete, incomplete []string
	for _, path := range newestFirst {
		if path == latest {
			continue
		}
		if isCompleteReport(path) {
			complete = append(complete, path)
		} else {
			incomplete = append(incomplete, path)
		}
	}
	for _, path := range append(complete, incomplete...) {
		if len(kept) >= keep {
			break
		}
		kept[path] = true
	}
	var stale []string
	for _, path := range newestFirst {
		if !kept[path] {
			stale = append(stale, path)
		}
	}
	return stale
}

// isCompleteReport reports whether path is a readable report with no errored
// execution that bench run recorded nothing incomplete about.
func isCompleteReport(path string) bool {
	rpt, err := report.ReadJSON(path)
	if err != nil || len(rpt.Settings.Incomplete) > 0 {
		return false
	}
	for _, job := range rpt.Jobs {
		for _, entry := range job.Aggregated {
			if entry.ErrorCount > 0 {
				return false
			}
		}
	}
	return true
}
