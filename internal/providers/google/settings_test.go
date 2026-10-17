// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"testing"

	"google.golang.org/api/compute/v1"
)

// A lister without a project fails, rather than listing project "" or
// panicking.
func TestProjectSetting(t *testing.T) {
	if project, err := projectSetting(map[string]interface{}{"project": "acme"}); err != nil || project != "acme" {
		t.Errorf("got %q, %v", project, err)
	}
	for _, settings := range []map[string]interface{}{nil, {"project": ""}, {"project": 1}} {
		if _, err := projectSetting(settings); err == nil {
			t.Errorf("%v: want an error", settings)
		}
	}
}

// A lister gets the region Select passes; without one, or with one of
// another type, it lists what is global rather than panicking.
func TestRegionSetting(t *testing.T) {
	if region := regionSetting(map[string]interface{}{"region": compute.Region{Name: "us-central1"}}); region.Name != "us-central1" {
		t.Errorf("got %+v", region)
	}
	for _, settings := range []map[string]interface{}{nil, {"region": "us-central1"}, {"region": nil}} {
		if region := regionSetting(settings); region.Name != "" {
			t.Errorf("%v: got %+v, want the zero region", settings, region)
		}
	}
}

// What Select passes is what the accessors read.
func TestSelectPassesTheSettings(t *testing.T) {
	p := &Provider{projectName: "acme", region: compute.Region{Name: "europe-west2"}}
	if err := p.Select(cloudAssetService, false); err != nil {
		t.Fatal(err)
	}
	settings := p.Selected().Settings()
	if project, err := projectSetting(settings); err != nil || project != "acme" {
		t.Errorf("project %q, %v", project, err)
	}
	if region := regionSetting(settings); region.Name != "europe-west2" {
		t.Errorf("region %+v", region)
	}
}
