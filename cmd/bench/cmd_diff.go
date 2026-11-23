package main

import (
	"fmt"

	"github.com/spf13/cobra"
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
