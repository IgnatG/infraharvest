// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"errors"

	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// The keys of the settings Provider.Select passes each lister, written
// only by listerSettings.args and read only by settingsOf, so that the
// two can't drift apart.
const (
	regionKey    = "region"
	projectKey   = "project"
	catalogueKey = "catalogue"
	unwantedKey  = "unwanted"
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
	// unwanted reports the types the import leaves out whatever is listed
	// (see Provider.SetUnwantedTypes); nil wants every type.
	unwanted func(typ string) bool
}

// args returns the settings as a lister's settings map. Without unwanted
// types the map has no entry for them, so that the maps of equal settings
// are deeply equal, which no func is.
func (s listerSettings) args() map[string]interface{} {
	args := map[string]interface{}{
		regionKey:    s.region,
		projectKey:   s.project,
		catalogueKey: s.catalogue,
	}
	if s.unwanted != nil {
		args[unwantedKey] = s.unwanted
	}
	return args
}

// settingsOf returns the settings in a lister's settings map, as args
// makes it. A setting that is missing, or of another type, comes back as
// its zero value.
func settingsOf(settings map[string]interface{}) listerSettings {
	var s listerSettings
	s.region, _ = settings[regionKey].(compute.Region)
	s.project, _ = settings[projectKey].(string)
	s.catalogue, _ = settings[catalogueKey].(*catalogue.Catalogue)
	s.unwanted, _ = settings[unwantedKey].(func(typ string) bool)
	return s
}

// wants reports whether the import may keep resources of typ, by a
// lister's settings: a lister skips the calls that list only resources of
// a type it doesn't want.
func wants(settings map[string]interface{}, typ string) bool {
	unwanted := settingsOf(settings).unwanted
	return unwanted == nil || !unwanted(typ)
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
