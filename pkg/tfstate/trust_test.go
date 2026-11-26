// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfstate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func roleState(t *testing.T, policy string) []byte {
	t.Helper()
	p, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(`{"version":4,"serial":1,"lineage":"l","terraform_version":"1.9.0","outputs":{},"resources":[
	 {"mode":"managed","type":"aws_iam_role","name":"r","provider":"provider[\"registry.terraform.io/hashicorp/aws\"]",
	  "instances":[{"attributes":{"id":"r","arn":"arn:aws:iam::111111111111:role/r","assume_role_policy":` + string(p) + `}}]},
	 {"mode":"managed","type":"aws_sqs_queue","name":"q","provider":"provider[\"registry.terraform.io/hashicorp/aws\"]",
	  "instances":[{"attributes":{"id":"q","assume_role_policy":` + string(p) + `}}]}]}`)
}

// A role's trust policy is reduced to who it names, and its text, its Sid and
// its condition values are not kept.
func TestParseRoleTrust(t *testing.T) {
	t.Parallel()
	policy := `{"Version":"2012-10-17","Statement":[
	 {"Sid":"PLANTED-SID","Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":["arn:aws:iam::222222222222:role/ci","333333333333"],"Service":"ec2.amazonaws.com"}},
	 {"Effect":"Allow","Action":["sts:Assume*"],"Principal":"*","Condition":{"StringEquals":{"sts:ExternalId":"PLANTED-EXTERNAL-ID"}}},
	 {"Effect":"Allow","Action":"sts:AssumeRoleWithWebIdentity","Principal":{"Federated":"arn:aws:iam::111111111111:oidc-provider/x"}},
	 {"Effect":"Deny","Action":"sts:AssumeRole","NotPrincipal":{"AWS":"arn:aws:iam::111111111111:user/u"}}]}`
	doc, err := Parse(roleState(t, policy), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Instances) != 2 || doc.Instances[1].Trust != nil || doc.Instances[1].TrustUnreadable {
		t.Fatalf("only an aws_iam_role has a trust policy: %+v", doc.Instances)
	}
	in := doc.Instances[0]
	want := []TrustStatement{
		{Effect: "Allow", Assumes: true, Principals: []TrustPrincipal{
			{"AWS", "arn:aws:iam::222222222222:role/ci"}, {"AWS", "333333333333"}, {"Service", "ec2.amazonaws.com"}}},
		{Effect: "Allow", Assumes: true, Conditional: true, Principals: []TrustPrincipal{{"*", "*"}}},
		{Effect: "Allow", Principals: []TrustPrincipal{{"Federated", "arn:aws:iam::111111111111:oidc-provider/x"}}},
		{Effect: "Deny", Assumes: true, NotPrincipal: true, Principals: []TrustPrincipal{{"AWS", "arn:aws:iam::111111111111:user/u"}}},
	}
	if !reflect.DeepEqual(in.Trust, want) {
		t.Errorf("trust = %+v\nwant %+v", in.Trust, want)
	}
	out, _ := json.Marshal(doc)
	for _, secret := range []string{"PLANTED", "ExternalId", "2012-10-17", "StringEquals"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("the document holds %q: %s", secret, out)
		}
	}
}

func TestParseRoleTrustUnreadable(t *testing.T) {
	t.Parallel()
	for name, policy := range map[string]string{"not json": "{", "no statement": `{"Version":"x"}`, "empty": ""} {
		doc, err := Parse(roleState(t, policy), "s")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if in := doc.Instances[0]; !in.TrustUnreadable || in.Trust != nil {
			t.Errorf("%s: %+v", name, in)
		}
	}
	// A single statement object, not a list, reads too.
	doc, _ := Parse(roleState(t, `{"Statement":{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"111111111111"}}}`), "s")
	if got := doc.Instances[0].Trust; len(got) != 1 || !got[0].Assumes {
		t.Errorf("trust = %+v", got)
	}
}

// A policy over a size cap is unreadable and nothing of it is kept.
func TestParseRoleTrustCaps(t *testing.T) {
	t.Parallel()
	stmt := `{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"111111111111"}}`
	tests := map[string]struct {
		policy     string
		unreadable bool
	}{
		"statements at the cap":        {`{"Statement":[` + strings.TrimSuffix(strings.Repeat(stmt+",", maxTrustStatements), ",") + `]}`, false},
		"statements over the cap":      {`{"Statement":[` + strings.TrimSuffix(strings.Repeat(stmt+",", maxTrustStatements+1), ",") + `]}`, true},
		"principals at the cap":        {`{"Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":[` + strings.TrimSuffix(strings.Repeat(`"a",`, maxTrustPrincipals), ",") + `]}}]}`, false},
		"principals over the cap":      {`{"Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":[` + strings.TrimSuffix(strings.Repeat(`"a",`, maxTrustPrincipals+1), ",") + `]}}]}`, true},
		"principals over across types": {`{"Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":[` + strings.TrimSuffix(strings.Repeat(`"a",`, maxTrustPrincipals), ",") + `],"Service":"x"}}]}`, true},
		"text over the cap":            {`{"Statement":[` + stmt + `],"Pad":"` + strings.Repeat("x", maxTrustText) + `"}`, true},
	}
	for name, tc := range tests {
		doc, err := Parse(roleState(t, tc.policy), "s")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		in := doc.Instances[0]
		if in.TrustUnreadable != tc.unreadable || (tc.unreadable && in.Trust != nil) || (!tc.unreadable && in.Trust == nil) {
			t.Errorf("%s: unreadable=%v, %d statements", name, in.TrustUnreadable, len(in.Trust))
		}
	}
}

// A value that is neither a string nor a list of strings makes the policy
// unreadable, rather than being dropped from it.
func TestParseRoleTrustMixedTypes(t *testing.T) {
	t.Parallel()
	for name, policy := range map[string]string{
		"mixed principal list": `{"Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":["arn:aws:iam::222222222222:root",5]}}]}`,
		"number principal":     `{"Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":5}}]}`,
		"mixed action list":    `{"Statement":[{"Effect":"Allow","Action":["sts:AssumeRole",5],"Principal":{"AWS":"111111111111"}}]}`,
		"principal is a list":  `{"Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":["111111111111"]}]}`,
	} {
		doc, err := Parse(roleState(t, policy), "s")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if in := doc.Instances[0]; !in.TrustUnreadable || in.Trust != nil {
			t.Errorf("%s: %+v", name, in)
		}
	}
}
