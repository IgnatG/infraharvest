// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

var googleLabels = EffectiveTags{Attribute: "labels", Effective: "effective_labels", ReservedPrefix: "goog-"}

const labelledTopics = `resource "google_pubsub_topic" "orders" {
  name = "orders"
}

resource "google_pubsub_topic" "events" {
  name   = "events"
  labels = null
}

resource "google_pubsub_topic" "owned" {
  name   = "owned"
  labels = { team = "web" }
}

resource "google_pubsub_topic" "google" {
  name = "google"
}

resource "google_dns_record_set" "app" {
  name = "app.example.com."
}
`

// labelledValues are the topics' imported values: an import leaves labels
// empty, and effective_labels has them all.
func labelledValues() map[string]map[string]any {
	effective := map[string]any{"env": "prod", "goog-terraform-provisioned": "true"}
	return map[string]map[string]any{
		"google_pubsub_topic.orders": {"labels": map[string]any{}, "effective_labels": effective},
		"google_pubsub_topic.events": {"labels": nil, "effective_labels": effective},
		"google_pubsub_topic.owned":  {"labels": map[string]any{}, "effective_labels": map[string]any{"team": "web", "env": "prod"}},
		"google_pubsub_topic.google": {"labels": map[string]any{}, "effective_labels": map[string]any{"goog-managed-by": "x"}},
		// No labels in its type's schema.
		"google_dns_record_set.app": {"effective_labels": effective},
	}
}

// Before, Terraform generated no labels, as an import leaves them empty:
// each resource gets those of effective_labels, without Google's own, but
// one whose configuration has labels already, or that has only Google's.
func TestFillTags(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, labelledTopics)

	filled, err := fillTags(dir, googleLabels, labelledValues(), map[string]bool{"google_pubsub_topic.events": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(filled, ","); got != "google_pubsub_topic.orders" {
		t.Errorf("filled %s, want google_pubsub_topic.orders: events is skipped, owned has labels, google only Google's", got)
	}
	generated := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		`resource "google_pubsub_topic" "orders" { name = "orders" labels = { env = "prod" } }`,
		`labels = { team = "web" }`,
		`resource "google_pubsub_topic" "google" { name = "google" }`,
		`resource "google_dns_record_set" "app" { name = "app.example.com." }`,
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, generated)
		}
	}
	if strings.Contains(generated, "goog-") {
		t.Errorf("generated.tf has Google's labels:\n%s", generated)
	}
}

// labelsUpdate plans address as an import that updates its labels and
// terraform_labels, and, with other, another attribute.
func labelsUpdate(address string, other bool) *tfjson.ResourceChange {
	after := map[string]any{"labels": map[string]any{"env": "prod"}, "terraform_labels": map[string]any{"env": "prod"}}
	if other {
		after["effective_labels"] = map[string]any{}
	}
	rc := resourceChange(address, tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"labels": map[string]any{}, "terraform_labels": map[string]any{}}, after)
	rc.Type, _, _ = strings.Cut(address, ".")
	return rc
}

// The labels stay where the plan only updates the state-only labels and
// terraform_labels; a resource whose plan then changes anything else gets
// none, and on a plan error no resource does.
func TestFillEffectiveTags(t *testing.T) {
	stateOnly := map[string][]string{"*": {"labels", "terraform_labels"}}
	noChanges := map[string]string{}
	for _, a := range []string{"google_pubsub_topic.orders", "google_pubsub_topic.events", "google_pubsub_topic.owned", "google_pubsub_topic.google", "google_dns_record_set.app"} {
		noChanges[a] = changeSignature(resourceChange(a, tfjson.Actions{tfjson.ActionNoop}, nil, nil))
	}
	for _, tc := range []struct {
		name    string
		showns  []*tfjson.Plan
		plans   []fakePlan
		want    bool
		labeled []string
	}{
		{
			name:    "state-only updates",
			showns:  []*tfjson.Plan{{ResourceChanges: []*tfjson.ResourceChange{labelsUpdate("google_pubsub_topic.orders", false), labelsUpdate("google_pubsub_topic.events", false)}}},
			want:    true,
			labeled: []string{"orders", "events"},
		},
		{
			name: "another change",
			showns: []*tfjson.Plan{
				{ResourceChanges: []*tfjson.ResourceChange{labelsUpdate("google_pubsub_topic.orders", false), labelsUpdate("google_pubsub_topic.events", true)}},
				{ResourceChanges: []*tfjson.ResourceChange{labelsUpdate("google_pubsub_topic.orders", false)}},
			},
			want:    true,
			labeled: []string{"orders"},
		},
		{
			name:  "a plan error",
			plans: []fakePlan{{diags: []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "bad"}}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, GeneratedFileName, labelledTopics)
			tf := &fakeTerraform{dir: dir, plans: tc.plans, showns: tc.showns}

			filled, err := fillEffectiveTags(context.Background(), tf, dir, googleLabels, labelledValues(), noChanges, stateOnly, nil)
			if err != nil {
				t.Fatal(err)
			}
			if filled != tc.want {
				t.Errorf("filled: %v, want %v", filled, tc.want)
			}
			generated := squashed(readFile(t, dir, GeneratedFileName))
			for _, name := range []string{"orders", "events"} {
				labeled := strings.Contains(generated, `name = "`+name+`" labels = { env = "prod" }`)
				if want := strings.Contains(strings.Join(tc.labeled, ","), name); labeled != want {
					t.Errorf("%s labelled: %v, want %v:\n%s", name, labeled, want, generated)
				}
			}
		})
	}
}

// The catalogue's state-only arguments for every type apply to each type,
// with its own.
func TestStateOnlyOf(t *testing.T) {
	stateOnly := map[string][]string{"*": {"labels"}, "google_compute_network": {"delete_bgp_always_compare_med"}}
	if got := strings.Join(stateOnlyOf(stateOnly, "google_compute_network"), ","); got != "labels,delete_bgp_always_compare_med" {
		t.Errorf("google_compute_network: %s", got)
	}
	if got := strings.Join(stateOnlyOf(stateOnly, "google_pubsub_topic"), ","); got != "labels" {
		t.Errorf("google_pubsub_topic: %s", got)
	}
	if got := stateOnlyOf(map[string][]string{"aws_iam_role": {"force_detach_policies"}}, "aws_iam_role"); len(got) != 1 {
		t.Errorf("aws_iam_role: %v", got)
	}
}

// The labels go with the other arguments, before the nested blocks, as
// the style guide has them, not at the end of the resource.
func TestFillTagsBeforeBlocks(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "google_pubsub_subscription" "orders" {
  name = "orders"
  expiration_policy {
    ttl = "86400s"
  }
}
`)
	values := map[string]map[string]any{"google_pubsub_subscription.orders": {"labels": map[string]any{}, "effective_labels": map[string]any{"env": "prod"}}}
	if _, err := fillTags(dir, googleLabels, values, nil); err != nil {
		t.Fatal(err)
	}
	want := `resource "google_pubsub_subscription" "orders" { name = "orders" labels = { env = "prod" } expiration_policy { ttl = "86400s" } }`
	if got := squashed(readFile(t, dir, GeneratedFileName)); got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}
