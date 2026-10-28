// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func TestSensitiveValues(t *testing.T) {
	t.Parallel()
	p := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{{
		Change: &tfjson.Change{
			Before:          map[string]any{"name": "app-db", "password": "s3cr3t-value", "nested": []any{map[string]any{"token": "tok-123456"}}, "short": "abc"},
			BeforeSensitive: map[string]any{"password": true, "nested": []any{map[string]any{"token": true}}, "short": true},
		},
	}}}

	got := InPlan(p)

	if want := []string{"s3cr3t-value", "tok-123456"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A sensitive value that another attribute shows in the open isn't a
// secret: the provider marks every SSM parameter's value sensitive, even
// one holding the region the resource is in.
func TestSensitiveValuesLeavesOutPublicOnes(t *testing.T) {
	t.Parallel()
	p := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		{Change: &tfjson.Change{
			Before:          map[string]any{"name": "/app/region", "region": "eu-west-1", "value": "eu-west-1"},
			BeforeSensitive: map[string]any{"value": true},
		}},
		{Change: &tfjson.Change{
			Before:          map[string]any{"name": "/app/key", "value": "k3y-value-1", "tags": map[string]any{"Note": "k3y-value"}},
			BeforeSensitive: map[string]any{"value": true},
		}},
		// A secret's value copied into a function's environment, where the
		// provider doesn't mark it: the copy is the leak, so the value
		// stays in.
		{Change: &tfjson.Change{
			Before:          map[string]any{"secret_string": "Pa55-W0rd!db"},
			BeforeSensitive: map[string]any{"secret_string": true},
		}},
		{Change: &tfjson.Change{
			Before: map[string]any{"environment": []any{map[string]any{"variables": map[string]any{"DB_PW": "Pa55-W0rd!db"}}}},
		}},
	}}

	// k3y-value is only part of the secret: still left in.
	if got, want := InPlan(p), []string{"Pa55-W0rd!db", "k3y-value-1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Every credential format is looked for in every file, the README and the
// other files Literals doesn't read as much as a .tf file; but a
// password argument in a file is no connection string's password.
func TestScanFileCredentialFormats(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		rel, content, want string
	}{
		"GitLab token in the README": {"README.md", "Set the token to glpat-" + fakeSecret("gitlab")[:20] + ".\n", "GitLab token"},
		"npm token in the README":    {"README.md", "npm_" + fakeSecret("npm")[:36] + "\n", "npm token"},
		"SendGrid key in a script":   {"scripts/send.sh", "KEY=SG." + fakeSecret("sg")[:22] + "." + fakeSecret("grid")[:43] + "\n", "SendGrid key"},
		"webhook within the README":  {"README.md", "Alerts go to https://hooks.slack.com/services/" + fakeSecret("hook")[:24] + ".\n", "Slack webhook"},
		"GitHub token in a .tf file": {"generated.tf", "# ghp_" + fakeSecret("gh")[:36] + "\n", "GitHub token"},
		"password argument":          {"generated.tf", "resource \"aws_db_instance\" \"a\" {\n  password = var.db_password\n}\n", ""},
		"password in the README":     {"README.md", "password = var.db_password\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			found := InFile(NewMatcher(nil), tt.rel, tt.content)
			if tt.want == "" {
				if len(found) > 0 {
					t.Errorf("clean file reported: %v", found)
				}
				return
			}
			if want := []string{tt.rel + ": looks like it contains a " + tt.want}; !reflect.DeepEqual(found, want) {
				t.Errorf("got %v, want %v", found, want)
			}
		})
	}
}

// An access key ID is an identifier: on its own, or beside a hex digest,
// it is no credential; beside what looks like its secret, it is.
func TestAWSKeyPair(t *testing.T) {
	t.Parallel()
	for content, want := range map[string]bool{
		`tags = { ` + testKeyID + ` = "tool" }`:                                                false,
		`id = "` + testKeyID + `"` + "\n" + `sha = "3f786850e387550fdab836ed7e6dc881de23001b"`: false,
		`key = "` + testKeyID + `"` + "\n" + `secret = "` + testSecret + `"`:                   true,
		`secret = "` + testSecret + `"`:                                                        false,
		`env = { ID = "` + "ASIA" + testKeyID[4:] + `", SECRET = "` + testSecret + `" }`:       true,
	} {
		if got := AWSKeyPair(content); got != want {
			t.Errorf("AWSKeyPair(%s) = %v, want %v", content, got, want)
		}
	}
}

// TestValueMatcher checks that Matcher finds what a strings.Contains
// of each value would: at the start and the end of a text, among values
// that share their first bytes, and values shorter than minSecretLength.
func TestValueMatcher(t *testing.T) {
	t.Parallel()
	values := []string{"s3cr3t-value", "s3cr3t-other", "abc", "token-1234567"}
	contains := func(content string) bool {
		for _, v := range values {
			if strings.Contains(content, v) {
				return true
			}
		}
		return false
	}
	m := NewMatcher(values)
	for _, content := range []string{
		"", "s3cr3t", "s3cr3t-valu", "s3cr3t-value", "prefix s3cr3t-other", "token-123456 token-1234567",
		"no secret here", "ab", "zzabc", "s3cr3t-s3cr3t-value", "s3cr3t-otherwise",
	} {
		if got, want := m.in(content), contains(content); got != want {
			t.Errorf("in(%q) = %v, want %v", content, got, want)
		}
	}
	if NewMatcher(nil).in("anything") {
		t.Error("a matcher of no values matched")
	}
}

// Made-up key halves, built from parts so that secret scanners don't take
// the source for a leak. betterleaks still takes the secret for a generic
// API key; the betterleaks:allow comment tells it this one is made up.
var (
	testKeyID  = "AKIA" + "EXAMPLE" + "EXAMPLE12"
	testSecret = "Ab3+cD4/eF5gH6iJ7kL8" + "mN9oP0qR1sT2uV3wX4yZ" // betterleaks:allow
)
