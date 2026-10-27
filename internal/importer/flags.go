// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"fmt"
	"log"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// Import engines selectable with --engine.
const (
	EngineTerraform = "terraform"
	EngineTofu      = "tofu"
)

// Values of --format: text prints a summary, json the import report (see
// package report) on stdout too.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// engineBinary is the binary --engine runs.
func engineBinary(name string) engine.Binary {
	if name == EngineTofu {
		return engine.TofuBinary
	}
	return engine.TerraformBinary
}

// checkImportOptions rejects values of --format and --modules the import
// would otherwise silently ignore.
func checkImportOptions(options Options) error {
	if options.Format != "" && options.Format != FormatText && options.Format != FormatJSON {
		return fmt.Errorf("--format must be %s or %s, not %q", FormatText, FormatJSON, options.Format)
	}
	switch options.Modules {
	case "", ModulesRegistry, ModulesLatestUntested, ModulesLocal, ModulesNone:
	default:
		return fmt.Errorf("--modules must be %s, %s, %s or %s, not %q", ModulesRegistry, ModulesLatestUntested, ModulesLocal, ModulesNone, options.Modules)
	}
	return checkFilters(options)
}

// checkFilters rejects an --id, --tag or --where that doesn't parse. --id
// applies to every service; a filter on a tag or another attribute only
// takes effect where the service's lister passes it to the API it lists
// with (such as AWS EC2 instance tags), so it says so.
func checkFilters(options Options) error {
	filters, err := options.filters()
	if err != nil {
		return err
	}
	for _, f := range filters {
		if f.Path != "id" {
			log.Printf("a filter on %s only applies to services whose lister supports it (see the provider's page); others list everything", f.Path)
		}
	}
	return nil
}
