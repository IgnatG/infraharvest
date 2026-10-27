// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"slices"
	"sort"
)

// typeAliases maps resource types to the type another name of theirs is
// matched as: a load balancer managed as aws_alb is the aws_lb listed, and
// an Azure virtual machine managed as azurerm_virtual_machine is the
// azurerm_linux_virtual_machine or azurerm_windows_virtual_machine listed.
// The types of each group have the same IDs, so a resource of one is
// imported as another with the ID it has.
var typeAliases = map[string]string{
	"aws_alb":                                        "aws_lb",
	"aws_alb_listener":                               "aws_lb_listener",
	"aws_alb_listener_certificate":                   "aws_lb_listener_certificate",
	"aws_alb_listener_rule":                          "aws_lb_listener_rule",
	"aws_alb_target_group":                           "aws_lb_target_group",
	"aws_alb_target_group_attachment":                "aws_lb_target_group_attachment",
	"azurerm_linux_virtual_machine":                  "azurerm_virtual_machine",
	"azurerm_windows_virtual_machine":                "azurerm_virtual_machine",
	"azurerm_linux_virtual_machine_scale_set":        "azurerm_virtual_machine_scale_set",
	"azurerm_windows_virtual_machine_scale_set":      "azurerm_virtual_machine_scale_set",
	"azurerm_orchestrated_virtual_machine_scale_set": "azurerm_virtual_machine_scale_set",
	"google_compute_instance_from_template":          "google_compute_instance",
}

// CanonicalType returns the type the types of typ's group are matched as
// (see typeAliases): typ itself if it has no other names.
func CanonicalType(typ string) string {
	if alias, ok := typeAliases[typ]; ok {
		return alias
	}
	return typ
}

// AliasTypes returns the other types of typ's group (see typeAliases),
// sorted; none if typ has no other names.
func AliasTypes(typ string) []string {
	canonical := CanonicalType(typ)
	var others []string
	if canonical != typ {
		others = append(others, canonical)
	}
	for t, alias := range typeAliases {
		if alias == canonical && t != typ {
			others = append(others, t)
		}
	}
	sort.Strings(others)
	return slices.Compact(others)
}

// sameType reports whether a and b are one type or of one group (see
// typeAliases).
func sameType(a, b string) bool {
	return CanonicalType(a) == CanonicalType(b)
}
