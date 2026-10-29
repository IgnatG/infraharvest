// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// stackResource is a resource a CloudFormation stack manages: the stack's
// name, and the resource's CloudFormation type.
type stackResource struct {
	stack, cloudFormationType string
}

// managingStack returns the stack of managed, the resources of stacks
// that have one physical ID, that manages a listed resource of the
// Terraform type typ with that ID, and whether one does: the first whose
// CloudFormation type, by types (see
// catalogue.Catalogue.CloudFormationTypes), can be listed as typ, or that
// types doesn't know.
func managingStack(managed []stackResource, cat *catalogue.Catalogue, types map[string][]string, typ string) (string, bool) {
	for _, m := range managed {
		if covers, known := stackTypeCovers(cat, types, m.cloudFormationType, typ); !known || covers {
			return m.stack, true
		}
	}
	return "", false
}

// stackTypeCovers reports whether a stack's resource of the CloudFormation
// type cfnType can be listed as the Terraform type typ, by types (see
// catalogue.Catalogue.CloudFormationTypes): as one of its types, or as one that shares their ID (see
// coversType); and whether types knows cfnType at all.
func stackTypeCovers(cat *catalogue.Catalogue, types map[string][]string, cfnType, typ string) (covers, known bool) {
	terraform, known := types[cfnType]
	return known && slices.ContainsFunc(terraform, func(t string) bool { return coversType(cat, t, typ) }), known
}

// coversType reports whether a resource of the Terraform type parent may
// share its ID with one of typ that holds a part of it: typ is parent,
// starts with parent and an underscore, as aws_s3_bucket_policy does, or
// is one of the children the catalogue names for parent.
func coversType(cat *catalogue.Catalogue, parent, typ string) bool {
	if typ == parent || strings.HasPrefix(typ, parent+"_") {
		return true
	}
	e, ok := cat.Entry(parent)
	return ok && slices.Contains(e.Children, typ)
}
