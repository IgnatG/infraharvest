// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/adapters"
)

// A generated module of a bucket and its versioning, as modularize
// writes one: what the calls share stays in it, the rest are variables.
var bucketModule = map[string]string{
	"main.tf": `resource "aws_s3_bucket" "this" {
  bucket        = var.bucket
  force_destroy = false
  tags          = var.tags
}

resource "aws_s3_bucket_versioning" "versioning" {
  bucket = aws_s3_bucket.this.id

  versioning_configuration {
    status = var.versioning_versioning_configuration_status
  }
}
`,
	"variables.tf": `variable "bucket" {
  type = string
}

variable "tags" {
  type = map(string)
}

variable "versioning_versioning_configuration_status" {
  type = string
}
`,
}

// A local module of a queue whose settings are one object, read as its
// attributes are in the ways a module reads them.
var queueModule = map[string]string{
	"main.tf": `resource "aws_sqs_queue" "this" {
  name                       = var.settings.name
  message_retention_seconds  = lookup(var.settings, "retention", null)
  delay_seconds              = try(var.settings.delay, null)
  visibility_timeout_seconds = var.settings["visibility"]
}
`,
	"variables.tf": `variable "settings" {
  type = object({
    name       = string
    retention  = optional(number)
    delay      = optional(number)
    visibility = optional(number)
  })
}
`,
}

// iamRoleModule stands for the iam-role module the IAMRole adapter pins:
// its Inverse says what sets what, so only the variables are read.
var iamRoleModule = map[string]string{
	"main.tf": "resource \"aws_iam_role\" \"this\" {\n  count = 1\n  tags  = var.tags\n}\n",
	"variables.tf": `variable "max_session_duration" {
  type    = number
  default = null
}

variable "name" {
  type    = string
  default = null
}

variable "policies" {
  type    = map(string)
  default = {}
}

variable "tags" {
  type    = map(string)
  default = {}
}
`,
}

const bucketCalls = `module "logs" {
  source = "../../../modules/s3_0a1b2c3d"

  bucket = "logs"
  tags = {
    Env = "prod"
  }
  versioning_versioning_configuration_status = "Enabled"
}

module "data" {
  source = "../../../modules/s3_0a1b2c3d"

  bucket = "data"
  tags = {
    Env = "prod"
  }
  versioning_versioning_configuration_status = "Enabled"
}
`

const queueCall = `module "jobs" {
  source = "../../../modules/queue_0a1b2c3d"

  settings = {
    name       = "jobs"
    retention  = 60
    visibility = 30
  }
}
`

var iamRoleCall = `module "deploy" {
  source  = "` + adapters.IAMRole.Source + `"
  version = "` + adapters.IAMRole.Version + `"

  max_session_duration = 7200
  name                 = "deploy"
  policies = {
    ReadOnlyAccess = "arn:aws:iam::aws:policy/ReadOnlyAccess"
  }
  tags = {
    Env = "prod"
  }
}
`

// testModule is a module of files, in dir, that calls calls, as
// loadDriftModules reads it.
func testModule(t *testing.T, dir string, calls int, files map[string]string) *driftModule {
	t.Helper()
	m, err := newDriftModule(byteFiles(files))
	if err != nil {
		t.Fatal(err)
	}
	m.dir, m.calls = dir, calls
	return m
}

// moduleDrift is a change, the next apply would revert, to the attribute
// of the resource of type typ at address, in a module call, whose new
// value in the cloud is value.
func moduleDrift(address, typ, attribute string, value any) *DriftChange {
	calls, _, _, _ := splitModuleAddress(address)
	return &DriftChange{Address: address, Type: typ, ModuleAddress: "module." + calls[0].name, Attribute: attribute, Kind: DriftChanged, Outcome: DriftReverts, value: value}
}

func TestDriftModuleSourceMapping(t *testing.T) {
	t.Parallel()
	modules := func(t *testing.T) map[string]*driftModule {
		bucket := testModule(t, "../../../modules/s3_0a1b2c3d", 2, bucketModule)
		queue := testModule(t, "../../../modules/queue_0a1b2c3d", 3, queueModule)
		return map[string]*driftModule{
			"logs": bucket, "data": bucket,
			"jobs": queue, "queues": queue, "teams": queue,
			"deploy": testModule(t, ".terraform/modules/deploy", 1, iamRoleModule),
		}
	}
	for _, tc := range []struct {
		name   string
		file   string
		after  map[string]any
		change *DriftChange
		// want is the file as the edit leaves it, or what it has, if
		// contains; neither means there is no edit, for reason.
		want, contains string
		reason         string
		// source is the file the source is in, and module what its
		// Module has.
		source, module string
	}{
		{
			name:   "var.x",
			file:   bucketCalls,
			after:  map[string]any{"bucket": "logs", "tags": map[string]any{"Env": "dev"}},
			change: moduleDrift("module.logs.aws_s3_bucket.this", "aws_s3_bucket", "tags.Env", "dev"),
			want:   strings.Replace(bucketCalls, `Env = "prod"`, `Env = "dev"`, 1),
			source: "main.tf",
			module: "../../../modules/s3_0a1b2c3d/main.tf:4 var.tags",
		},
		{
			name:   "var.x in a nested block",
			file:   bucketCalls,
			after:  map[string]any{"versioning_configuration": []any{map[string]any{"status": "Suspended"}}},
			change: moduleDrift("module.data.aws_s3_bucket_versioning.versioning", "aws_s3_bucket_versioning", "versioning_configuration[0].status", "Suspended"),
			want:   bucketCalls[:strings.LastIndex(bucketCalls, `"Enabled"`)] + `"Suspended"` + bucketCalls[strings.LastIndex(bucketCalls, `"Enabled"`)+len(`"Enabled"`):],
			source: "main.tf",
		},
		{
			name:   "a literal the module's calls share",
			file:   bucketCalls,
			after:  map[string]any{"force_destroy": true},
			change: moduleDrift("module.logs.aws_s3_bucket.this", "aws_s3_bucket", "force_destroy", true),
			reason: "a literal that its 2 calls share",
			source: "../../../modules/s3_0a1b2c3d/main.tf",
		},
		{
			name:   "var.x.k",
			file:   queueCall,
			after:  map[string]any{"name": "work"},
			change: moduleDrift("module.jobs.aws_sqs_queue.this", "aws_sqs_queue", "name", "work"),
			want:   strings.Replace(queueCall, `= "jobs"`, `= "work"`, 1),
			source: "main.tf",
			module: "../../../modules/queue_0a1b2c3d/main.tf:2 var.settings.name",
		},
		{
			name:   "lookup(), typed as the variable is",
			file:   queueCall,
			after:  map[string]any{"message_retention_seconds": float64(120)},
			change: moduleDrift("module.jobs.aws_sqs_queue.this", "aws_sqs_queue", "message_retention_seconds", float64(120)),
			want:   strings.Replace(queueCall, "retention  = 60", "retention  = 120", 1),
		},
		{
			name:   "try(), with an optional attribute the call leaves out",
			file:   queueCall,
			after:  map[string]any{"delay_seconds": float64(5)},
			change: moduleDrift("module.jobs.aws_sqs_queue.this", "aws_sqs_queue", "delay_seconds", float64(5)),
			want:   strings.Replace(queueCall, "    visibility = 30\n", "    visibility = 30\n    delay      = 5\n", 1),
		},
		{
			name:   `var.x["k"]`,
			file:   queueCall,
			after:  map[string]any{"visibility_timeout_seconds": float64(45)},
			change: moduleDrift("module.jobs.aws_sqs_queue.this", "aws_sqs_queue", "visibility_timeout_seconds", float64(45)),
			want:   strings.Replace(queueCall, "visibility = 30", "visibility = 45", 1),
		},
		{
			name: "a count index into a list literal",
			file: `module "queues" {
  source = "../../../modules/queue_0a1b2c3d"
  count  = 2

  settings = [{ name = "a" }, { name = "b" }][count.index]
}
`,
			after:    map[string]any{"name": "c"},
			change:   moduleDrift("module.queues[1].aws_sqs_queue.this", "aws_sqs_queue", "name", "c"),
			contains: `settings = [{ name = "a" }, { name = "c" }][count.index]`,
		},
		{
			name: "a for_each key into the call's for_each",
			file: `module "teams" {
  source = "../../../modules/queue_0a1b2c3d"

  for_each = {
    a = { name = "a" }
    b = { name = "b" }
  }

  settings = each.value
}
`,
			after:    map[string]any{"name": "c"},
			change:   moduleDrift(`module.teams["b"].aws_sqs_queue.this`, "aws_sqs_queue", "name", "c"),
			contains: "    a = { name = \"a\" }\n    b = { name = \"c\" }\n",
		},
		{
			name: "an argument every instance shares",
			file: `module "teams" {
  source   = "../../../modules/queue_0a1b2c3d"
  for_each = toset(["a", "b"])

  settings = { name = "x" }
}
`,
			after:  map[string]any{"name": "c"},
			change: moduleDrift(`module.teams["b"].aws_sqs_queue.this`, "aws_sqs_queue", "name", "c"),
			reason: "isn't each one's own literal",
		},
		{
			name:   "a curated module, through its adapter's Inverse",
			file:   iamRoleCall,
			after:  map[string]any{"tags": map[string]any{"Env": "dev"}},
			change: moduleDrift("module.deploy.aws_iam_role.this[0]", "aws_iam_role", "tags.Env", "dev"),
			want:   strings.Replace(iamRoleCall, `Env = "prod"`, `Env = "dev"`, 1),
			source: "main.tf",
			module: adapters.IAMRole.Source + " " + adapters.IAMRole.Version + ": aws_iam_role.this.tags from var.tags",
		},
		{
			name:     "a curated module's for_each over an input",
			file:     iamRoleCall,
			after:    map[string]any{"policy_arn": "arn:aws:iam::aws:policy/PowerUserAccess"},
			change:   moduleDrift(`module.deploy.aws_iam_role_policy_attachment.this["ReadOnlyAccess"]`, "aws_iam_role_policy_attachment", "policy_arn", "arn:aws:iam::aws:policy/PowerUserAccess"),
			contains: `ReadOnlyAccess = "arn:aws:iam::aws:policy/PowerUserAccess"`,
		},
		{
			name:     "a curated module's default, left out",
			file:     iamRoleCall,
			after:    map[string]any{"max_session_duration": float64(3600)},
			change:   moduleDrift("module.deploy.aws_iam_role.this[0]", "aws_iam_role", "max_session_duration", float64(3600)),
			contains: "\n  name = \"deploy\"\n",
		},
		{
			name:   "a curated module's attribute without an Inverse entry",
			file:   iamRoleCall,
			after:  map[string]any{"name": "other"},
			change: moduleDrift("module.deploy.aws_iam_role.this[0]", "aws_iam_role", "name", "other"),
			reason: "doesn't trace back to the call's arguments",
			source: "main.tf",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := testEditor(map[string]map[string]any{tc.change.Address: tc.after})
			e.modules = modules(t)
			contents := map[string][]byte{"main.tf": []byte(tc.file)}
			before := maps.Clone(contents)
			_, applied, failed, err := e.apply(contents, []*DriftChange{tc.change})
			if err != nil {
				t.Fatal(err)
			}
			src, edited := applied[tc.change]
			if !edited {
				src = failed[tc.change]
			}
			if tc.source != "" && src.File != tc.source {
				t.Errorf("source in %q, want %q", src.File, tc.source)
			}
			if tc.module != "" && src.Module != tc.module {
				t.Errorf("module path %q, want %q", src.Module, tc.module)
			}
			if tc.want == "" && tc.contains == "" {
				if edited {
					t.Fatalf("edited as %+v, want no edit", src)
				}
				if !strings.Contains(src.Reason, tc.reason) {
					t.Errorf("source %s: %q, want one saying %q", src.Kind, src.Reason, tc.reason)
				}
				if !reflect.DeepEqual(contents, before) {
					t.Error("the files changed")
				}
				return
			}
			if !edited {
				t.Fatalf("no edit: %s: %s", src.Kind, src.Reason)
			}
			got := string(contents["main.tf"])
			if tc.want != "" && got != tc.want {
				t.Errorf("main.tf:\n%s\nwant:\n%s", got, tc.want)
			}
			if tc.contains != "" && !strings.Contains(got, tc.contains) {
				t.Errorf("main.tf:\n%s\nwant it to have:\n%s", got, tc.contains)
			}
		})
	}
}

// driftModuleRoot writes a root at aws/123/eu-west-2 of an output directory,
// with root's files, and the modules under modules/ by their directory
// names, and the modules.json of an init, whose calls, by key, call the
// modules by those names; it returns the root and where Drift stages it.
func driftModuleRoot(t *testing.T, root map[string]string, modules map[string]map[string]string, calls map[string]string) (dir, staging string) {
	t.Helper()
	dir, staging = acceptRoot(t, root)
	out := filepath.Dir(filepath.Dir(filepath.Dir(dir)))
	for name, files := range modules {
		path := filepath.Join(out, "modules", name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		for file, content := range files {
			if err := os.WriteFile(filepath.Join(path, file), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	type entry struct{ Key, Source, Dir string }
	manifest := struct{ Modules []entry }{Modules: []entry{{Key: "", Source: "", Dir: "."}}}
	for key, name := range calls {
		source := "../../../modules/" + name
		manifest.Modules = append(manifest.Modules, entry{Key: key, Source: source, Dir: source})
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".terraform", "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".terraform", "modules", "modules.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, staging
}

// A module of two resources that get their tags from one variable.
var appModule = map[string]string{
	"main.tf": `resource "aws_s3_bucket" "this" {
  bucket = "app"
  tags   = var.tags
}

resource "aws_sqs_queue" "this" {
  name = "app"
  tags = var.tags
}
`,
	"variables.tf": "variable \"tags\" {\n  type = map(string)\n}\n",
}

const appCall = `module "app" {
  source = "../../../modules/app_0a1b2c3d"

  tags = {
    Env = "prod"
  }
}
`

func moduleTags(address, typ string, actions tfjson.Actions, from, to string) *tfjson.ResourceChange {
	return driftResource(address, typ, actions, map[string]any{"tags": map[string]any{"Env": from}}, map[string]any{"tags": map[string]any{"Env": to}})
}

// An accepted change of a resource in a module call is written into the
// call's argument, and the report says how the module reads it.
func TestDriftAcceptsIntoAModuleCall(t *testing.T) {
	t.Parallel()
	dir, staging := driftModuleRoot(t, map[string]string{"main.tf": appCall}, map[string]map[string]string{"app_0a1b2c3d": appModule}, map[string]string{"app": "app_0a1b2c3d"})
	update, noop := tfjson.Actions{tfjson.ActionUpdate}, tfjson.Actions{tfjson.ActionNoop}
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		moduleTags("module.app.aws_s3_bucket.this", "aws_s3_bucket", update, "prod", "dev"),
		moduleTags("module.app.aws_sqs_queue.this", "aws_sqs_queue", update, "prod", "dev"),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		moduleTags("module.app.aws_s3_bucket.this", "aws_s3_bucket", update, "dev", "prod"),
		moduleTags("module.app.aws_sqs_queue.this", "aws_sqs_queue", update, "dev", "prod"),
	}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		moduleTags("module.app.aws_s3_bucket.this", "aws_s3_bucket", noop, "dev", "dev"),
		moduleTags("module.app.aws_sqs_queue.this", "aws_sqs_queue", noop, "dev", "dev"),
	}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, DriftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range result.Changes {
		if c.Outcome != DriftWritten || c.ModuleAddress != "module.app" {
			t.Errorf("%s %s: %s: %s", c.Address, c.Attribute, c.Outcome, c.Reason)
		}
		want := DriftSource{File: "main.tf", Line: 5, Expression: `"prod"`, ModulePath: "../../../modules/app_0a1b2c3d/main.tf:3 var.tags"}
		if c.Address == "module.app.aws_sqs_queue.this" {
			want.ModulePath = "../../../modules/app_0a1b2c3d/main.tf:8 var.tags"
		}
		if c.Source == nil || *c.Source != want {
			t.Errorf("%s: source %+v, want %+v", c.Address, c.Source, want)
		}
	}
	if got, want := readFile(t, dir, "main.tf"), strings.Replace(appCall, "prod", "dev", 1); got != want {
		t.Errorf("main.tf:\n%s\nwant:\n%s", got, want)
	}
	if got := readFile(t, filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(dir))), "modules", "app_0a1b2c3d"), "main.tf"); got != appModule["main.tf"] {
		t.Errorf("the module changed:\n%s", got)
	}
}

// An edit of a call's argument reaches every resource the module makes
// from it: the plan of the edit catches one that didn't change in the
// cloud, and the edit is left out.
func TestDriftModuleCallEditLeaks(t *testing.T) {
	t.Parallel()
	dir, staging := driftModuleRoot(t, map[string]string{"main.tf": appCall}, map[string]map[string]string{"app_0a1b2c3d": appModule}, map[string]string{"app": "app_0a1b2c3d"})
	update, noop := tfjson.Actions{tfjson.ActionUpdate}, tfjson.Actions{tfjson.ActionNoop}
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		moduleTags("module.app.aws_s3_bucket.this", "aws_s3_bucket", update, "prod", "dev"),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		moduleTags("module.app.aws_s3_bucket.this", "aws_s3_bucket", update, "dev", "prod"),
		moduleTags("module.app.aws_sqs_queue.this", "aws_sqs_queue", noop, "prod", "prod"),
	}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		moduleTags("module.app.aws_s3_bucket.this", "aws_s3_bucket", noop, "dev", "dev"),
		moduleTags("module.app.aws_sqs_queue.this", "aws_sqs_queue", update, "prod", "dev"),
	}}
	result, copied, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, DriftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := outcomes(result)["module.app.aws_s3_bucket.this tags.Env"]
	if c.Outcome != DriftRejectedByPlan || !strings.Contains(c.Reason, "module.app.aws_sqs_queue.this") {
		t.Errorf("bucket: %s: %s", c.Outcome, c.Reason)
	}
	if got := readFile(t, dir, "main.tf"); got != appCall {
		t.Errorf("main.tf changed:\n%s", got)
	}
	if len(copied.planOpts) != 3 {
		t.Errorf("%d plans, want 3", len(copied.planOpts))
	}
}

func TestSplitModuleAddress(t *testing.T) {
	t.Parallel()
	calls, resource, key, ok := splitModuleAddress(`module.net["a.b"].module.vpc[0].google_compute_network.network["x"]`)
	want := []moduleStep{{name: "net", key: instanceKey{key: "a.b", hasKey: true}}, {name: "vpc", key: instanceKey{index: 0, isNum: true}}}
	if !ok || !reflect.DeepEqual(calls, want) || resource != "google_compute_network.network" || key != (instanceKey{key: "x", hasKey: true}) {
		t.Errorf("got %+v %s %+v %v", calls, resource, key, ok)
	}
	if got := unkeyedAddress(`module.net["a.b"].aws_s3_bucket.this[0]`); got != "module.net.aws_s3_bucket.this" {
		t.Errorf("block address %s", got)
	}
	for _, address := range []string{"aws_s3_bucket.this", "module.x", "module.x.data.aws_iam_policy_document.p"} {
		if _, _, _, ok := splitModuleAddress(address); ok {
			t.Errorf("%s splits", address)
		}
	}
}
