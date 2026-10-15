// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"log"
	"os"

	"golang.org/x/term"

	"github.com/IgnatG/infraharvest/internal/picker"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// errNoTerminal is returned when the picker can't run.
var errNoTerminal = errors.New("the picker needs a terminal: edit the selection file instead")

// pick opens the picker on the selection file at path.
func Pick(path string) error {
	if !Interactive() {
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
func Interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
