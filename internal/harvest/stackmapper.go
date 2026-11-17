// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	cfn "github.com/IgnatG/cfn2tf"
	tfjson "github.com/hashicorp/terraform-json"
)

// ProviderWithStackMapper is a provider that names the Terraform side of
// CloudFormation templates from what it knows of its types (see
// cfn.Mapper): the Terraform types each CloudFormation type is imported
// as, checked against the provider's schema, so that the importer carries
// a stack's template into the roots without guessing (AWS --cfn-stack).
// schemas are the providers' schemas of the root being generated.
type ProviderWithStackMapper interface {
	StackMapper(schemas *tfjson.ProviderSchemas) cfn.Mapper
}
