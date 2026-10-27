// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package driftpolicy_test

import (
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/report"
)

func TestParseRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		s    string
		want driftpolicy.Rule
	}{
		{"all", driftpolicy.Rule{Accept: true}},
		{"attribute=tags.*", driftpolicy.Rule{Accept: true, Field: "attribute", Pattern: "tags.*"}},
		{"type=aws_security_group", driftpolicy.Rule{Accept: true, Field: "type", Pattern: "aws_security_group"}},
		{"address=aws_s3_bucket.*", driftpolicy.Rule{Accept: true, Field: "address", Pattern: "aws_s3_bucket.*"}},
		{"root=aws/*/eu-west-2", driftpolicy.Rule{Accept: true, Field: "root", Pattern: "aws/*/eu-west-2"}},
		{"attribute=tags.a=b", driftpolicy.Rule{Accept: true, Field: "attribute", Pattern: "tags.a=b"}},
	} {
		s, want := tc.s, tc.want
		got, err := driftpolicy.ParseRule(s, true)
		if err != nil || got != want {
			t.Errorf("%s: got %+v (%v), want %+v", s, got, err, want)
		}
		if got.String() != s {
			t.Errorf("%s: written back as %s", s, got.String())
		}
	}
	for _, s := range []string{"", "everything", "name=x", "attribute=", "=x"} {
		if _, err := driftpolicy.ParseRule(s, false); err == nil || !strings.Contains(err.Error(), "--keep") {
			t.Errorf("%q: got %v, want an error naming --keep", s, err)
		}
	}
}

func TestParseRuleLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		s    string
		want driftpolicy.Rule
	}{
		{"all", driftpolicy.Rule{Accept: true}},
		{"  accept   attribute=tags.*  ", driftpolicy.Rule{Accept: true, Field: "attribute", Pattern: "tags.*"}},
		{"keep type=aws_security_group", driftpolicy.Rule{Field: "type", Pattern: "aws_security_group"}},
		{"keep\tall", driftpolicy.Rule{}},
	} {
		if got, err := driftpolicy.ParseRuleLine(tc.s); err != nil || got != tc.want {
			t.Errorf("%q: got %+v (%v), want %+v", tc.s, got, err, tc.want)
		}
	}
	for _, s := range []string{"", "keep", "maybe all", "keep name=x"} {
		if _, err := driftpolicy.ParseRuleLine(s); err == nil || !strings.Contains(err.Error(), "want accept or keep") {
			t.Errorf("%q: got %v, want the line refused", s, err)
		}
	}
}

func TestRulesLastMatchWins(t *testing.T) {
	t.Parallel()
	change := func(root, address, typ, attribute string) report.DriftChange {
		return report.DriftChange{Root: root, Address: address, Type: typ, Attribute: attribute}
	}
	tag := change("aws/1/eu-west-2", "aws_security_group.web", "aws_security_group", "tags.Env")
	rule := change("aws/1/eu-west-2", "aws_security_group.web", "aws_security_group", "ingress[0].cidr_blocks")
	bucket := change("aws/1/eu-west-2", "aws_s3_bucket.logs", "aws_s3_bucket", "versioning[0].enabled")
	elsewhere := change("aws/1/eu-west-1", "aws_s3_bucket.logs", "aws_s3_bucket", "versioning[0].enabled")
	parse := func(accept bool, s string) driftpolicy.Rule {
		r, err := driftpolicy.ParseRule(s, accept)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	rules := []driftpolicy.Rule{parse(true, "all"), parse(false, "type=aws_security_group"), parse(true, "attribute=tags.*")}
	for _, tc := range []struct {
		rules []driftpolicy.Rule
		c     report.DriftChange
		want  bool
	}{
		{nil, tag, false},
		{rules, tag, true},
		{rules, rule, false},
		{rules, bucket, true},
		{[]driftpolicy.Rule{parse(true, "root=aws/*/eu-west-1")}, bucket, false},
		{[]driftpolicy.Rule{parse(true, "root=aws/*/eu-west-1")}, elsewhere, true},
		{[]driftpolicy.Rule{parse(true, "address=aws_s3_bucket.*"), parse(false, "all")}, bucket, false},
	} {
		if got := (driftpolicy.Decider{Rules: tc.rules}).Decide(tc.c).Accept; got != tc.want {
			t.Errorf("%v in %s, %s %s: accepted %v, want %v", tc.rules, tc.c.Root, tc.c.Address, tc.c.Attribute, got, tc.want)
		}
	}
}

// A rule on a type or an address matches a change of shared tags if it
// matches one of the resources the change covers, or, for an address,
// where the tags are kept.
func TestRulesMatchSharedTags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		rule string
		want bool
	}{
		{"attribute=tags.*", true},
		{"attribute=labels.*", false},
		{"type=aws_s3_bucket", true},
		{"type=aws_sqs_queue", true},
		{"type=aws_security_group", false},
		{"address=local.tags", true},
		{"address=aws_s3_bucket.*", true},
		{"address=aws_instance.*", false},
		{"root=aws/*", true},
	} {
		r, err := driftpolicy.ParseRule(tc.rule, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := (driftpolicy.Decider{Rules: []driftpolicy.Rule{r}}).Decide(sharedTags).Accept; got != tc.want {
			t.Errorf("%s: accepted %v, want %v", tc.rule, got, tc.want)
		}
	}
}

// sharedTags is a change of tags two resources, one in a module call, get
// from local.tags.
var sharedTags = report.DriftChange{Root: "aws/1/eu-west-2", Address: "local.tags", Attribute: "tags.Env", Covers: []string{"aws_s3_bucket.logs", "module.queues.aws_sqs_queue.this[0]"}}
