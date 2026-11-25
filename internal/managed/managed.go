// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package managed finds the resources Terraform already manages, from
// state, so that an import can leave them out instead of importing them a
// second time. State can hold secrets: it is read only when asked, and
// only resource types, IDs and addresses are kept. The reading itself,
// of files and of the places backends keep state, is pkg/tfstate's; this
// package keeps what an import needs of what it reads, keyed so that a
// listed resource finds the resource that manages it.
package managed

import (
	"context"
	"io"
	"strings"

	"github.com/IgnatG/infraharvest/internal/tftypes"
	"github.com/IgnatG/infraharvest/pkg/tfstate"
)

// ObjectStore reads state from a bucket (see tfstate.ObjectStore).
type ObjectStore = tfstate.ObjectStore

// StreamStore is an ObjectStore that can also open an object to read as it
// goes (see tfstate.StreamStore).
type StreamStore = tfstate.StreamStore

// Stores open the object stores state is read from (see tfstate.Stores).
// Each store is a package of its own under pkg/tfstate/stores, which links
// its cloud's SDK: the command that builds Stores includes only the clouds
// compiled in, so this package links none.
type Stores = tfstate.Stores

// DefaultHCPTerraformHost is the host of HCP Terraform (formerly Terraform
// Cloud); Terraform Enterprise has its own.
const DefaultHCPTerraformHost = tfstate.DefaultHCPTerraformHost

// Resources are managed resources, by "type id" (and "type arn", where a
// resource has an ARN, and "type identity", where its state has the
// resource identity, by its ID, see tftypes.IdentityID), as key makes
// them.
type Resources map[string]Resource

// Resource is a managed resource: Where its state is, and its Type and
// Address there, such as module.vpc.aws_subnet.a[0], and its ID there,
// its id attribute, or its ARN where it has no id.
type Resource struct {
	Where, Type, Address, ID string
}

// Find returns the managed resource of typ with any of ids.
func (r Resources) Find(typ string, ids ...string) (Resource, bool) {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if res, ok := r[key(typ, id)]; ok {
			return res, true
		}
	}
	return Resource{}, false
}

// key is how Resources keys a resource of typ with id: by the type its
// other names share (see tftypes.Canonical), so that a load balancer
// managed as aws_alb is the aws_lb listed, and an Azure resource ID in
// lower case, since Azure doesn't keep one case for them:
// resourceGroups/App in Resource Graph can be resourcegroups/app in
// state. ARNs and Google self-links keep their case, which matters for
// them.
func key(typ, id string) string {
	typ = tftypes.Canonical(typ)
	if isAzureID(id) {
		id = strings.ToLower(id)
	}
	return typ + " " + id
}

// isAzureID reports whether id is an Azure Resource Manager ID, which
// starts with /subscriptions/ or /providers/ in any case.
func isAzureID(id string) bool {
	for _, prefix := range []string{"/subscriptions/", "/providers/"} {
		if len(id) >= len(prefix) && strings.EqualFold(id[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// AzureBlobSuffix returns the host of Blob Storage in the Azure cloud
// environment names (see tfstate.AzureBlobSuffix).
func AzureBlobSuffix(environment string) (string, error) {
	return tfstate.AzureBlobSuffix(environment)
}

// Load reads the state in sources (see tfstate.Read for what a source may
// be), from stores, and returns the resources it manages. A source that
// holds no state, such as a directory without state files or a workspace
// pattern that matches no workspace, fails: it is most likely mistyped,
// and leaving out nothing would import what it manages a second time.
func Load(ctx context.Context, sources []string, stores Stores) (Resources, error) {
	docs, err := tfstate.ReadAll(ctx, sources, stores)
	if err != nil {
		return nil, err
	}
	r := Resources{}
	for _, doc := range docs {
		r.add(doc)
	}
	return r, nil
}

// CheckSources rejects the sources Load couldn't read from what they say
// alone, without reading any state: a URL that doesn't parse or names no
// bucket, container, organization or workspace, and a local path that
// doesn't exist.
func CheckSources(sources []string) error {
	return tfstate.CheckLocations(sources)
}

// Parse adds the managed resources of a state file found at where, in
// format version 4, as Terraform 0.12 and later and OpenTofu write it.
// Version 3, from Terraform 0.11 and earlier, is refused with a hint to
// upgrade it. Each resource has its address in the state, or none where
// the state doesn't name it.
func (r Resources) Parse(content []byte, where string) error {
	doc, err := tfstate.Parse(content, where)
	if err != nil {
		return err
	}
	r.add(doc)
	return nil
}

// ParseFrom is Parse of the state in, read as it is decoded: one resource
// of the state is held at a time, and of it only what the reading keeps, so
// that a large state doesn't take its size in memory twice over. It fails
// on a state larger than the most state is read (see tfstate.ReadState),
// and, as Parse does, adds nothing from a state it fails on.
func (r Resources) ParseFrom(in io.Reader, where string) error {
	doc, err := tfstate.ParseFrom(in, where)
	if err != nil {
		return err
	}
	r.add(doc)
	return nil
}

// add adds the managed resources of doc to r, in the order of the state,
// so that of two instances with an ID in common the later is kept.
func (r Resources) add(doc tfstate.Document) {
	for _, i := range doc.Instances {
		if i.Mode != "managed" {
			continue
		}
		// An instance is found by its ID, its ARN, and, where its state
		// has the resource identity, by that.
		for _, id := range []string{i.ID, i.ARN, i.IdentityID} {
			if id != "" {
				r[key(i.Type, id)] = Resource{Where: doc.Source, Type: i.Type, Address: i.Address, ID: i.ID}
			}
		}
	}
}
