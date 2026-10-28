// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"errors"

	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// listerSettings are the settings Provider.Lister passes each lister (see
// harvest.ListerOf).
type listerSettings struct {
	// region is the region the import lists, its zero value for global.
	region compute.Region
	// project is the project's ID.
	project string
	// catalogue is the import's catalogue, nil for the built-in one.
	catalogue *catalogue.Catalogue
}

// projectID returns the project, as Provider.Lister sets it. A lister
// given none fails rather than listing project "".
func (s listerSettings) projectID() (string, error) {
	if s.project == "" {
		return "", errors.New("the lister's settings name no project")
	}
	return s.project, nil
}
