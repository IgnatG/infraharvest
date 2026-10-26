// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package schemameta

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hashicorp/go-version"
)

// Every built-in file parses, is of the provider it is named after, at a
// release, and names in its constraints only arguments it could hold.
func TestBuiltin(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"aws", "azurerm", "google"} {
		f, err := Builtin(provider)
		if err != nil {
			t.Fatal(err)
		}
		if f == nil {
			t.Fatalf("no built-in metadata for %s", provider)
		}
		if want := "registry.terraform.io/hashicorp/" + provider; f.Provider != want {
			t.Errorf("%s.json: provider %q, want %q", provider, f.Provider, want)
		}
		if _, err := version.NewSemver(f.Version); err != nil {
			t.Errorf("%s.json: version %q: %v", provider, f.Version, err)
		}
		if len(f.Resources) == 0 {
			t.Errorf("%s.json: no resource types", provider)
		}
		t.Logf("%s %s: %d resource types", provider, f.Version, len(f.Resources))
		for typ, r := range f.Resources {
			if !strings.HasPrefix(typ, provider+"_") {
				t.Errorf("%s.json: type %s isn't the provider's", provider, typ)
			}
			for path, a := range r.Arguments {
				if r.Framework && len(a.ExactlyOneOf)+len(a.ConflictsWith)+len(a.AtLeastOneOf)+len(a.RequiredWith) > 0 {
					t.Errorf("%s: %s: a Plugin Framework resource with constraints", typ, path)
				}
				for _, list := range [][]string{a.ExactlyOneOf, a.ConflictsWith, a.AtLeastOneOf, a.RequiredWith} {
					for _, other := range list {
						if other == "" || strings.Contains(other, "..") {
							t.Errorf("%s: %s: constraint names %q", typ, path, other)
						}
					}
				}
			}
		}
	}
	if f, err := Builtin("kubernetes"); err != nil || f != nil {
		t.Errorf("Builtin(kubernetes) = %v, %v, want none", f, err)
	}
}

// The generator reads what the providers' code says, value for value: a
// few entries known from the providers' source, of each provider and of
// each kind the generator reads.
func TestBuiltinValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		provider, typ, path string
		check               func(a *Argument) bool
		want                string
	}{
		{"aws", "aws_subnet", "availability_zone", func(a *Argument) bool {
			return slices.Equal(a.ConflictsWith, []string{"availability_zone_id"})
		}, "conflicts with availability_zone_id"},
		{"aws", "aws_subnet", "availability_zone_id", func(a *Argument) bool {
			return slices.Equal(a.ConflictsWith, []string{"availability_zone"})
		}, "conflicts with availability_zone"},
		{"aws", "aws_iam_role", "max_session_duration", func(a *Argument) bool { return a.Default == json.Number("3600") }, "default 3600"},
		{"aws", "aws_iam_role", "path", func(a *Argument) bool { return a.Default == "/" }, `default "/"`},
		{"aws", "aws_cloudwatch_event_rule", "event_pattern", func(a *Argument) bool {
			return slices.Equal(a.AtLeastOneOf, []string{"event_pattern", "schedule_expression"})
		}, "at least one of event_pattern and schedule_expression"},
		{"aws", "aws_subnet", "assign_ipv6_address_on_creation", func(a *Argument) bool { return a.Default == false }, "default false"},
		{"google", "google_storage_bucket", "storage_class", func(a *Argument) bool { return a.Default == "STANDARD" }, `default "STANDARD"`},
		{"google", "google_storage_bucket", "soft_delete_policy.retention_duration_seconds", func(a *Argument) bool { return a.Default == json.Number("604800") }, "default 604800, in a nested block"},
		{"azurerm", "azurerm_storage_account", "account_kind", func(a *Argument) bool { return a.Default == "StorageV2" }, `default "StorageV2"`},
	} {
		f, err := Builtin(tc.provider)
		if err != nil {
			t.Fatal(err)
		}
		if a := f.Argument(tc.typ, tc.path); a == nil || !tc.check(a) {
			t.Errorf("%s.%s: got %+v, want %s", tc.typ, tc.path, a, tc.want)
		}
	}
	// A Plugin Framework resource: its defaults only.
	f, err := Builtin("aws")
	if err != nil {
		t.Fatal(err)
	}
	if r := f.Resource("aws_api_gateway_account"); r == nil || !r.Framework || r.Arguments["cloudwatch_role_arn"] == nil || r.Arguments["cloudwatch_role_arn"].Default != "" {
		t.Errorf("aws_api_gateway_account: got %+v, want a Plugin Framework resource whose cloudwatch_role_arn defaults to \"\"", r)
	}
}

// The NOTICE names, for each built-in file, the provider release it was
// read from and where that release's source is, as MPL-2.0 asks: the
// schema-metadata workflows write it with the files
// (.github/scripts/schema-metadata-notice.sh), and a file committed
// without it would leave it naming another release.
func TestNoticeNamesTheReleases(t *testing.T) {
	t.Parallel()
	notice, err := os.ReadFile(filepath.Join("..", "..", "..", "NOTICE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"aws", "azurerm", "google"} {
		f, err := Builtin(provider)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"internal/catalogue/schemameta/" + provider + ".json.gz",
			"terraform-provider-" + provider + " " + f.Version + ",",
			"https://github.com/hashicorp/terraform-provider-" + provider + "/tree/v" + f.Version,
		} {
			if !strings.Contains(string(notice), want) {
				t.Errorf("NOTICE doesn't name %q: regenerate it with .github/scripts/schema-metadata-notice.sh internal/catalogue/schemameta > NOTICE", want)
			}
		}
	}
	if !strings.Contains(string(notice), "LICENSES/MPL-2.0.txt") {
		t.Error("NOTICE doesn't point at the MPL-2.0 text")
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	f, err := Parse([]byte(`{"provider":"registry.terraform.io/hashicorp/aws","version":"6.70.0","resources":{
"aws_thing":{"arguments":{"retries":{"default":3},"ratio":{"default":0.25},"mode":{"default":"auto","suppress":true},"on":{"default":false},"a":{"conflicts_with":["b"]}}},
"aws_fw":{"framework":true}
}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Argument("aws_thing", "retries").Default; got != json.Number("3") {
		t.Errorf("retries: default %#v, want json.Number 3", got)
	}
	if got := f.Argument("aws_thing", "ratio").Default; got != json.Number("0.25") {
		t.Errorf("ratio: default %#v", got)
	}
	if a := f.Argument("aws_thing", "mode"); a.Default != "auto" || !a.Suppress {
		t.Errorf("mode: %+v", a)
	}
	if got := f.Argument("aws_thing", "on").Default; got != false {
		t.Errorf("on: default %#v, want false", got)
	}
	if r := f.Resource("aws_fw"); r == nil || !r.Framework {
		t.Errorf("aws_fw: %+v", r)
	}
	if f.Argument("aws_other", "x") != nil || (*File)(nil).Argument("aws_thing", "retries") != nil {
		t.Error("an argument without metadata has some")
	}

	for _, bad := range []string{
		`{"provider":"p","version":"1.0.0","resources":{"t":{"arguments":{"x":{"default":[1]}}}}}`,
		`{"provider":"p","version":"1.0.0","resources":{"t":{"arguments":{"x":{"defualt":1}}}}}`,
		`{"provider":"p","resources":{}}`,
		`{"provider":"p","version":"1.0.0","resources":{"t":null}}`,
		`{"provider":"p","version":"1.0.0","resources":{"t":{"arguments":{"x":null}}}}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%s): want an error", bad)
		}
	}
}

// gzipped returns content gzipped, as the generator's files are.
func gzipped(t *testing.T, content string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The overrides replace, add and remove arguments and types of the
// generated metadata.
func TestLoadMergesOverrides(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"aws.json.gz": {Data: gzipped(t, `{"provider":"registry.terraform.io/hashicorp/aws","version":"6.70.0","resources":{
"aws_thing":{"arguments":{"retries":{"default":3},"wrong":{"default":"x"},"kept":{"default":true}}},
"aws_gone":{"arguments":{"a":{"default":1}}},
"aws_fw":{"framework":true,"arguments":{"mode":{"default":"auto"}}}
}}
`)},
		"overrides.json": {Data: []byte(`{"aws":{
"aws_thing":{"arguments":{"retries":{"default":5},"wrong":null,"added":{"conflicts_with":["kept"]}}},
"aws_gone":null,
"aws_fw":{"framework":false,"arguments":{"mode":{"default":"auto","conflicts_with":["other"]}}},
"aws_new":{"arguments":{"x":{"default":false}}}
}}`)},
	}
	o, err := readOverrides(fsys, "overrides.json")
	if err != nil {
		t.Fatal(err)
	}
	f, err := load(fsys, "aws", o["aws"])
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Argument("aws_thing", "retries").Default; got != json.Number("5") {
		t.Errorf("retries: default %v, want the override's 5", got)
	}
	if f.Argument("aws_thing", "wrong") != nil {
		t.Error("wrong: still there")
	}
	if a := f.Argument("aws_thing", "kept"); a == nil || a.Default != true {
		t.Errorf("kept: %+v", a)
	}
	if a := f.Argument("aws_thing", "added"); a == nil || !slices.Equal(a.ConflictsWith, []string{"kept"}) {
		t.Errorf("added: %+v", a)
	}
	if f.Resource("aws_gone") != nil {
		t.Error("aws_gone: still there")
	}
	if r := f.Resource("aws_fw"); r == nil || r.Framework || !slices.Equal(r.Arguments["mode"].ConflictsWith, []string{"other"}) {
		t.Errorf("aws_fw: %+v", r)
	}
	if a := f.Argument("aws_new", "x"); a == nil || a.Default != false {
		t.Errorf("aws_new.x: %+v", a)
	}
	if f, err := load(fsys, "google", nil); f != nil || err != nil {
		t.Errorf("no file: got %v, %v", f, err)
	}

	for _, bad := range []string{
		`{"aws":{"aws_thing":{"arguments":{"x":{"default":[1]}}}}}`,
		`{"aws":{"aws_thing":{"argument":{}}}}`,
		`[]`,
	} {
		if _, err := readOverrides(fstest.MapFS{"o.json": {Data: []byte(bad)}}, "o.json"); err == nil {
			t.Errorf("readOverrides(%s): want an error", bad)
		}
	}
	if _, err := load(fstest.MapFS{"aws.json.gz": {Data: []byte("not gzip")}}, "aws", nil); err == nil {
		t.Error("load of a file that isn't gzip: want an error")
	}
}
