// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"errors"
	"log"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/IgnatG/infraharvest/picker"
	"github.com/IgnatG/infraharvest/selection"
)

// newPickCmd opens the picker on a selection file.
func newPickCmd() *cobra.Command {
	path := DefaultSelectionFile
	cmd := &cobra.Command{
		Use:   "pick",
		Short: "Choose in the terminal what a selection file imports",
		Long: "Open a selection file from infraharvest discover in the terminal: a tree of its\n" +
			"resources by account, region and type, to include or exclude one at a time or a\n" +
			"group at once, with a summary of what an import of it would bring under\n" +
			"Terraform. s saves the file; q leaves it as it was.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(*cobra.Command, []string) error {
			return pick(path)
		},
	}
	cmd.Flags().StringVar(&path, "selection", DefaultSelectionFile, "selection file to edit, from infraharvest discover")
	return cmd
}

// errNoTerminal is returned when the picker can't run.
var errNoTerminal = errors.New("the picker needs a terminal: edit the selection file instead")

// pick opens the picker on the selection file at path.
func pick(path string) error {
	if !interactive() {
		return errNoTerminal
	}
	f, err := selection.Load(path)
	if err != nil {
		return err
	}
	saved, err := picker.Run(path, f)
	if err != nil {
		return err
	}
	if saved {
		log.Printf("saved %s", path)
	} else {
		log.Printf("left %s as it was", path)
	}
	return nil
}

// interactive reports whether infraharvest runs in a terminal someone can
// use: standard input and output both are one.
func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
