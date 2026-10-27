// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package providers

import (
	"time"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/importer"
)

// EngineFlags are the flags of the commands that run Terraform or
// OpenTofu on an output directory's roots: import and discover, verify
// and drift. Each field but the help points at where its flag's value
// goes in the command's options, so that the flags have the same names,
// defaults and help in each command; the help fields say what differs.
type EngineFlags struct {
	Engine, TerraformPath *string
	EngineTimeout         *time.Duration
	Format, Catalogue     *string
	ForceUnlock           *bool
	// Environments is nil for a command without --environment.
	Environments *[]string

	// FormatHelp is --format's help, which says what the command prints.
	FormatHelp string
	// CatalogueHelp follows what --catalogue's directory is, saying what
	// the command takes it for.
	CatalogueHelp string
	// EnvironmentHelp is --environment's help.
	EnvironmentHelp string
}

// AddEngineFlags adds f's flags to flags.
func AddEngineFlags(flags *pflag.FlagSet, f EngineFlags) {
	flags.StringVar(f.Engine, "engine", importer.EngineTerraform, "terraform or tofu: run Terraform or OpenTofu on the roots")
	flags.StringVar(f.TerraformPath, "terraform-path", "", "Terraform or OpenTofu binary (default: on PATH; Terraform >= 1.5 or else the latest release, downloaded and verified; OpenTofu >= 1.6)")
	flags.DurationVar(f.EngineTimeout, "engine-timeout", importer.DefaultEngineTimeout, "longest time one Terraform or OpenTofu command, such as the plan of one root, may run; a root whose command takes longer fails (0: no limit)")
	flags.StringVar(f.Format, "format", importer.FormatText, f.FormatHelp)
	flags.StringVar(f.Catalogue, "catalogue", "", "directory of catalogue files (YAML) that add to or override what the built-in catalogue says about resource types"+f.CatalogueHelp+" (see "+DocsURL+"catalogue/)")
	flags.BoolVar(f.ForceUnlock, "force-unlock", false, "run even when another run holds the output directory's lock, as when that run is stuck, or when the file system can't lock; a run that is killed releases the lock by itself")
	if f.Environments != nil {
		flags.StringSliceVar(f.Environments, "environment", nil, f.EnvironmentHelp)
	}
}
