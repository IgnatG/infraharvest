// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

func WriteBootstrap(configFile, out string, w io.Writer, profiles []harvest.Profile) error {
	f, err := config.Load(configFile, profiles)
	if err != nil {
		return err
	}
	if f.Backend == nil {
		return errors.New(configFile + " has no backend to bootstrap")
	}
	files, err := f.Backend.Bootstrap()
	if err != nil {
		return err
	}
	// As an import checks it (see lockOutput): out may be a clone, which
	// could commit bootstrap, or a file in it, as a link elsewhere.
	if err := fsutil.CheckContained(out, CheckpointDir, ".terraform", ".git"); err != nil {
		return fmt.Errorf("%w: refusing to write under %s; remove it", err, out)
	}
	dir := filepath.Join(out, config.BootstrapDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Its state stays local at first: keep it out of version control.
	if err := writeGitignore(out); err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := fsutil.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "Wrote %s: apply it once (terraform init, terraform apply), then plan the generated roots.\n", dir)
	return err
}
