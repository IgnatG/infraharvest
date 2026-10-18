// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"errors"

	"google.golang.org/api/compute/v1"

	"github.com/infraharvest/infraharvest-cli/internal/catalogue"
)

// The keys of the settings Provider.Select passes each lister, written
// only by listerSettings.args and read only by settingsOf, so that the
// two can't drift apart.
const (
	regionKey    = "region"
	projectKey   = "project"
	catalogueKey = "catalogue"
)

// listerSettings are the settings Provider.Select passes each lister,
// typed: the lister gets them as a map (see harvest.ListerConfig), which
// args makes and settingsOf reads back.
type listerSettings struct {
	// region is the region the import lists, its zero value for global.
	region compute.Region
	// project is the project's ID.
	project string
	// catalogue is the import's catalogue, nil for the built-in one.
	catalogue *catalogue.Catalogue
}

// args returns the settings as a lister's settings map.
func (s listerSettings) args() map[string]interface{} {
	return map[string]interface{}{
		regionKey:    s.region,
		projectKey:   s.project,
		catalogueKey: s.catalogue,
	}
}

// settingsOf returns the settings in a lister's settings map, as args
// makes it. A setting that is missing, or of another type, comes back as
// its zero value.
func settingsOf(settings map[string]interface{}) listerSettings {
	var s listerSettings
	s.region, _ = settings[regionKey].(compute.Region)
	s.project, _ = settings[projectKey].(string)
	s.catalogue, _ = settings[catalogueKey].(*catalogue.Catalogue)
	return s
}

// projectSetting returns the project in a lister's settings, as
// Provider.Select sets them. A missing one, such as after the key is
// renamed, fails the lister rather than listing project "" or panicking.
func projectSetting(settings map[string]interface{}) (string, error) {
	project := settingsOf(settings).project
	if project == "" {
		return "", errors.New("the lister's settings name no project")
	}
	return project, nil
}

// regionSetting returns the region in a lister's settings, as
// Provider.Select sets them: the zero compute.Region, which lists what is
// global, when there is none or it is of another type.
func regionSetting(settings map[string]interface{}) compute.Region {
	return settingsOf(settings).region
}
