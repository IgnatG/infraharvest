// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"regexp"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/pkg/cfn"
)

// cfnGuesses are the names the converter reported as guesses for one
// template: the Terraform types it derived, the argument names it derived
// from property names, and the attributes it derived for a Ref or
// Fn::GetAtt.
type cfnGuesses struct {
	types, arguments, attributes map[string]bool
}

var (
	guessedTypePattern      = regexp.MustCompile(`^the Terraform type (\S+) is derived from `)
	guessedArgumentsPattern = regexp.MustCompile(`^argument names derived from the property names, to check against the provider's schema: (.*)$`)
	guessedArgumentPattern  = regexp.MustCompile(`(\S+) \([^)]*\)`)
	guessedAttributePattern = regexp.MustCompile(`the attribute (\S+), which the mapper guessed`)
	// The first quoted name in a validation error's detail: the type,
	// argument, block or attribute it is about.
	quotedNamePattern = regexp.MustCompile(`"([^"]+)"`)
)

// newCFNGuesses reads the guesses out of a conversion's diagnostics.
func newCFNGuesses(diags []cfn.Diagnostic) cfnGuesses {
	g := cfnGuesses{types: map[string]bool{}, arguments: map[string]bool{}, attributes: map[string]bool{}}
	for _, d := range diags {
		if m := guessedTypePattern.FindStringSubmatch(d.Message); m != nil {
			g.types[m[1]] = true
		}
		if m := guessedArgumentsPattern.FindStringSubmatch(d.Message); m != nil {
			for _, a := range guessedArgumentPattern.FindAllStringSubmatch(m[1], -1) {
				g.arguments[a[1]] = true
			}
		}
		if m := guessedAttributePattern.FindStringSubmatch(d.Message); m != nil {
			g.attributes[m[1]] = true
		}
	}
	return g
}

// excuses reports whether d, an error of terraform validate, is about a
// name the converter reported as a guess: the user is told to check those,
// so they are logged, not failed. A missing required argument is excused
// only when the template has a guessed type, whose arguments the converter
// can't know. Any other error fails, an unsupported argument or an invalid
// type the converter didn't report as a guess too: that is a mapping it
// gets wrong.
func (g cfnGuesses) excuses(d tfjson.Diagnostic) bool {
	name := ""
	if m := quotedNamePattern.FindStringSubmatch(d.Detail); m != nil {
		name = m[1]
	}
	switch d.Summary {
	case "Invalid resource type":
		return g.types[name]
	case "Unsupported argument", "Unsupported block type":
		return g.arguments[name]
	case "Unsupported attribute":
		return g.attributes[name] || g.arguments[name]
	case "Missing required argument":
		return len(g.types) > 0
	}
	return false
}

// A validation error is excused only when it names what the converter
// reported as a guess.
func TestCFNGuessesExcuse(t *testing.T) {
	g := newCFNGuesses([]cfn.Diagnostic{
		{Severity: cfn.Warning, Path: "Resources.Thing.Type", Message: "the Terraform type aws_thing_widget is derived from AWS::Thing::Widget: check that the AWS provider has it"},
		{Severity: cfn.Info, Path: "Resources.Function.Properties", Message: "argument names derived from the property names, to check against the provider's schema: tracing_config (TracingConfig), mode (TracingConfig.Mode)"},
		{Severity: cfn.Info, Path: "Outputs.Arn.Value", Message: "Thing.Arn is the attribute arn, which the mapper guessed"},
	})
	for _, tc := range []struct {
		summary, detail string
		want            bool
	}{
		{"Invalid resource type", `The provider hashicorp/aws does not support resource type "aws_thing_widget".`, true},
		{"Invalid resource type", `The provider hashicorp/aws does not support resource type "aws_s3_bucket_typo".`, false},
		{"Unsupported argument", `An argument named "tracing_config" is not expected here.`, true},
		{"Unsupported argument", `An argument named "bucket_nme" is not expected here.`, false},
		{"Unsupported block type", `Blocks of type "mode" are not expected here.`, true},
		{"Unsupported attribute", `This object has no argument, nested block, or exported attribute named "arn".`, true},
		{"Unsupported attribute", `This object has no argument, nested block, or exported attribute named "id_typo".`, false},
		{"Missing required argument", `The argument "name" is required, but no definition was found.`, true},
		{"Incorrect attribute value type", `Inappropriate value for attribute "tracing_config".`, false},
	} {
		d := tfjson.Diagnostic{Severity: tfjson.DiagnosticSeverityError, Summary: tc.summary, Detail: tc.detail}
		if got := g.excuses(d); got != tc.want {
			t.Errorf("%s: %s: excused %v, want %v", tc.summary, tc.detail, got, tc.want)
		}
	}
	if none := newCFNGuesses(nil); none.excuses(tfjson.Diagnostic{Summary: "Missing required argument", Detail: `The argument "name" is required.`}) {
		t.Error("a missing required argument is excused without a guessed type")
	}
}
