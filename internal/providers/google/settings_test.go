// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import "testing"

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
