// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
)

// queuesForEach is the code of a merged root's queues of one shape: one
// resource with for_each over a map variable each environment sets, and
// its import block.
const queuesForEach = `variable "sqs_queues" {
  description = "The instances of aws_sqs_queue.this in the environment, by key."
  type = map(object({
    message_retention_seconds = number
    name                      = string
  }))
}

resource "aws_sqs_queue" "this" {
  for_each = var.sqs_queues

  delay_seconds             = 5
  message_retention_seconds = each.value.message_retention_seconds
  name                      = each.value.name
}

import {
  for_each = var.sqs_queues

  to = aws_sqs_queue.this[each.key]
  id = var.import_ids[format("aws_sqs_queue.this[%q]", each.key)]
}
`

// queuesVars is an environment's value of var.sqs_queues.
const queuesVars = `env = "dev"
sqs_queues = {
  "a" = {
    message_retention_seconds = 3600
    name                      = "shop-dev-a"
  }
  "b" = {
    message_retention_seconds = 7200
    name                      = "shop-dev-b"
  }
}
`

// queuesLocal is queuesForEach with for_each over a local map instead.
const queuesLocal = `locals {
  sqs_queues = {
    "a" = { message_retention_seconds = 3600, name = "a" }
    "b" = { message_retention_seconds = 7200, name = "b" }
  }
}

resource "aws_sqs_queue" "this" {
  for_each = local.sqs_queues

  delay_seconds             = 5
  message_retention_seconds = each.value.message_retention_seconds
  name                      = each.value.name
}
`

// alertsFeature is a resource of a feature only some environments have.
const alertsFeature = `variable "deploy_alerts" {
  description = "Whether the environment has aws_sqs_queue.alerts."
  type        = bool
  default     = false
}

resource "aws_sqs_queue" "alerts" {
  count = var.deploy_alerts ? 1 : 0

  name          = "shop-prd-alerts"
  delay_seconds = 0
}
`

// sharedDelay is a variable three queues get their delay from, the third
// only in environments with its feature.
const sharedDelay = `variable "delay" {
  description = "The delay of the queues."
  type        = number
}

resource "aws_sqs_queue" "a" {
  delay_seconds = var.delay
}

resource "aws_sqs_queue" "b" {
  delay_seconds = var.delay
}

resource "aws_sqs_queue" "c" {
  count = var.deploy_c ? 1 : 0

  delay_seconds = var.delay
}
`

// formatted is content as the edits leave a file: formatted.
func formatted(content string) string {
	return string(hclwrite.Format([]byte(content)))
}

// A change of a resource whose block makes it with for_each or count is
// taken as hand-written code would be: in the instance's key of the map
// for_each is over, in a block that makes one instance, and, in a root
// environments share, only where it reaches no other environment.
func TestDriftEditsInstances(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		files  map[string]string
		change *Change
		setup  func(e *driftEditor)
		// want are the files the edit changes; none means there is no
		// edit, for reason.
		want   map[string]string
		kind   string
		reason string
	}{
		{
			name:   "a field of the key in a local map",
			files:  map[string]string{"main.tf": queuesLocal},
			change: driftAt(`aws_sqs_queue.this["b"]`, "message_retention_seconds", float64(9000)),
			kind:   sourceLocal,
			want:   map[string]string{"main.tf": formatted(strings.Replace(queuesLocal, "7200", "9000", 1))},
		},
		{
			name:   "a field of the key in the environment's value of a map variable",
			files:  map[string]string{"main.tf": queuesForEach, "vars/dev/terraform.tfvars": queuesVars},
			change: driftAt(`aws_sqs_queue.this["b"]`, "message_retention_seconds", float64(9000)),
			setup:  devEnvironment,
			kind:   sourceVariable,
			want:   map[string]string{"vars/dev/terraform.tfvars": formatted(strings.Replace(queuesVars, "7200", "9000", 1))},
		},
		{
			name: "a field of the key in the map variable's default, which the var file then sets",
			files: map[string]string{
				"main.tf":                   strings.Replace(queuesForEach, "  }))\n}", "  }))\n  default = {\n    a = { message_retention_seconds = 3600, name = \"a\" }\n    b = { message_retention_seconds = 7200, name = \"b\" }\n  }\n}", 1),
				"vars/dev/terraform.tfvars": "env = \"dev\"\n",
			},
			change: driftAt(`aws_sqs_queue.this["b"]`, "message_retention_seconds", float64(9000)),
			setup:  devEnvironment,
			kind:   sourceVariable,
			want:   map[string]string{"vars/dev/terraform.tfvars": formatted("env = \"dev\"\nsqs_queues = {\n  a = { message_retention_seconds = 3600, name = \"a\" }\n  b = { message_retention_seconds = 9000, name = \"b\" }\n}\n")},
		},
		{
			name:   "an argument every instance gets",
			files:  map[string]string{"main.tf": queuesForEach, "vars/dev/terraform.tfvars": queuesVars},
			change: driftAt(`aws_sqs_queue.this["b"]`, "delay_seconds", float64(10)),
			setup:  devEnvironment,
			kind:   sourceLiteral,
			reason: "every instance of the block's for_each gets it",
		},
		{
			name:   "a field the block reads twice",
			files:  map[string]string{"main.tf": strings.Replace(queuesLocal, "delay_seconds             = 5", "delay_seconds             = 5\n  max_message_size          = each.value.message_retention_seconds", 1)},
			change: driftAt(`aws_sqs_queue.this["b"]`, "message_retention_seconds", float64(9000)),
			kind:   sourceExpression,
			reason: "other arguments of the block read too",
		},
		{
			name:   "a local map other blocks use",
			files:  map[string]string{"main.tf": queuesLocal + "\noutput \"queues\" {\n  value = local.sqs_queues\n}\n"},
			change: driftAt(`aws_sqs_queue.this["b"]`, "message_retention_seconds", float64(9000)),
			kind:   sourceSharedLocal,
			reason: "other blocks use too",
		},
		{
			name:   "a local map of a root environments share",
			files:  map[string]string{"main.tf": queuesLocal, "vars/dev/terraform.tfvars": "env = \"dev\"\n"},
			change: driftAt(`aws_sqs_queue.this["b"]`, "message_retention_seconds", float64(9000)),
			setup:  devEnvironment,
			kind:   sourceLocal,
			reason: "the environments share this root's code",
		},
		{
			name:   "a map variable of a root no environments share",
			files:  map[string]string{"main.tf": queuesForEach},
			change: driftAt(`aws_sqs_queue.this["b"]`, "message_retention_seconds", float64(9000)),
			kind:   sourceVariable,
			reason: "change the value the root is given",
		},
		{
			name:   "a resource of a feature only the environment has",
			files:  map[string]string{"main.tf": alertsFeature, "vars/prod/terraform.tfvars": "deploy_alerts = true\n"},
			change: driftAt("aws_sqs_queue.alerts[0]", "delay_seconds", float64(30)),
			setup: func(e *driftEditor) {
				e.env, e.envVars, e.ownFlags = "prod", hclfile.EnvVarsPath("prod"), map[string]bool{"deploy_alerts": true}
			},
			kind: sourceLiteral,
			want: map[string]string{"main.tf": formatted(strings.Replace(alertsFeature, "delay_seconds = 0", "delay_seconds = 30", 1))},
		},
		{
			name:   "a resource of a feature other environments have too",
			files:  map[string]string{"main.tf": alertsFeature, "vars/prod/terraform.tfvars": "deploy_alerts = true\n"},
			change: driftAt("aws_sqs_queue.alerts[0]", "delay_seconds", float64(30)),
			setup: func(e *driftEditor) {
				e.env, e.envVars = "prod", hclfile.EnvVarsPath("prod")
			},
			kind:   sourceLiteral,
			reason: "the environments share this root's code",
		},
		{
			name:   "a resource of a feature, in a root no environments share",
			files:  map[string]string{"main.tf": alertsFeature},
			change: driftAt("aws_sqs_queue.alerts[0]", "delay_seconds", float64(30)),
			kind:   sourceLiteral,
			want:   map[string]string{"main.tf": formatted(strings.Replace(alertsFeature, "delay_seconds = 0", "delay_seconds = 30", 1))},
		},
		{
			name:   "a block that makes several resources with count",
			files:  map[string]string{"main.tf": strings.Replace(alertsFeature, "var.deploy_alerts ? 1 : 0", "2", 1)},
			change: driftAt("aws_sqs_queue.alerts[0]", "delay_seconds", float64(30)),
			kind:   sourceExpression,
			reason: "several resources with count",
		},
		{
			name:   "a variable other arguments use, which changed alike in the cloud",
			files:  map[string]string{"main.tf": sharedDelay, "vars/dev/terraform.tfvars": "delay = 10\n"},
			change: driftAt("aws_sqs_queue.a", "delay_seconds", float64(30)),
			setup: func(e *driftEditor) {
				devEnvironment(e)
				// The third queue's feature isn't the environment's.
				e.current = map[string]any{"aws_sqs_queue.a": map[string]any{"delay_seconds": float64(30)}, "aws_sqs_queue.b": map[string]any{"delay_seconds": float64(30)}}
			},
			kind: sourceVariable,
			want: map[string]string{"vars/dev/terraform.tfvars": "delay = 30\n"},
		},
		{
			name:   "a variable other arguments use, one of which didn't change alike",
			files:  map[string]string{"main.tf": sharedDelay, "vars/dev/terraform.tfvars": "delay = 10\n"},
			change: driftAt("aws_sqs_queue.a", "delay_seconds", float64(30)),
			setup: func(e *driftEditor) {
				devEnvironment(e)
				e.current = map[string]any{"aws_sqs_queue.a": map[string]any{"delay_seconds": float64(30)}, "aws_sqs_queue.b": map[string]any{"delay_seconds": float64(10)}}
			},
			kind:   sourceVariable,
			reason: "var.delay sets aws_sqs_queue.b delay_seconds too, which doesn't have the cloud's value",
		},
		{
			name:   "a variable other arguments use, of a resource the environment's state doesn't have",
			files:  map[string]string{"main.tf": sharedDelay, "vars/dev/terraform.tfvars": "delay = 10\n"},
			change: driftAt("aws_sqs_queue.a", "delay_seconds", float64(30)),
			setup: func(e *driftEditor) {
				devEnvironment(e)
				e.current = map[string]any{"aws_sqs_queue.a": map[string]any{"delay_seconds": float64(30)}}
			},
			kind:   sourceVariable,
			reason: "which the environment's state doesn't have",
		},
		{
			name:   "a variable a template uses too",
			files:  map[string]string{"main.tf": sharedDelay + "\nresource \"aws_sqs_queue\" \"d\" {\n  name = \"q-${var.delay}\"\n}\n", "vars/dev/terraform.tfvars": "delay = 10\n"},
			change: driftAt("aws_sqs_queue.a", "delay_seconds", float64(30)),
			setup: func(e *driftEditor) {
				devEnvironment(e)
				e.current = map[string]any{"aws_sqs_queue.a": map[string]any{"delay_seconds": float64(30)}, "aws_sqs_queue.b": map[string]any{"delay_seconds": float64(30)}}
			},
			kind:   sourceVariable,
			reason: "other arguments use too",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := testEditor(map[string]map[string]any{tc.change.Address: {tc.change.Attribute: tc.change.value}})
			if tc.setup != nil {
				tc.setup(e)
			}
			contents := byteFiles(tc.files)
			before := maps.Clone(contents)
			names, applied, failed, err := e.apply(contents, []*Change{tc.change})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil {
				src, ok := failed[tc.change]
				if !ok {
					t.Fatalf("edited %v as %+v, want no edit", names, applied[tc.change])
				}
				if src.Kind != tc.kind || !strings.Contains(src.Reason, tc.reason) {
					t.Errorf("source %s: %q, want %s saying %q", src.Kind, src.Reason, tc.kind, tc.reason)
				}
				if !reflect.DeepEqual(contents, before) {
					t.Errorf("the files changed: %v", names)
				}
				return
			}
			if src, ok := failed[tc.change]; ok {
				t.Fatalf("no edit: %s: %s", src.Kind, src.Reason)
			}
			if got := applied[tc.change].Kind; got != tc.kind {
				t.Errorf("source %s, want %s", got, tc.kind)
			}
			for name, content := range contents {
				want, ok := tc.want[name]
				if !ok {
					want = tc.files[name]
				}
				if string(content) != want {
					t.Errorf("%s:\n%s\nwant:\n%s", name, content, want)
				}
			}
		})
	}
}

// nsgTags is the code of a merged root's group whose tags a map variable
// each environment sets gives it.
const nsgTags = `variable "network_security_group_tags" {
  description = "The tags of azurerm_network_security_group.main."
  type        = map(string)
}

resource "azurerm_network_security_group" "main" {
  name = "nsg"
  tags = var.network_security_group_tags
}
`

// nsgVars is dev's value of var.network_security_group_tags.
const nsgVars = `network_security_group_tags = {
  env   = "dev"
  owner = "team-a"
}
`

// A key of a map a variable sets as a whole, in a root environments
// share, is taken into the key of the literal the environment's var file
// sets the variable to, the rest kept; where the file doesn't set it, or
// its value reaches an argument without the cloud's, it is a hand edit.
func TestDriftEditsEnvironmentMapKeys(t *testing.T) {
	t.Parallel()
	const address = "azurerm_network_security_group.main"
	other := nsgTags + "\nresource \"azurerm_network_security_group\" \"other\" {\n  name = \"other\"\n  tags = var.network_security_group_tags\n}\n"
	for _, tc := range []struct {
		name   string
		files  map[string]string
		change *Change
		// after is the cloud's tags of the group.
		after map[string]any
		// current are the values the plan of the code has, by address.
		current map[string]any
		// want are the files the edit changes; none means there is no
		// edit, for reason.
		want   map[string]string
		reason string
	}{
		{
			name:   "a key the var file sets",
			files:  map[string]string{"main.tf": nsgTags, "vars/dev/terraform.tfvars": nsgVars},
			change: driftAt(address, "tags.owner", "team-c"),
			after:  map[string]any{"env": "dev", "owner": "team-c"},
			want:   map[string]string{"vars/dev/terraform.tfvars": formatted(strings.Replace(nsgVars, "team-a", "team-c", 1))},
		},
		{
			name:   "a key the cloud added",
			files:  map[string]string{"main.tf": nsgTags, "vars/dev/terraform.tfvars": nsgVars},
			change: driftAt(address, "tags.team", "ops"),
			after:  map[string]any{"env": "dev", "owner": "team-a", "team": "ops"},
			want:   map[string]string{"vars/dev/terraform.tfvars": formatted(strings.Replace(nsgVars, "}\n", "  team = \"ops\"\n}\n", 1))},
		},
		{
			name:   "a key the cloud removed",
			files:  map[string]string{"main.tf": nsgTags, "vars/dev/terraform.tfvars": nsgVars},
			change: driftAt(address, "tags.env", nil),
			after:  map[string]any{"owner": "team-a"},
			want:   map[string]string{"vars/dev/terraform.tfvars": formatted("network_security_group_tags = {\n  owner = \"team-a\"\n}\n")},
		},
		{
			name:   "a key of a variable the var file doesn't set",
			files:  map[string]string{"main.tf": nsgTags, "vars/dev/terraform.tfvars": "env = \"dev\"\n"},
			change: driftAt(address, "tags.owner", "team-c"),
			after:  map[string]any{"env": "dev", "owner": "team-c"},
			reason: "vars/dev/terraform.tfvars doesn't set it",
		},
		{
			name:    "a key of a variable another group uses, which changed alike",
			files:   map[string]string{"main.tf": other, "vars/dev/terraform.tfvars": nsgVars},
			change:  driftAt(address, "tags.owner", "team-c"),
			after:   map[string]any{"env": "dev", "owner": "team-c"},
			current: map[string]any{address: map[string]any{"tags": map[string]any{"env": "dev", "owner": "team-c"}}, "azurerm_network_security_group.other": map[string]any{"tags": map[string]any{"env": "dev", "owner": "team-c"}}},
			want:    map[string]string{"vars/dev/terraform.tfvars": formatted(strings.Replace(nsgVars, "team-a", "team-c", 1))},
		},
		{
			name:    "a key of a variable another group uses, which didn't change",
			files:   map[string]string{"main.tf": other, "vars/dev/terraform.tfvars": nsgVars},
			change:  driftAt(address, "tags.owner", "team-c"),
			after:   map[string]any{"env": "dev", "owner": "team-c"},
			current: map[string]any{address: map[string]any{"tags": map[string]any{"env": "dev", "owner": "team-c"}}, "azurerm_network_security_group.other": map[string]any{"tags": map[string]any{"env": "dev", "owner": "team-a"}}},
			reason:  "var.network_security_group_tags sets azurerm_network_security_group.other tags too, which doesn't have the cloud's value",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := testEditor(map[string]map[string]any{address: {"tags": tc.after}})
			devEnvironment(e)
			if tc.current != nil {
				e.current = tc.current
			}
			contents := byteFiles(tc.files)
			before := maps.Clone(contents)
			names, applied, failed, err := e.apply(contents, []*Change{tc.change})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil {
				src, ok := failed[tc.change]
				if !ok {
					t.Fatalf("edited %v as %+v, want no edit", names, applied[tc.change])
				}
				if src.Kind != sourceVariable || !strings.Contains(src.Reason, tc.reason) {
					t.Errorf("source %s: %q, want %s saying %q", src.Kind, src.Reason, sourceVariable, tc.reason)
				}
				if !reflect.DeepEqual(contents, before) {
					t.Errorf("the files changed: %v", names)
				}
				return
			}
			if src, ok := failed[tc.change]; ok {
				t.Fatalf("no edit: %s: %s", src.Kind, src.Reason)
			}
			if src := applied[tc.change]; src.Kind != sourceVariable || src.File != "vars/dev/terraform.tfvars" {
				t.Errorf("source %s in %s, want %s in vars/dev/terraform.tfvars", src.Kind, src.File, sourceVariable)
			}
			for name, content := range contents {
				want, ok := tc.want[name]
				if !ok {
					want = tc.files[name]
				}
				if string(content) != want {
					t.Errorf("%s:\n%s\nwant:\n%s", name, content, want)
				}
			}
		})
	}
}

func devEnvironment(e *driftEditor) {
	e.env, e.envVars = "dev", hclfile.EnvVarsPath("dev")
}

// The bool variables only one environment sets true, by its var files or
// their defaults, are its own.
func TestEnvOnlyFlags(t *testing.T) {
	t.Parallel()
	dir, _ := acceptRoot(t, map[string]string{
		"main.tf": `variable "deploy_alerts" {
  type    = bool
  default = false
}

variable "deploy_shared" {
  type    = bool
  default = false
}

variable "deploy_default" {
  type    = bool
  default = true
}

variable "deploy_unset" {
  type = bool
}

variable "name" {
  type    = string
  default = "x"
}
`,
		"vars/common.tfvars":         "deploy_shared = true\n",
		"vars/dev/terraform.tfvars":  "deploy_default = false\n",
		"vars/prod/terraform.tfvars": "deploy_alerts = true\ndeploy_unset = true\n",
		"vars/qa/terraform.tfvars":   "deploy_default = false\n",
	})
	files, err := hclfile.ConfigFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for env, want := range map[string]map[string]bool{
		"prod": {"deploy_alerts": true, "deploy_default": true},
		"dev":  {},
		"qa":   {},
	} {
		got, err := envOnlyFlags(dir, env, files)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
			t.Errorf("%s: %v, want %v", env, got, want)
		}
	}
}

// mergedShapes is the code of a merged root with queues of one shape and
// a feature only prod has.
const mergedShapes = queuesForEach + "\n" + alertsFeature

const prodShapesVars = `deploy_alerts = true
sqs_queues = {
  "a" = {
    message_retention_seconds = 3600
    name                      = "shop-prd-a"
  }
  "b" = {
    message_retention_seconds = 7200
    name                      = "shop-prd-b"
  }
}
`

// Check as prod of a merged root takes a change of a key of the for_each
// into prod's var file, and one of the feature only prod has into its
// block; dev's var file stays as it was.
func TestDriftAcceptsMergedShapes(t *testing.T) {
	t.Parallel()
	dir, staging := acceptRoot(t, map[string]string{
		"main.tf":                                    mergedShapes,
		"vars/dev/terraform.tfvars":                  queuesVars,
		"vars/prod/terraform.tfvars":                 prodShapesVars,
		"terraform.tfstate.d/prod/terraform.tfstate": driftState,
	})
	queue := func(retention float64) map[string]any {
		return map[string]any{"message_retention_seconds": retention, "delay_seconds": float64(5)}
	}
	alerts := func(delay float64) map[string]any {
		return map[string]any{"name": "shop-prd-alerts", "delay_seconds": delay}
	}
	update := tfjson.Actions{tfjson.ActionUpdate}
	noop := tfjson.Actions{tfjson.ActionNoop}
	a := driftResource(`aws_sqs_queue.this["a"]`, "aws_sqs_queue", noop, queue(3600), queue(3600))
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		driftResource(`aws_sqs_queue.this["b"]`, "aws_sqs_queue", update, queue(7200), queue(9000)),
		driftResource("aws_sqs_queue.alerts[0]", "aws_sqs_queue", update, alerts(0), alerts(30)),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		a,
		driftResource(`aws_sqs_queue.this["b"]`, "aws_sqs_queue", update, queue(9000), queue(7200)),
		driftResource("aws_sqs_queue.alerts[0]", "aws_sqs_queue", update, alerts(30), alerts(0)),
	}}
	verified := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		a,
		driftResource(`aws_sqs_queue.this["b"]`, "aws_sqs_queue", noop, queue(9000), queue(9000)),
		driftResource("aws_sqs_queue.alerts[0]", "aws_sqs_queue", noop, alerts(30), alerts(30)),
	}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, verified}, Options{Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(result)
	retention := got[`aws_sqs_queue.this["b"] message_retention_seconds`]
	if retention.Outcome != Written {
		t.Errorf("the retention: %s: %s", retention.Outcome, retention.Reason)
	}
	if s := retention.Source; s == nil || s.File != "vars/prod/terraform.tfvars" || s.Line != 8 {
		t.Errorf("the retention's source: %+v, want vars/prod/terraform.tfvars:8", s)
	}
	delay := got["aws_sqs_queue.alerts[0] delay_seconds"]
	if delay.Outcome != Written {
		t.Errorf("the alerts' delay: %s: %s", delay.Outcome, delay.Reason)
	}
	if s := delay.Source; s == nil || s.File != "main.tf" {
		t.Errorf("the alerts' delay's source: %+v, want main.tf", s)
	}
	if got, want := readFile(t, dir, filepath.Join("vars", "prod", "terraform.tfvars")), formatted(strings.Replace(prodShapesVars, "7200", "9000", 1)); got != want {
		t.Errorf("prod's terraform.tfvars:\n%s\nwant:\n%s", got, want)
	}
	if got := readFile(t, dir, filepath.Join("vars", "dev", "terraform.tfvars")); got != queuesVars {
		t.Errorf("dev's terraform.tfvars changed:\n%s", got)
	}
	if got, want := readFile(t, dir, "main.tf"), formatted(strings.Replace(mergedShapes, "delay_seconds = 0", "delay_seconds = 30", 1)); got != want {
		t.Errorf("main.tf:\n%s\nwant:\n%s", got, want)
	}
}
