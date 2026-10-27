// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package cfn translates AWS CloudFormation templates into Terraform
// configuration for the AWS provider.
//
// Parse reads a template, in JSON or YAML with or without the short-form
// function tags, into a Template; Convert writes it as Terraform files and
// reports, as Diagnostics, what it couldn't carry over:
//
//	t, err := cfn.Parse(file)
//	if err != nil {
//		return err
//	}
//	res, err := cfn.Convert(t, cfn.Options{})
//	if err != nil {
//		return err
//	}
//	for _, name := range res.FileNames() {
//		os.WriteFile(name, res.Files[name], 0o644)
//	}
//	for _, d := range res.Diagnostics {
//		fmt.Println(d)
//	}
//
// The output keeps the template's intent: parameters become variables with
// their constraints as validation, mappings and conditions become locals,
// resources become resource blocks (with count for a conditional one), and
// intrinsic functions become the Terraform expressions that compute the
// same values. CloudFormation ideas that Terraform has no equivalent of,
// such as stacks, exports, custom resources and macros, become stand-in
// variables or comments, each with a diagnostic, so nothing is dropped
// silently.
//
// What a resource type, property and attribute are called in Terraform
// comes from a Mapper. DefaultMapper knows the common AWS types and derives
// the rest from their names, marking the derived names as guesses; a
// caller with a better table, such as one built from the provider's schema,
// passes its own in Options, embedding BaseMapper for what its table
// leaves out.
//
// The package depends on nothing else of infraharvest's but internal/cfnhcl,
// the names and numbers it shares with the rest, which depends on nothing,
// so that it can be used, and later published, on its own. See the README
// for what is and isn't converted.
package cfn
