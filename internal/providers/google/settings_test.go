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

// selectedSettings returns the settings p's Select passed its current
// lister.
func selectedSettings(t *testing.T, p *Provider) listerSettings {
	t.Helper()
	l, ok := p.Selected().(interface{ Settings() listerSettings })
	if !ok {
		t.Fatalf("%T takes no settings", p.Selected())
	}
	return l.Settings()
}

// What Select passes is what the lister reads.
func TestSelectPassesTheSettings(t *testing.T) {
	t.Parallel()
	p := &Provider{projectName: "acme", region: compute.Region{Name: "europe-west2"}}
	if err := p.Select(cloudAssetService, false); err != nil {
		t.Fatal(err)
	}
	settings := selectedSettings(t, p)
	if project, err := settings.projectID(); err != nil || project != "acme" {
		t.Errorf("project %q, %v", project, err)
	}
	if settings.region.Name != "europe-west2" {
		t.Errorf("region %+v", settings.region)
	}
}
