// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import "errors"

// projectSetting returns the project in a lister's settings, as
// Provider.Select sets them. A missing one, such as after the key is
// renamed, fails the lister rather than listing project "" or panicking.
func projectSetting(settings map[string]interface{}) (string, error) {
	project, _ := settings["project"].(string)
	if project == "" {
		return "", errors.New("the lister's settings name no project")
	}
	return project, nil
}
