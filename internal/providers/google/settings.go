// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"errors"

	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// listerSettings are the settings Provider.Select passes each lister (see
// harvest.ListerOf).
type listerSettings struct {
	// region is the region the import lists, its zero value for global.
	region compute.Region
	// project is the project's ID.
	project string
	// catalogue is the import's catalogue, nil for the built-in one.
	catalogue *catalogue.Catalogue
	// unwanted reports the types the import leaves out whatever is listed
	// (see Provider.SetUnwantedTypes); nil wants every type.
	unwanted func(typ string) bool
}

// wants reports whether the import may keep resources of typ: a lister
// skips the calls that list only resources of a type it doesn't want.
func (s listerSettings) wants(typ string) bool {
	return s.unwanted == nil || !s.unwanted(typ)
}

// projectID returns the project, as Provider.Select sets it. A lister
// given none fails rather than listing project "".
func (s listerSettings) projectID() (string, error) {
	if s.project == "" {
		return "", errors.New("the lister's settings name no project")
	}
	return s.project, nil
}
