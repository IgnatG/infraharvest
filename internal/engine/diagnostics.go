// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"path/filepath"
	"regexp"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// byResource assigns each diagnostic to the resource it is about, using, in
// order: the address Terraform reports, the block its range points into in
// generated.tf or imports.tf, and the one resource address its message
// names. It returns the diagnostics it can't assign to exactly one resource
// separately: they concern the whole directory, such as the provider
// configuration.
func byResource(diags []tfjson.Diagnostic, generated, imports *hclfile.File) (map[string][]tfjson.Diagnostic, []tfjson.Diagnostic) {
	addresses := imports.ImportAddresses()
	assigned := map[string][]tfjson.Diagnostic{}
	var unassigned []tfjson.Diagnostic
	for _, d := range diags {
		addr := ""
		switch {
		case d.Address != "":
			addr = trimInstanceKey(d.Address)
		case d.Range != nil && filepath.Base(d.Range.Filename) == GeneratedFileName:
			addr = generated.ResourceAt(d.Range.Start.Line)
		case d.Range != nil && filepath.Base(d.Range.Filename) == ImportsFileName:
			addr = imports.ImportAt(d.Range.Start.Line)
		default:
			addr = onlyMentioned(d.Summary+" "+d.Detail, addresses)
		}
		if addr == "" || !addresses[addr] {
			unassigned = append(unassigned, d)
			continue
		}
		assigned[addr] = append(assigned[addr], d)
	}
	return assigned, unassigned
}

// trimInstanceKey turns aws_vpc.a["x"] or aws_vpc.a[0] into aws_vpc.a.
func trimInstanceKey(addr string) string {
	if i := strings.IndexByte(addr, '['); i >= 0 {
		return addr[:i]
	}
	return addr
}

var addressLike = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_-]*\.[A-Za-z_][A-Za-z0-9_-]*`)

// onlyMentioned returns the single address in addresses that text names, or
// "" if it names none or several.
func onlyMentioned(text string, addresses map[string]bool) string {
	found := ""
	for _, m := range addressLike.FindAllString(text, -1) {
		if !addresses[m] || m == found {
			continue
		}
		if found != "" {
			return ""
		}
		found = m
	}
	return found
}
