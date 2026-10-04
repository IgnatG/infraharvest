// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package moduleinterface

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/IgnatG/infraharvest/adapters"
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
		Variables: map[string]Variable{"name": {Required: true}, "tags": {}},
		Outputs:   []string{"id"},
	}
	if !reflect.DeepEqual(iface, want) {
		t.Errorf("interface: got %+v, want %+v", iface, want)
	}

	problems := Check(adapters.Adapter{Inputs: []string{"tags", "size"}, Outputs: []string{"id", "arn"}}, iface)
	wantProblems := []string{`no variable "size"`, `the adapter doesn't set the required variable "name"`, `no output "arn"`}
	if !reflect.DeepEqual(problems, wantProblems) {
		t.Errorf("problems: got %q, want %q", problems, wantProblems)
	}
}

func TestGithubSource(t *testing.T) {
	owner, repo, ref, err := githubSource("git::https://github.com/terraform-aws-modules/terraform-aws-s3-bucket?ref=5dc2f1f8")
	if err != nil || owner != "terraform-aws-modules" || repo != "terraform-aws-s3-bucket" || ref != "5dc2f1f8" {
		t.Errorf("got %s %s %s %v", owner, repo, ref, err)
	}
	if _, _, _, err := githubSource("git::https://gitlab.com/a/b?ref=x"); err == nil {
		t.Error("want an error for a source outside GitHub")
	}
}
