// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"io"
	"slices"
	"sort"
	"strings"
)

// What manages a resource the selection leaves out (see Excluded.Owner and
// OwnerOf).
const (
	// OwnerTerraformState: a state --managed-state read has it.
	OwnerTerraformState = "terraform_state"
	// OwnerTerraformTag: its tags or labels say another Terraform or
	// OpenTofu state manages it.
	OwnerTerraformTag = "terraform_tag"
	// OwnerTerraformRoot: a root of the output has it, which an incremental
	// import leaves out as Terraform manages it there.
	OwnerTerraformRoot = "terraform_root"
	// OwnerCloudFormation: a CloudFormation stack manages it, as the stacks
	// that list it, or its tags, say.
	OwnerCloudFormation = "cloudformation"
	// OwnerOtherTool: its tags say another tool, such as Pulumi, manages it.
	OwnerOtherTool = "other_tool"
	// OwnerCloud: the default selection leaves it out for another reason,
	// mostly as the cloud creates and manages it itself, such as a default
	// VPC.
	OwnerCloud = "cloud"
	// OwnerSelection: the selection file leaves it out, by its entry, a rule
	// or its defaults. That is people's decision, which says nothing of what
	// manages it.
	OwnerSelection = "selection"
)

// The reasons a selection file gives for what it leaves out by people's
// decision (see OwnerSelection).
const (
	ReasonSelectionEntry    = "excluded in the selection file"
	ReasonSelectionRule     = "excluded by a rule in the selection file"
	ReasonSelectionDefaults = "excluded by the selection file's defaults"
	// ReasonSelectionTagsUnread is the reason of a rule on tags that would
	// decide a resource whose tags aren't read, as Terraform's list
	// resources return none: what the rule makes of it can't be told, so it
	// is left out until its entry includes it.
	ReasonSelectionTagsUnread = "excluded by a rule in the selection file on tags, which Terraform's list resources don't return: include its entry to import it"
)

// byTagMarker is in the reason of an exclusion a resource's tags decide,
// such as "managed by Pulumi (per its tag ManagedBy=pulumi; ...)".
const byTagMarker = "per its tag "

// OwnerOf returns what manages a resource the selection leaves out with
// reason, one of the Owner constants; "" for no reason. A selection file
// keeps only the reason, which this reads; what an import leaves out
// itself it records with its owner (see Excluded.Owner).
func OwnerOf(reason string) string {
	switch {
	case reason == "":
		return ""
	case strings.HasPrefix(reason, TerraformPrefix):
		if ByTag(reason) {
			return OwnerTerraformTag
		}
		return OwnerTerraformState
	case strings.HasPrefix(reason, OtherToolPrefix+"CloudFormation"):
		return OwnerCloudFormation
	case strings.HasPrefix(reason, OtherToolPrefix):
		return OwnerOtherTool
	case reason == ReasonSelectionEntry || reason == ReasonSelectionRule || reason == ReasonSelectionDefaults || reason == ReasonSelectionTagsUnread, strings.HasPrefix(reason, OutsideScopePrefix):
		return OwnerSelection
	}
	return OwnerCloud
}

// ByTag reports whether reason says a resource's tags or labels decided
// what manages it.
func ByTag(reason string) bool {
	return strings.Contains(reason, byTagMarker)
}

// Discovery summarises what discover listed, as discover --format json
// prints it: how many resources the default selection includes, and what
// manages those it excludes, in all, by type, by account and region, and
// by the value of each of the tag keys asked for.
type Discovery struct {
	SchemaVersion int    `json:"schema_version"`
	Selection     string `json:"selection"`
	DiscoveryCount
	Types  []DiscoveryType  `json:"types"`
	Scopes []DiscoveryScope `json:"scopes,omitempty"`
	Tags   []DiscoveryTag   `json:"tags,omitempty"`
	// Failures are what couldn't be listed, or listed only in part: the
	// counts miss what they would have found.
	Failures []string `json:"failures,omitempty"`

	tagKeys  []string
	byType   map[string]*DiscoveryCount
	byScope  map[string]*DiscoveryCount
	byTag    map[[2]string]*DiscoveryCount
	finished bool
}

// DiscoveryCount counts listed resources: those the default selection
// includes, and those it excludes, by what manages them (see OwnerOf).
type DiscoveryCount struct {
	Listed         int `json:"listed"`
	Included       int `json:"included"`
	Excluded       int `json:"excluded"`
	TerraformState int `json:"terraform_state"`
	TerraformTag   int `json:"terraform_tag"`
	CloudFormation int `json:"cloudformation"`
	OtherTool      int `json:"other_tool"`
	Cloud          int `json:"cloud"`
}

// DiscoveryType counts the listed resources of one type.
type DiscoveryType struct {
	Type string `json:"type"`
	DiscoveryCount
}

// DiscoveryScope counts the listed resources of one account and region,
// subscription or project and region, such as aws/123456789012/eu-west-2.
type DiscoveryScope struct {
	Scope string `json:"scope"`
	DiscoveryCount
}

// DiscoveryTag counts the listed resources with one value of a tag key,
// "" for those without the tag.
type DiscoveryTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	DiscoveryCount
}

// add counts a resource the default selection excludes with reason, or
// includes if reason is "".
func (c *DiscoveryCount) add(reason string) {
	c.Listed++
	if reason == "" {
		c.Included++
		return
	}
	c.Excluded++
	switch OwnerOf(reason) {
	case OwnerTerraformState:
		c.TerraformState++
	case OwnerTerraformTag:
		c.TerraformTag++
	case OwnerCloudFormation:
		c.CloudFormation++
	case OwnerOtherTool:
		c.OtherTool++
	case OwnerCloud:
		c.Cloud++
	}
}

// NewDiscovery returns an empty summary of the selection file at path,
// which counts by the value of each of tagKeys.
func NewDiscovery(path string, tagKeys []string) *Discovery {
	var keys []string
	for _, key := range tagKeys {
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	return &Discovery{
		SchemaVersion: SchemaVersion,
		Selection:     path,
		tagKeys:       keys,
		byType:        map[string]*DiscoveryCount{},
		byScope:       map[string]*DiscoveryCount{},
		byTag:         map[[2]string]*DiscoveryCount{},
	}
}

// Add counts a listed resource of typ in scope, with its tags, that the
// default selection excludes with reason, or includes if reason is "".
func (d *Discovery) Add(scope, typ, reason string, tags map[string]string) {
	at := func(m map[string]*DiscoveryCount, k string) *DiscoveryCount {
		if m[k] == nil {
			m[k] = &DiscoveryCount{}
		}
		return m[k]
	}
	d.add(reason)
	at(d.byType, typ).add(reason)
	if scope != "" {
		at(d.byScope, scope).add(reason)
	}
	for _, key := range d.tagKeys {
		k := [2]string{key, tags[key]}
		if d.byTag[k] == nil {
			d.byTag[k] = &DiscoveryCount{}
		}
		d.byTag[k].add(reason)
	}
}

// Finish sorts the counts into Types, Scopes and Tags.
func (d *Discovery) Finish() {
	d.Types = make([]DiscoveryType, 0, len(d.byType))
	for typ, c := range d.byType {
		d.Types = append(d.Types, DiscoveryType{Type: typ, DiscoveryCount: *c})
	}
	sort.Slice(d.Types, func(i, j int) bool { return d.Types[i].Type < d.Types[j].Type })
	d.Scopes = nil
	for scope, c := range d.byScope {
		d.Scopes = append(d.Scopes, DiscoveryScope{Scope: scope, DiscoveryCount: *c})
	}
	sort.Slice(d.Scopes, func(i, j int) bool { return d.Scopes[i].Scope < d.Scopes[j].Scope })
	d.Tags = nil
	for k, c := range d.byTag {
		d.Tags = append(d.Tags, DiscoveryTag{Key: k[0], Value: k[1], DiscoveryCount: *c})
	}
	order := map[string]int{}
	for i, key := range d.tagKeys {
		order[key] = i
	}
	sort.Slice(d.Tags, func(i, j int) bool {
		a, b := d.Tags[i], d.Tags[j]
		if a.Key != b.Key {
			return order[a.Key] < order[b.Key]
		}
		return a.Value < b.Value
	})
	sort.Strings(d.Failures)
	d.finished = true
}

// WriteJSON writes the summary as one JSON document, finished first.
func (d *Discovery) WriteJSON(w io.Writer) error {
	if !d.finished {
		d.Finish()
	}
	content, err := marshal(d)
	if err != nil {
		return err
	}
	_, err = w.Write(content)
	return err
}
