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
// Parse and Convert are meant to take templates from anywhere, hostile
// ones included. Neither reads a file, the network or the environment, or
// runs anything a template names, and fuzzing checks that no template
// makes either panic or hang. Parse fails on a template larger than
// 16 MiB, one that nests values more than 512 deep, or one whose YAML
// aliases expand to more than 1M values or 64 MiB of text. Convert fails
// once the expressions it writes pass 64 MiB, which only values built to
// repeat one another reach, and checks a Template built by hand rather
// than by Parse against limits of its own: a nil section entry, or a value
// that contains itself, nests too deeply or repeats values too often, is
// an error rather than a stack overflow or an endless loop. The output is
// Terraform for a person to review: it can still name any resource or
// value the template does.
//
// The package depends on nothing else of infraharvest's but internal/cfnhcl,
// the names and numbers it shares with the rest, which depends on nothing,
// so that it can be used, and later published, on its own. See the README
// for what is and isn't converted.
package cfn
