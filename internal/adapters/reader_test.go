// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// readerOf returns a reader of the body of the first block in src.
func readerOf(t *testing.T, src string) *Reader {
	t.Helper()
	f, diags := hclwrite.ParseConfig([]byte(src), "main.tf", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	return Read("google_storage_bucket.logs", f.Body().Blocks()[0].Body())
}

// argumentsOf returns a call's arguments as "name=value", in order.
func argumentsOf(call *Call) []string {
	var args []string
	for _, a := range call.Arguments {
		args = append(args, a.Name+"="+strings.TrimSpace(string(a.Value.Bytes())))
	}
	return args
}

// Require sets each input from its argument, in order, and marks them
// read.
func TestRequireSetsTheInputs(t *testing.T) {
	r := readerOf(t, `resource "google_storage_bucket" "logs" {
  name     = "logs"
  project  = "p"
  location = "EU"
}
`)
	var call Call
	if err := r.Require(&call, Pair{"name", "name"}, Pair{"project", "project_id"}, Pair{"location", "location"}); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(argumentsOf(&call), " "), `name="logs" project_id="p" location="EU"`; got != want {
		t.Errorf("arguments: got %s, want %s", got, want)
	}
	if err := r.Done(); err != nil {
		t.Errorf("Done: %v", err)
	}
}

// Require declines an absent or null argument, naming the resource and
// the argument.
func TestRequireDeclinesWhatIsMissing(t *testing.T) {
	r := readerOf(t, `resource "google_storage_bucket" "logs" {
  name    = "logs"
  project = null
}
`)
	var call Call
	for _, arg := range []string{"project", "location"} {
		err := r.Require(&call, Pair{"name", "name"}, Pair{arg, arg})
		if !IsDecline(err) || err.Error() != "google_storage_bucket.logs has no "+arg {
			t.Errorf("%s: got %v, want a decline naming it", arg, err)
		}
	}
}

// CopyAs sets the inputs of the arguments that are set, under the input's
// name, on a call or an object, and leaves out the rest.
func TestCopyAsSetsWhatIsSet(t *testing.T) {
	r := readerOf(t, `resource "google_storage_bucket" "logs" {
  labels        = local.labels
  storage_class = "STANDARD"
  force_destroy = null
}
`)
	var call Call
	r.CopyAs(&call, Pair{"force_destroy", "force_destroy"}, Pair{"labels", "labels"}, Pair{"storage_class", "class"}, Pair{"location", "location"})
	if got, want := strings.Join(argumentsOf(&call), " "), `labels=local.labels class="STANDARD"`; got != want {
		t.Errorf("arguments: got %s, want %s", got, want)
	}
	if err := r.Done(); err != nil {
		t.Errorf("Done: %v", err)
	}

	var o Object
	r.CopyAs(&o, Pair{"storage_class", "class"})
	if got := string(o.Tokens().Bytes()); !strings.Contains(got, "class") || !strings.Contains(got, `"STANDARD"`) {
		t.Errorf("object: got %s", got)
	}
	var empty Object
	r.CopyAs(&empty, Pair{"location", "location"})
	if !empty.Empty() {
		t.Errorf("object: got %s, want it empty", empty.Tokens().Bytes())
	}
}
