// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package moduleinterface

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
)

// moduleArchive is a tar.gz of a module repository at a commit.
func moduleArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestClient(t *testing.T) {
	t.Parallel()
	archive := moduleArchive(t, map[string]string{
		"terraform-aws-thing-abc123/variables.tf":             "variable \"name\" {\n  type = string\n}\n\nvariable \"tags\" {\n  type    = map(string)\n  default = {}\n}\n",
		"terraform-aws-thing-abc123/outputs.tf":               "output \"id\" {\n  value = 1\n}\n",
		"terraform-aws-thing-abc123/modules/sub/variables.tf": "variable \"ignored\" {}\n",
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/modules/acme/thing/aws/versions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"modules":[{"versions":[{"version":"1.0.0"},{"version":"1.2.0"},{"version":"2.0.0-rc.1"}]}]}`))
	})
	mux.HandleFunc("/v1/modules/acme/thing/aws/1.2.0/download", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Terraform-Get", "git::https://github.com/acme/terraform-aws-thing?ref=abc123")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/archive/acme/terraform-aws-thing/abc123", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := &Client{HTTP: server.Client(), Registry: server.URL, Archive: func(owner, repo, ref string) string {
		return server.URL + "/archive/" + owner + "/" + repo + "/" + ref
	}}

	latest, err := client.Latest(t.Context(), "acme/thing/aws")
	if err != nil || latest != "1.2.0" {
		t.Fatalf("latest: %q, %v", latest, err)
	}
	iface, err := client.Fetch(t.Context(), "acme/thing/aws", latest)
	if err != nil {
		t.Fatal(err)
	}
	want := &Interface{
		Source:    "acme/thing/aws",
		Version:   "1.2.0",
		Variables: map[string]Variable{"name": {Required: true, Type: "string"}, "tags": {Type: "map(string)", Default: json.RawMessage(`{}`)}},
		Outputs:   []string{"id"},
	}
	if !reflect.DeepEqual(iface, want) {
		t.Errorf("interface: got %+v, want %+v", iface, want)
	}

	// A submodule's interface is its directory's.
	if latest, err := client.Latest(t.Context(), "acme/thing/aws//modules/sub"); err != nil || latest != "1.2.0" {
		t.Fatalf("latest of a submodule: %q, %v", latest, err)
	}
	sub, err := client.Fetch(t.Context(), "acme/thing/aws//modules/sub", latest)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]Variable{"ignored": {Required: true}}; !reflect.DeepEqual(sub.Variables, want) || sub.Source != "acme/thing/aws//modules/sub" {
		t.Errorf("submodule interface: got %+v", sub)
	}

	problems := Check(adapters.Adapter{Inputs: []string{"tags", "size"}, Outputs: []string{"id", "arn"}}, iface)
	wantProblems := []string{`no variable "size"`, `the adapter doesn't set the required variable "name"`, `no output "arn"`}
	if !reflect.DeepEqual(problems, wantProblems) {
		t.Errorf("problems: got %q, want %q", problems, wantProblems)
	}
}

func TestGithubSource(t *testing.T) {
	t.Parallel()
	owner, repo, ref, err := githubSource("git::https://github.com/terraform-aws-modules/terraform-aws-s3-bucket?ref=5dc2f1f8")
	if err != nil || owner != "terraform-aws-modules" || repo != "terraform-aws-s3-bucket" || ref != "5dc2f1f8" {
		t.Errorf("got %s %s %s %v", owner, repo, ref, err)
	}
	if _, _, _, err := githubSource("git::https://gitlab.com/a/b?ref=x"); err == nil {
		t.Error("want an error for a source outside GitHub")
	}
}

func TestReadRefusesAnOversizedFile(t *testing.T) {
	t.Parallel()
	archive := moduleArchive(t, map[string]string{
		"terraform-aws-thing-abc123/variables.tf": "variable \"name\" {}\n" + strings.Repeat("#", maxRead),
	})
	_, err := Read(bytes.NewReader(archive), "")
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("Read() error = %v, want one saying the file is too large", err)
	}
}

// Read records each variable's type as written and its default when it is
// a plain value; a default with references or function calls, or null,
// isn't recorded.
func TestReadRecordsTypesAndDefaults(t *testing.T) {
	t.Parallel()
	archive := moduleArchive(t, map[string]string{
		"terraform-aws-thing-abc123/variables.tf": `variable "mtu" {
  type    = number
  default = 0
}
variable "mode" {
  default = "LEGACY"
}
variable "rules" {
  type = list(object({
    name = string
  }))
  default = []
}
variable "computed" {
  type    = string
  default = upper("x")
}
variable "none" {
  type    = string
  default = null
}
`,
	})
	iface, err := Read(bytes.NewReader(archive), "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Variable{
		"mtu":      {Type: "number", Default: json.RawMessage(`0`)},
		"mode":     {Default: json.RawMessage(`"LEGACY"`)},
		"rules":    {Type: "list(object({ name = string }))", Default: json.RawMessage(`[]`)},
		"computed": {Type: "string"},
		"none":     {Type: "string"},
	}
	if !reflect.DeepEqual(iface.Variables, want) {
		t.Errorf("variables: got %+v, want %+v", iface.Variables, want)
	}
}

// Check reports an adapter default that isn't the recorded one, compared
// in the adapter's type, and leaves alone a variable whose default wasn't
// recorded.
func TestCheckDefaults(t *testing.T) {
	t.Parallel()
	iface := &Interface{Variables: map[string]Variable{
		"mtu":    {Type: "number", Default: json.RawMessage(`0`)},
		"mode":   {Type: "string", Default: json.RawMessage(`"LEGACY"`)},
		"flag":   {Type: "string", Default: json.RawMessage(`"true"`)},
		"rules":  {Default: json.RawMessage(`[]`)},
		"region": {Type: "string"},
	}}
	a := adapters.Adapter{
		Inputs: []string{"flag", "mode", "mtu", "region", "rules"},
		Defaults: map[string]cty.Value{
			"mtu":    cty.NumberIntVal(1460),
			"mode":   cty.StringVal("LEGACY"),
			"flag":   cty.True,
			"rules":  cty.ListValEmpty(cty.String),
			"region": cty.StringVal("eu"),
		},
	}
	want := []string{`the adapter's default for "mtu" isn't the module's, 0`}
	if got := Check(a, iface); !reflect.DeepEqual(got, want) {
		t.Errorf("problems: got %q, want %q", got, want)
	}
}

// TypeChanges names the inputs whose type changed, and leaves out those
// without a recorded type and the variables the adapter doesn't set.
func TestTypeChanges(t *testing.T) {
	t.Parallel()
	pinned := &Interface{Variables: map[string]Variable{
		"tags":    {Type: "map(string)"},
		"mtu":     {Type: "number"},
		"rules":   {},
		"ignored": {Type: "string"},
	}}
	newer := &Interface{Variables: map[string]Variable{
		"tags":    {Type: "map(any)"},
		"mtu":     {Type: "number"},
		"rules":   {Type: "list(string)"},
		"ignored": {Type: "bool"},
	}}
	want := []string{`"tags" is map(any), was map(string)`}
	if got := TypeChanges(pinned, newer, []string{"tags", "mtu", "rules", "gone"}); !reflect.DeepEqual(got, want) {
		t.Errorf("changes: got %q, want %q", got, want)
	}
}

// Check rejects an Inverse entry of a resource the adapter doesn't map
// onto, onto a variable the module doesn't have, or onto an input path
// that doesn't parse.
func TestCheckInverse(t *testing.T) {
	t.Parallel()
	iface := &Interface{Variables: map[string]Variable{
		"tags":     {Type: "map(string)"},
		"policies": {Type: "map(string)"},
	}}
	a := adapters.Adapter{
		Inputs:  []string{"policies", "tags"},
		Targets: []string{"aws_iam_role.this", "aws_iam_role_policy_attachment.this"},
		Inverse: map[string]string{
			"aws_iam_role.this.tags":                         "tags",
			"aws_iam_role_policy_attachment.this.policy_arn": "policies[each.key]",
			"aws_iam_role.this.description":                  "role_description",
			"aws_iam_instance_profile.this.tags":             "tags",
			"aws_iam_role.this.path":                         "tags[",
		},
	}
	want := []string{
		`Inverse has "aws_iam_instance_profile.this.tags", an attribute of none of the adapter's Targets`,
		`Inverse maps "aws_iam_role.this.description" onto "role_description", but the module has no variable "role_description"`,
		`Inverse maps "aws_iam_role.this.path" onto an input path that doesn't parse: "tags[" has a [ without its ]`,
	}
	if got := Check(a, iface); !reflect.DeepEqual(got, want) {
		t.Errorf("problems: got %q, want %q", got, want)
	}
}
