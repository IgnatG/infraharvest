// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"errors"

	"google.golang.org/api/compute/v1"
)

// The keys of the settings Provider.Select passes each lister, read only
// through the accessors below, so that the two can't drift apart.
const (
	// regionKey is the compute.Region the import lists, its zero value
	// for global.
	regionKey = "region"
	// projectKey is the project's ID.
	projectKey = "project"
	// catalogueKey is the import's *catalogue.Catalogue, nil for the
	// built-in one.
	catalogueKey = "catalogue"
)

// projectSetting returns the project in a lister's settings, as
// Provider.Select sets them. A missing one, such as after the key is
// renamed, fails the lister rather than listing project "" or panicking.
func projectSetting(settings map[string]interface{}) (string, error) {
	project, _ := settings[projectKey].(string)
	if project == "" {
		return "", errors.New("the lister's settings name no project")
	}
	return project, nil
}

// regionSetting returns the region in a lister's settings, as
// Provider.Select sets them: the zero compute.Region, which lists what is
// global, when there is none or it is of another type.
func regionSetting(settings map[string]interface{}) compute.Region {
	region, _ := settings[regionKey].(compute.Region)
	return region
}
