// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package run

import (
	"maps"
	"slices"
	"sort"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// Managed is a resource a root imports: its type and import ID, and its
// address in the root, such as module.logs.aws_s3_bucket.this[0].
type Managed struct {
	Type, ID string
	Address  string
	// Unverified, if set, says Address was inferred from what an older
	// checkpoint recorded rather than read from the root's import blocks:
	// for a resource moved into a module call, it is then the call alone,
	// such as module.logs.
	Unverified bool `json:",omitempty"`
}

// SortManaged sorts managed by type, then import ID.
func SortManaged(managed []Managed) {
	sort.Slice(managed, func(i, j int) bool {
		if managed[i].Type != managed[j].Type {
			return managed[i].Type < managed[j].Type
		}
		return managed[i].ID < managed[j].ID
	})
}

// Result is what Generate leaves for the user to finish.
type Result struct {
	// Imported are the resources the configuration imports, with the
	// labels Generate gave them.
	Imported []rootfile.Import
	// Secrets are the variables to set before planning.
	Secrets []secret.Variable
	// Rejected are the resources left out of the configuration.
	Rejected []Rejection
	// Owned are the resources left out because their tags say another
	// tool manages them (see OwnedBy).
	Owned []OwnedResource `json:",omitempty"`
	// Gone are the resources left out because their import found no
	// object where Options.MayBeGone says the listing can lag behind the
	// cloud: they were deleted since they were listed.
	Gone []Rejection `json:",omitempty"`
	// Gate is the result of the verification gate (see runGate).
	Gate gate.Gate
	// Warnings are about what the configuration holds that people should
	// look at, such as a tag named after an AWS access key ID.
	Warnings []string `json:",omitempty"`
	// Modules are the module calls the configuration makes, and the
	// clusters of resources Generate didn't move into one, with why.
	Modules []ModuleCall
	// Files are the files the configuration is in besides generated.tf,
	// with Options.SplitFiles.
	Files []string `json:",omitempty"`
	// Addresses are where the root's import blocks import each resource,
	// as Generate finally wrote them, module calls included, so that a
	// later run can tell which resources it would give other addresses.
	Addresses []Managed `json:",omitempty"`
	// CloudIDs are the canonical cloud IDs of the resources the gate's plan
	// imports, by address (see gate.CloudIDs): an AWS ARN or an Azure
	// Resource Manager ID, where the plan knows one. A consumer falls back
	// to the import ID for a resource without one.
	CloudIDs map[string]string `json:",omitempty"`
	// Moves are the moved blocks of moves.tf, for a root generated again
	// whose resources have other addresses than before (see
	// Options.Previous).
	Moves []rootfile.Move `json:",omitempty"`
	// Removed are the removed blocks of moves.tf, for a root generated
	// again that hands resources over to other roots (see
	// Previous.Removed), and Arrived the resources it imports that other
	// roots hand over (see Options.Arrivals).
	Removed []rootfile.Removal `json:",omitempty"`
	Arrived []rootfile.Arrival `json:",omitempty"`
	// Retyped are the resources of such a root that are of another type now,
	// another name of the one they were, which its removed blocks forget
	// at the addresses they had and its import blocks import again (see
	// Retype).
	Retyped []rootfile.Retype `json:",omitempty"`
	// Unmoved are the resources of such a root at other addresses that no
	// moved block can follow, with why; a checkpoint doesn't keep them.
	Unmoved []string `json:"-"`
	// NotInCloud are the resources of the root an incremental import added
	// to that its listing found no longer in the cloud, for the README; a
	// checkpoint doesn't keep them, as the next run looks again.
	NotInCloud []rootfile.Import `json:"-"`
	// UsedSnapshot says the root's later plans ran against a state snapshot
	// (see Options.ImportOnce), which a caller may generate it again
	// without (see NeedsFreshGeneration).
	UsedSnapshot bool `json:"-"`
	// Plans count the plans Generate ran for the root, by step (see the
	// step constants), and the plan files it showed: each plan imports
	// every resource of the root again.
	Plans []tfrun.StepPlans `json:",omitempty"`
}

// With returns what a root holds once an incremental import added added
// to it (see Add): r's resources and added's, in the files of both, with
// added's checks. Its addresses are both's, unless r, as from an older
// release, recorded none for what it imports: InferredAddresses and the
// root's import blocks then tell all of them, not only added's. Its moves
// are r's: Add leaves moves.tf as it is.
func (r *Result) With(added *Result) *Result {
	if r == nil {
		return added
	}
	var addresses []Managed
	if len(r.Addresses) > 0 || len(r.Imported) == 0 {
		addresses = append(slices.Clone(r.Addresses), added.Addresses...)
		SortManaged(addresses)
	}
	files := slices.Clone(r.Files)
	for _, name := range added.Files {
		if !slices.Contains(files, name) {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	return &Result{
		Imported:  append(slices.Clone(r.Imported), added.Imported...),
		Secrets:   append(slices.Clone(r.Secrets), added.Secrets...),
		Rejected:  append(slices.Clone(r.Rejected), added.Rejected...),
		Owned:     append(slices.Clone(r.Owned), added.Owned...),
		Gone:      append(slices.Clone(r.Gone), added.Gone...),
		Modules:   append(slices.Clone(r.Modules), added.Modules...),
		Warnings:  append(slices.Clone(r.Warnings), added.Warnings...),
		Gate:      added.Gate,
		CloudIDs:  withCloudIDs(r.CloudIDs, added.CloudIDs),
		Files:     files,
		Addresses: addresses,
		Moves:     r.Moves,
		Removed:   r.Removed,
		Arrived:   r.Arrived,
	}
}

// withCloudIDs returns the cloud IDs of a root r's resources and those
// added to it: added's, from the gate run on the whole root, win.
func withCloudIDs(r, added map[string]string) map[string]string {
	if len(r) == 0 && len(added) == 0 {
		return nil
	}
	ids := maps.Clone(r)
	if ids == nil {
		ids = map[string]string{}
	}
	maps.Copy(ids, added)
	return ids
}

// OwnedResource is a resource Generate left out because its tags say
// another tool manages it (see OwnedBy).
type OwnedResource struct {
	Address string
	ID      string // the import ID
	Owner   Owner
}

// Rejection is a resource Generate left out because Terraform couldn't
// import it or generate valid configuration for it. Its import block and
// any generated configuration are in rejected.hcl, under its errors.
type Rejection struct {
	Address string
	ID      string // the import ID
	Errors  []string
}

// Blocks returns what r, of a root, imports, by type and import ID, with
// the address of the block of the root each is in: a resource's, or a
// module call's (see RemovedFrom). A result without addresses, as in a
// checkpoint an older release wrote, has only the labels Generate gave
// them.
func (r *Result) Blocks() []Managed {
	var blocks []Managed
	if len(r.Addresses) > 0 {
		for _, a := range r.Addresses {
			blocks = append(blocks, Managed{Type: a.Type, ID: a.ID, Address: rootfile.RemovedFrom(a.Address)})
		}
		return blocks
	}
	for _, imp := range r.Imported {
		blocks = append(blocks, Managed{Type: imp.Type, ID: imp.ID, Address: imp.Type + "." + imp.Name})
	}
	return blocks
}

// WithKept returns what a root holds once kept, resource blocks written
// into it besides what Generate wrote, such as those of a merged root an
// environment's root generated afresh lacks, are in it too: r, with kept
// imported after its own resources, at their labels. Its addresses gain
// them only where r records addresses, as a result without any has only
// labels (see Blocks). r stays as it is.
func (r *Result) WithKept(kept []rootfile.Import) *Result {
	with := *r
	with.Imported = append(slices.Clone(r.Imported), kept...)
	if len(r.Addresses) > 0 {
		with.Addresses = slices.Clone(r.Addresses)
		for _, k := range kept {
			with.Addresses = append(with.Addresses, Managed{Type: k.Type, ID: k.ID, Address: k.Type + "." + k.Name})
		}
	}
	return &with
}

// ModuleCall is a module call Generate made, or a cluster of resources it
// tried to move into a module and didn't.
type ModuleCall struct {
	// Name is the call's name; Source and Version are the module's.
	Name    string
	Source  string
	Version string
	// Resources are the addresses the resources had in the root.
	Resources []string
	// Declined, if set, is why the resources stayed where they were.
	Declined string
}
