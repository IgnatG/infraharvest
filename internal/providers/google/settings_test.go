// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"testing"

	"google.golang.org/api/compute/v1"
)

// A lister without a project fails, rather than listing project "".
func TestProjectID(t *testing.T) {
	t.Parallel()
	if project, err := (listerSettings{project: "acme"}).projectID(); err != nil || project != "acme" {
		t.Errorf("got %q, %v", project, err)
	}
	if _, err := (listerSettings{}).projectID(); err == nil {
		t.Error("no project: want an error")
	}
}

// listerSettingsOf returns the settings p's Lister passed the lister of
// service.
func listerSettingsOf(t *testing.T, p *Provider, service string) listerSettings {
	t.Helper()
	lister, err := p.Lister(service)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := lister.(interface{ Settings() listerSettings })
	if !ok {
		t.Fatalf("%T takes no settings", lister)
	}
	return l.Settings()
}

// What Lister passes is what the lister reads.
func TestListerPassesTheSettings(t *testing.T) {
	t.Parallel()
	p := &Provider{projectName: "acme", region: compute.Region{Name: "europe-west2"}}
	settings := listerSettingsOf(t, p, cloudAssetService)
	if project, err := settings.projectID(); err != nil || project != "acme" {
		t.Errorf("project %q, %v", project, err)
	}
	if settings.region.Name != "europe-west2" {
		t.Errorf("region %+v", settings.region)
	}
}
