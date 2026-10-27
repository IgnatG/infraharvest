// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// OutsideScopePrefix starts the reason of an exclusion for a resource the
// import's scope leaves out, such as the resources of no CloudFormation
// stack --cfn-stack names: the user's choice, like the selection file's
// (see OwnerSelection).
const OutsideScopePrefix = "outside the import's scope: "

// results is what the import did with each resource it listed, by where it
// was listed, its type and import ID, for the stacks the import is scoped
// to to work out what became of theirs (see cfn.Results).
type results struct {
	imported map[string]placed
	leftOut  map[string]string
	excluded map[string]Excluded
	checks   map[string][]string // failed checks or the error, by path
	failed   bool
}

// placed is where a resource was imported: its root and its address there.
type placed struct{ path, address string }

// resultKey keys a listed resource in results.
func resultKey(scope, typ, id string) string { return scope + "\n" + typ + " " + id }

// results gathers what the report's directories, exclusions and failures
// say of each resource the import listed.
func (r *Report) results() results {
	res := results{imported: map[string]placed{}, leftOut: map[string]string{}, excluded: map[string]Excluded{}, checks: map[string][]string{}, failed: len(r.Failures) > 0}
	for _, d := range r.Directories {
		for _, imported := range d.Imported {
			res.imported[resultKey(d.Scope, engine.AddressType(imported.Address), imported.ID)] = placed{d.Path, imported.Address}
		}
		for _, left := range d.LeftOut {
			res.leftOut[resultKey(d.Scope, engine.AddressType(left.Address), left.ID)] = strings.Join(left.Errors, "; ")
		}
		if d.Error != "" {
			res.checks[d.Path] = append(res.checks[d.Path], "failed: "+d.Error)
		}
		for _, c := range d.Checks {
			if !c.Passed {
				res.checks[d.Path] = append(res.checks[d.Path], c.Name+" failed")
			}
		}
	}
	for _, e := range r.Excluded {
		res.excluded[resultKey(e.Scope, e.Type, e.ID)] = e
	}
	return res
}

func (res results) Imported(scope, typ, id string) (string, string, bool) {
	at, ok := res.imported[resultKey(scope, typ, id)]
	return at.path, at.address, ok
}

func (res results) LeftOut(scope, typ, id string) (string, bool) {
	why, ok := res.leftOut[resultKey(scope, typ, id)]
	return why, ok
}

// Excluded counts as people's decision what the selection file leaves
// out, and what Terraform manages already, in a state or a root.
func (res results) Excluded(scope, typ, id string) (string, bool, bool) {
	e, ok := res.excluded[resultKey(scope, typ, id)]
	if !ok {
		return "", false, false
	}
	switch e.ManagedBy() {
	case OwnerSelection, OwnerTerraformState, OwnerTerraformRoot:
		return e.Reason, true, true
	}
	return e.Reason, false, true
}

func (res results) FailedChecks(path string) []string { return res.checks[path] }

func (res results) Failed() bool { return res.failed }
