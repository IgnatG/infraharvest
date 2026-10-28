// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// The golden tests record what the package's exported API does, in
// testdata/<name>.golden.json, so that a rewrite of it can be shown to
// behave the same. Record them with
//
//	go test ./internal/harvest -run Golden -update
//
// or, without a local toolchain, with the remote workflow's golden task.
var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// goldenCheck compares got, as indented JSON, with testdata/<name>.golden.json,
// or rewrites that file under -update.
func goldenCheck(t *testing.T, name string, got any) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(got); err != nil {
		t.Fatalf("%s: marshal: %v", name, err)
	}
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v: record it with go test ./internal/harvest -run Golden -update (or the remote workflow's golden task)", err)
	}
	want := strings.ReplaceAll(string(content), "\r\n", "\n")
	if got := buf.String(); got != want {
		t.Errorf("%s doesn't match what the code does now; %s\nif the change is intended, rewrite it with go test ./internal/harvest -run Golden -update and review git diff %s", path, goldenFirstDiff(want, got), path)
	}
}

// goldenFirstDiff describes the first line where want and got differ.
func goldenFirstDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		w, g := goldenLine(wantLines, i), goldenLine(gotLines, i)
		if w != g {
			return fmt.Sprintf("first difference at line %d:\n  golden: %s\n  now:    %s", i+1, w, g)
		}
	}
	return "no line differs"
}

func goldenLine(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "<end of file>"
}

// goldenPanics reports whether fn panics.
func goldenPanics(fn func()) (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	fn()
	return false
}

// goldenString is a named string type, a leaf for valuesAt as a string is.
type goldenString string

// goldenCtxKey keys a context value in TestGoldenListerAccessors.
type goldenCtxKey struct{}

// The declarations below fail to compile if the API's signatures change.

type goldenLister struct{ ListerOf[string] }

func (*goldenLister) List(context.Context, Settings) ([]Resource, error) { return nil, nil }

type goldenProvider struct{}

func (*goldenProvider) Init(context.Context, []string) error  { return nil }
func (*goldenProvider) Lister(string) (Lister, error)         { return nil, nil }
func (*goldenProvider) Name() string                          { return "golden" }
func (*goldenProvider) Profile() Profile                      { return Profile{} }
func (*goldenProvider) Services() map[string]Lister           { return nil }
func (*goldenProvider) ProviderBlock() map[string]interface{} { return nil }

type goldenOptional struct{}

func (goldenOptional) ChildImports(context.Context, Resource) ([]Resource, error) {
	return nil, nil
}
func (goldenOptional) ExcludedByDefault(context.Context, []Resource) (map[string]string, error) {
	return nil, nil
}
func (goldenOptional) SetCatalogue(*catalogue.Catalogue)          {}
func (goldenOptional) ModuleProviders() map[string]ModuleProvider { return nil }
func (goldenOptional) Tags(context.Context, []Resource) (map[string]map[string]string, error) {
	return nil, nil
}
func (goldenOptional) Scope(context.Context) (account, region string, err error) {
	return "", "", nil
}
func (goldenOptional) TerraformEnv(context.Context) (map[string]string, error) {
	return nil, nil
}

var (
	_ Lister                        = (*goldenLister)(nil)
	_ Provider                      = (*goldenProvider)(nil)
	_ ProviderWithChildImports      = goldenOptional{}
	_ ProviderWithSelectionDefaults = goldenOptional{}
	_ ProviderWithCatalogue         = goldenOptional{}
	_ ProviderWithModuleProviders   = goldenOptional{}
	_ ProviderWithTags              = goldenOptional{}
	_ ProviderWithScope             = goldenOptional{}
	_ ProviderWithTerraformEnv      = goldenOptional{}

	_ = ModuleProvider{Source: "", Config: map[string]interface{}{}}
	_ = Resource{Address: &Address{Type: ""}, Observed: &Observation{ID: "", Attributes: map[string]string{}}, RawName: "", Provider: ""}
	_ = Filter{Service: "", Path: "", Values: []string{}}
	_ = Settings{Service: "", Provider: "", Verbose: false, Filters: []Filter{}, Unwanted: func(string) bool { return false }}
	_ = ListerOf[string]{Resources: []Resource{}}

	_ SettingsTaker[string] = (*ListerOf[string])(nil)
)

// goldenSignature fails to compile unless its argument has type T.
func goldenSignature[T any](T) {}

// TestGoldenAPISignatures only has to compile: it pins the signatures of the
// package's functions and methods.
func TestGoldenAPISignatures(t *testing.T) {
	goldenSignature[func(string, string, string, string, map[string]string) Resource](Record)
	goldenSignature[func(string, string, string, string) Resource](RecordBare)
	goldenSignature[func(map[string]string) map[string]string](AttributeTags)
	goldenSignature[func(string) (Filter, error)](ParseIDFilter)
	goldenSignature[func(string) (Filter, error)](ParseTagFilter)
	goldenSignature[func(string) (Filter, error)](ParseWhereFilter)
	goldenSignature[func(string, interface{}) []interface{}](valuesAt)
	goldenSignature[func(string, interface{}) bool](hasPath)
	goldenSignature[func(*Filter, Resource) bool]((*Filter).Keeps)
	goldenSignature[func(*Filter, string) bool]((*Filter).AppliesTo)
	goldenSignature[func([]Resource, []Filter) []Resource](Prune)
	goldenSignature[func(Settings, string) bool](Settings.Wants)
	goldenSignature[func(Settings, ...string) bool](Settings.WantsAny)
	goldenSignature[func(*ListerOf[string], context.Context, Settings, func() error) ([]Resource, error)]((*ListerOf[string]).Run)
	goldenSignature[func(*ListerOf[string], context.Context, Settings)]((*ListerOf[string]).Begin)
	goldenSignature[func(*ListerOf[string]) context.Context]((*ListerOf[string]).Context)
	goldenSignature[func(*ListerOf[string]) []Filter]((*ListerOf[string]).Filters)
	goldenSignature[func(*ListerOf[string]) string]((*ListerOf[string]).Name)
	goldenSignature[func(*ListerOf[string]) string]((*ListerOf[string]).Provider)
	goldenSignature[func(*ListerOf[string]) bool]((*ListerOf[string]).Verbose)
	goldenSignature[func(*ListerOf[string], string) bool]((*ListerOf[string]).Wants)
	goldenSignature[func(*ListerOf[string], ...string) bool]((*ListerOf[string]).WantsAny)
	t.Log("the API has the signatures the golden tests expect")
}

func TestGoldenRecord(t *testing.T) {
	cases := []struct {
		name                        string
		id, resourceName, typ, prov string
		attributes                  map[string]string
		simple                      bool
	}{
		{name: "with attributes", id: "vpc-1", resourceName: "main", typ: "aws_vpc", prov: "aws", attributes: map[string]string{"id": "vpc-1", "tags.Name": "main"}},
		{name: "nil attributes", id: "vpc-2", resourceName: "other", typ: "aws_vpc", prov: "aws"},
		{name: "empty attributes", id: "vpc-3", resourceName: "third", typ: "aws_vpc", prov: "aws", attributes: map[string]string{}},
		{name: "simple", id: "https://sqs/1/my queue", resourceName: "my queue", typ: "aws_sqs_queue", prov: "aws", simple: true},
		{name: "all empty", simple: false},
		{name: "all empty, simple", simple: true},
		{name: "odd name", id: "projects/p/global/networks/n", resourceName: "n.with/odd:chars", typ: "google_compute_network", prov: "google", attributes: map[string]string{"labels.env": "prod"}},
		{name: "unicode name", id: "/subscriptions/s/resourceGroups/rg", resourceName: "grupă", typ: "azurerm_resource_group", prov: "azurerm", simple: true},
		{name: "html in name", id: "<id>", resourceName: "a<b>&c", typ: "aws_x", prov: "aws", attributes: map[string]string{"k": "<v>"}},
		{name: "prefixed name", id: "x", resourceName: "r--x", typ: "aws_x", prov: "aws", simple: true},
		{name: "type not matching provider", id: "x", resourceName: "x", typ: "google_x", prov: "aws", simple: true},
	}
	type result struct {
		Case              string          `json:"case"`
		Simple            bool            `json:"simple"`
		JSON              json.RawMessage `json:"json"`
		RawName           string          `json:"rawName"`
		Provider          string          `json:"provider"`
		Type              string          `json:"type"`
		StateID           string          `json:"stateId"`
		AttributesNil     bool            `json:"attributesNil"`
		AttributesLen     int             `json:"attributesLen"`
		AttributesShared  bool            `json:"attributesShared"`
		FreshMapEveryCall bool            `json:"freshMapEveryCall"`
	}
	var got []result
	for _, c := range cases {
		var r Resource
		if c.simple {
			r = RecordBare(c.id, c.resourceName, c.typ, c.prov)
		} else {
			r = Record(c.id, c.resourceName, c.typ, c.prov, c.attributes)
		}
		content, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		res := result{
			Case:          c.name,
			Simple:        c.simple,
			JSON:          content,
			RawName:       r.RawName,
			Provider:      r.Provider,
			Type:          r.Address.Type,
			StateID:       r.Observed.ID,
			AttributesNil: r.Observed.Attributes == nil,
			AttributesLen: len(r.Observed.Attributes),
		}
		if !c.simple && c.attributes != nil {
			c.attributes["golden-probe"] = "x"
			res.AttributesShared = r.Observed.Attributes["golden-probe"] == "x"
			delete(c.attributes, "golden-probe")
		}
		if c.simple {
			other := RecordBare(c.id, c.resourceName, c.typ, c.prov)
			r.Observed.Attributes["golden-probe"] = "x"
			res.FreshMapEveryCall = other.Observed.Attributes["golden-probe"] == ""
		}
		got = append(got, res)
	}
	goldenCheck(t, "record", got)
}

func TestGoldenAttributeTags(t *testing.T) {
	cases := []struct {
		name       string
		attributes map[string]string
	}{
		{"nil", nil},
		{"empty", map[string]string{}},
		{"no tags", map[string]string{"id": "x", "name": "y"}},
		{"mixed", map[string]string{
			"id": "vpc-1", "tags.%": "2", "tags.Name": "main", "tags.aws:cloudformation:stack-id": "arn:stack",
			"labels.team": "core", "tag.#": "1", "tags.#": "1", "tags.0.key": "listed",
		}},
		{"count only", map[string]string{"tags.%": "0"}},
		{"labels count", map[string]string{"labels.%": "1", "labels.env": "prod"}},
		{"empty key", map[string]string{"tags.": "v"}},
		{"empty value", map[string]string{"tags.Name": ""}},
		{"bare index", map[string]string{"tags.0": "v"}},
		{"list of blocks", map[string]string{"tags.#": "2", "tags.0.key": "a", "tags.0.value": "1", "tags.10.key": "b", "tags.0.": "c"}},
		{"dotted key", map[string]string{"tags.a.b": "v"}},
		{"index-like key", map[string]string{"tags.1a.b": "v", "tags..x": "w", "tags.-1.x": "z"}},
		{"non-ascii digit", map[string]string{"tags.٣.x": "v"}},
		{"percent and hash in key", map[string]string{"tags.%x": "v", "tags.#x": "w", "tags.x%": "y"}},
		{"nested prefixes", map[string]string{"tags.labels.x": "v", "labels.tags.y": "w"}},
		{"near misses", map[string]string{"Tags.x": "v", "tag.x": "v", "tagsx": "v", "labelsx": "v", "label.x": "v", "xtags.y": "v"}},
		{"unicode", map[string]string{"tags.名前": "値"}},
	}
	type result struct {
		Case string            `json:"case"`
		Tags map[string]string `json:"tags"`
	}
	got := make([]result, 0, len(cases))
	for _, c := range cases {
		got = append(got, result{Case: c.name, Tags: AttributeTags(c.attributes)})
	}
	goldenCheck(t, "attribute_tags", got)
}

type goldenFilterView struct {
	Service string   `json:"service"`
	Path    string   `json:"path"`
	Values  []string `json:"values"`
}

func TestGoldenParseFilters(t *testing.T) {
	inputs := map[string][]string{
		"id": {
			"aws_vpc=myid", "vpc=a,b", "vpc=,a", "vpc=a,,b,", "resource=id1,project:dataset_id",
			"vpc=", "vpc=,", "=myid", "=", "vpc", "", "a=b=c", "a b=c d", "vpc='a'", "vpc=a:b",
		},
		"tag": {
			"env=prod", "env=prod,dev", "env=", "env=,", "env", "Name=a=b", "=prod", "=", "",
			"team=web data", "k.v=x", "名前=値",
		},
		"where": {
			"cidr_block=10.0.0.0/16", "tags.env=prod", "labels.env", "id=vpc-1", "a.b.c=x,y",
			"=x", "", "x=", "x=a=b",
		},
	}
	parsers := map[string]func(string) (Filter, error){"id": ParseIDFilter, "tag": ParseTagFilter, "where": ParseWhereFilter}
	type result struct {
		Flag   string            `json:"flag"`
		Input  string            `json:"input"`
		Filter *goldenFilterView `json:"filter,omitempty"`
		Error  string            `json:"error,omitempty"`
	}
	var got []result
	for _, flag := range []string{"id", "tag", "where"} {
		for _, in := range inputs[flag] {
			r := result{Flag: flag, Input: in}
			f, err := parsers[flag](in)
			if err != nil {
				r.Error = err.Error()
			} else {
				view := goldenFilterView(f)
				r.Filter = &view
			}
			got = append(got, r)
		}
	}
	goldenCheck(t, "parse_filters", got)
}

func TestGoldenFilter(t *testing.T) {
	vpc1 := Record("vpc-1", "main", "aws_vpc", "aws", map[string]string{
		"id": "vpc-1", "tags.Name": "main", "tags.Env": "prod", "cidr_block": "10.0.0.0/16", "empty": "",
	})
	vpc2 := RecordBare("vpc-2", "other", "aws_vpc", "aws")
	bare := Record("vpc-3", "bare", "aws_vpc", "aws", nil)
	network := Record("net-1", "net", "google_compute_network", "google", map[string]string{"labels.env": "prod"})
	noProvider := RecordBare("x-1", "x", "type1", "")
	underscore := RecordBare("u-1", "u", "_type1", "")
	noPrefix := RecordBare("p-1", "p", "awsvpc", "aws")
	noAddress := Resource{Observed: &Observation{ID: "vpc-1"}, Provider: "aws"}
	noObservation := Resource{Address: &Address{Type: "aws_vpc"}, Provider: "aws"}

	cases := []struct {
		name     string
		filter   Filter
		resource Resource
	}{
		{"id match", Filter{Path: "id", Values: []string{"vpc-1"}}, vpc1},
		{"id mismatch", Filter{Path: "id", Values: []string{"vpc-1"}}, vpc2},
		{"id one of several", Filter{Path: "id", Values: []string{"a", "vpc-2", "b"}}, vpc2},
		{"id, nil values", Filter{Path: "id"}, vpc1},
		{"id, empty values", Filter{Path: "id", Values: []string{}}, vpc1},
		{"id is the state ID, not the address", Filter{Path: "id", Values: []string{"aws_vpc.r--main"}}, vpc1},
		{"id is not the name", Filter{Path: "id", Values: []string{"main"}}, vpc1},
		{"id is case-sensitive", Filter{Path: "id", Values: []string{"VPC-1"}}, vpc1},
		{"service without provider prefix applies", Filter{Service: "vpc", Path: "id", Values: []string{"vpc-9"}}, vpc1},
		{"service with provider prefix does not apply", Filter{Service: "aws_vpc", Path: "id", Values: []string{"vpc-9"}}, vpc1},
		{"other service does not apply", Filter{Service: "subnet", Path: "id", Values: []string{"vpc-9"}}, vpc1},
		{"service name is case-sensitive", Filter{Service: "VPC", Path: "id", Values: []string{"vpc-9"}}, vpc1},
		{"no provider, match", Filter{Service: "type1", Path: "id", Values: []string{"x-1"}}, noProvider},
		{"no provider, mismatch", Filter{Service: "type1", Path: "id", Values: []string{"zzz"}}, noProvider},
		{"no provider, leading underscore", Filter{Service: "type1", Path: "id", Values: []string{"zzz"}}, underscore},
		{"no underscore after provider", Filter{Service: "awsvpc", Path: "id", Values: []string{"zzz"}}, noPrefix},
		{"google service", Filter{Service: "compute_network", Path: "id", Values: []string{"zzz"}}, network},
		{"attribute present", Filter{Path: "tags.Name"}, vpc1},
		{"attribute absent", Filter{Path: "tags.Name"}, vpc2},
		{"attribute, nil attributes", Filter{Path: "tags.Name"}, bare},
		{"attribute present, empty value", Filter{Path: "empty"}, vpc1},
		{"attribute value match", Filter{Path: "tags.Env", Values: []string{"prod"}}, vpc1},
		{"attribute value mismatch", Filter{Path: "tags.Env", Values: []string{"dev"}}, vpc1},
		{"attribute value one of several", Filter{Path: "tags.Env", Values: []string{"dev", "prod"}}, vpc1},
		{"attribute empty value match", Filter{Path: "empty", Values: []string{""}}, vpc1},
		{"attribute, empty values", Filter{Path: "tags.Name", Values: []string{}}, vpc1},
		{"attribute missing with values", Filter{Path: "tags.Missing", Values: []string{"x"}}, vpc1},
		{"attribute id key is not the id filter", Filter{Path: "ID", Values: []string{"vpc-1"}}, vpc1},
		{"attribute prefix only", Filter{Path: "tags"}, vpc1},
		{"empty field path", Filter{Path: ""}, vpc1},
		{"labels", Filter{Path: "labels.env", Values: []string{"prod"}}, network},
		{"attribute filter on another service", Filter{Service: "subnet", Path: "tags.Name", Values: []string{"nope"}}, vpc1},
		{"nil Address", Filter{Path: "id", Values: []string{"vpc-1"}}, noAddress},
		{"nil Address, not applicable", Filter{Service: "subnet", Path: "id", Values: []string{"vpc-1"}}, noAddress},
		{"nil Observed, applicable id filter", Filter{Path: "id", Values: []string{"x"}}, noObservation},
		{"nil Observed, applicable attribute filter", Filter{Path: "tags.Name"}, noObservation},
		{"nil Observed, not applicable", Filter{Service: "subnet", Path: "id", Values: []string{"x"}}, noObservation},
	}
	type filterResult struct {
		Case     string `json:"case"`
		Passes   bool   `json:"passes"`
		Panicked bool   `json:"panicked"`
	}
	var filtered []filterResult
	for _, c := range cases {
		f := c.filter
		var passes bool
		panicked := goldenPanics(func() { passes = f.Keeps(c.resource) })
		filtered = append(filtered, filterResult{Case: c.name, Passes: passes, Panicked: panicked})
	}

	applicable := []struct{ service, name string }{
		{"", "vpc"}, {"", ""}, {"vpc", "vpc"}, {"vpc", "subnet"}, {"vpc", ""}, {"VPC", "vpc"}, {"aws_vpc", "vpc"}, {"vpc", "aws_vpc"},
	}
	type applicableResult struct {
		Service    string `json:"service"`
		Name       string `json:"name"`
		Applicable bool   `json:"applicable"`
	}
	var applicability []applicableResult
	for _, a := range applicable {
		f := Filter{Service: a.service, Path: "id"}
		applicability = append(applicability, applicableResult{Service: a.service, Name: a.name, Applicable: f.AppliesTo(a.name)})
	}

	goldenCheck(t, "filter", map[string]any{"keeps": filtered, "appliesTo": applicability})
}

type goldenResourceView struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	StateID string `json:"stateId"`
}

func goldenResourceViews(resources []Resource) []goldenResourceView {
	if resources == nil {
		return nil
	}
	views := make([]goldenResourceView, 0, len(resources))
	for _, r := range resources {
		views = append(views, goldenResourceView{Type: r.Address.Type, Name: r.RawName, StateID: r.Observed.ID})
	}
	return views
}

func TestGoldenPrune(t *testing.T) {
	r := func(typ, name, stateID string) Resource {
		return Resource{Address: &Address{Type: typ}, Observed: &Observation{ID: stateID}, RawName: name, Provider: "aws"}
	}
	vpc1 := r("aws_vpc", "vpc-1", "vpc-1")
	vpc2 := r("aws_vpc", "vpc-2", "vpc-2")
	vpc3 := r("aws_vpc", "vpc-3", "vpc-3")
	subnet1 := r("aws_subnet", "subnet-1", "subnet-1")
	several := []Resource{vpc1, subnet1, vpc2, vpc3, vpc1}

	id := func(service string, ids ...string) Filter { return Filter{Service: service, Path: "id", Values: ids} }
	cases := []struct {
		name      string
		resources []Resource
		filters   []Filter
	}{
		{name: "no filters drops duplicates", resources: several},
		{name: "empty filter slice drops duplicates", resources: several, filters: []Filter{}},
		{name: "nil resources, no filters", resources: nil},
		{name: "nil resources, a filter", resources: nil, filters: []Filter{id("vpc", "vpc-1")}},
		{name: "empty resources, a filter", resources: []Resource{}, filters: []Filter{id("vpc", "vpc-1")}},
		{name: "id filter", resources: several, filters: []Filter{id("vpc", "vpc-2")}},
		{name: "id filter with several ids", resources: several, filters: []Filter{id("vpc", "vpc-1", "vpc-3")}},
		{name: "two id filters on one service", resources: several, filters: []Filter{id("vpc", "vpc-1"), id("vpc", "vpc-2")}},
		{name: "id filters on two services", resources: several, filters: []Filter{id("vpc", "vpc-1"), id("subnet", "subnet-9")}},
		{name: "attribute filter only removes duplicates", resources: several, filters: []Filter{{Path: "tags.Name", Values: []string{"x"}}}},
		{name: "attribute and id filters", resources: several, filters: []Filter{{Service: "vpc", Path: "tags.Name", Values: []string{"x"}}, id("vpc", "vpc-3")}},
		{name: "filter without ids drops the service", resources: several, filters: []Filter{id("vpc")}},
		{name: "full type name is not applied", resources: several, filters: []Filter{id("aws_vpc", "vpc-1")}},
		{name: "unscoped id filter", resources: several, filters: []Filter{id("", "vpc-1", "subnet-1")}},
		{name: "same name, other state ID", resources: []Resource{
			r("aws_vpc", "same", "vpc-a"), r("aws_vpc", "same", "vpc-b"),
		}, filters: []Filter{id("subnet", "x")}},
		{name: "first copy filtered out, second kept", resources: []Resource{
			r("aws_vpc", "same", "vpc-a"), r("aws_vpc", "same", "vpc-b"),
		}, filters: []Filter{id("vpc", "vpc-b")}},
		{name: "types sharing an ID", resources: []Resource{
			r("aws_iam_role", "admins", "admins"), r("aws_iam_group", "admins", "admins"), r("aws_iam_group", "admins", "admins"),
		}, filters: []Filter{id("iam_user", "other")}},
		{name: "same state ID, other names", resources: []Resource{
			r("aws_vpc", "a", "vpc-1"), r("aws_vpc", "b", "vpc-1"),
		}, filters: []Filter{id("vpc", "vpc-1")}},
		{name: "everything filtered out", resources: several, filters: []Filter{id("vpc", "none"), id("subnet", "none")}},
	}

	type result struct {
		Case   string               `json:"case"`
		Pruned []goldenResourceView `json:"prune"`
	}
	got := make([]result, 0, len(cases))
	for _, c := range cases {
		var copied []Resource
		if c.resources != nil {
			copied = make([]Resource, len(c.resources))
			copy(copied, c.resources)
		}
		got = append(got, result{Case: c.name, Pruned: goldenResourceViews(Prune(copied, c.filters))})
	}
	goldenCheck(t, "prune", got)
}

func TestGoldenWalk(t *testing.T) {
	cases := []struct {
		name string
		path string
		data any
	}{
		{"nil data, empty path", "", nil},
		{"nil data, path", "a", nil},
		{"string, empty path", "", "v"},
		{"string, path", "a", "v"},
		{"empty string, empty path", "", ""},
		{"named string type, empty path", "", goldenString("v")},
		{"int, empty path", "", 5},
		{"bool, empty path", "", true},
		{"float leaf", "a", map[string]any{"a": 1.5}},
		{"int leaf", "a", map[string]any{"a": 1}},
		{"empty map", "a", map[string]any{}},
		{"empty nested typed map", "a.b", map[string]map[string]any{}},
		{"nil typed map", "a", map[string]any(nil)},
		{"top-level key", "a", map[string]any{"a": "v", "b": "w"}},
		{"string map", "b", map[string]string{"a": "v", "b": "w"}},
		{"missing key", "c", map[string]any{"a": "v"}},
		{"key is case-sensitive", "A", map[string]any{"a": "v"}},
		{"nested", "a.b", map[string]any{"a": map[string]any{"b": "v", "c": "w"}}},
		{"nested typed", "a.b", map[string]map[string]any{"a": {"b": "v"}}},
		{"nested string map", "a.b", map[string]any{"a": map[string]string{"b": "v"}}},
		{"three levels", "a.b.c", map[string]any{"a": map[string]any{"b": map[string]any{"c": "v"}}}},
		{"path ends on a map", "a.b", map[string]any{"a": map[string]any{"b": map[string]any{"c": "v"}}}},
		{"path ends on a map at the top", "a", map[string]any{"a": map[string]any{"c": "v"}}},
		{"path past a leaf", "a.b", map[string]any{"a": "v"}},
		{"nested missing", "a.c", map[string]any{"a": map[string]any{"b": "v"}}},
		{"dotted key at the top", "a.b", map[string]any{"a.b": "v"}},
		{"dotted key nested", "a.b.c", map[string]any{"a": map[string]any{"b.c": "v"}}},
		{"dotted key with three parts", "a.b.c", map[string]any{"a.b.c": "v"}},
		{"flattened attributes", "tags.Name", map[string]string{"id": "i-1", "tags.Name": "web", "tags.%": "1"}},
		{"prefix of a dotted key", "tags", map[string]string{"tags.Name": "web"}},
		{"repeated segment", "a.b", map[string]any{"a": map[string]any{"a": map[string]any{"b": "v"}}}},
		{"repeated segment, three deep", "a.b", map[string]any{"a": map[string]any{"a": map[string]any{"a": map[string]any{"b": "v"}}}}},
		{"skipped level does not match", "a.b", map[string]any{"a": map[string]any{"z": map[string]any{"b": "v"}}}},
		{"empty key under the path", "a", map[string]any{"a": map[string]any{"": "v"}}},
		{"empty key, empty path", "", map[string]any{"": "v"}},
		{"map, empty path", "", map[string]any{"x": "v"}},
		{"trailing dot", "a.", map[string]any{"a": "v"}},
		{"leading dot", ".a", map[string]any{"a": "v"}},
		{"double dot", "a..b", map[string]any{"a": map[string]any{"": map[string]any{"b": "v"}}}},
		{"array of strings", "a", map[string]any{"a": []any{"x", "y"}}},
		{"array of strings, path past them", "a.b", map[string]any{"a": []any{"x", "y"}}},
		{"empty array", "a", map[string]any{"a": []any{}}},
		{"typed string slice", "a", map[string]any{"a": []string{"x"}}},
		{"typed map of arrays", "a", map[string][]any{"a": {"x"}}},
		{"array of maps", "a.b", map[string]any{"a": []any{map[string]any{"b": "1"}, map[string]any{"b": "2"}, map[string]any{"c": "3"}}}},
		{"array of maps, none match", "a.b", map[string]any{"a": []any{map[string]any{"c": "3"}}}},
		{"array of maps holding arrays", "a.b", map[string]any{"a": []any{map[string]any{"b": []any{"1", "2"}}, "s", map[string]any{"b": "3"}}}},
		{"array of typed maps", "a.b", map[string]any{"a": []any{map[string]string{"b": "1"}, map[string]string{"b": "2"}}}},
		{"top-level array, empty path", "", []any{"x", map[string]any{"a": "y"}, 7}},
		{"top-level array, path", "a", []any{"x", map[string]any{"a": "y"}, map[string]any{"a": "z"}}},
		{"array of nil", "", []any{nil}},
		{"array of nil and a string", "", []any{nil, "x"}},
		{"array of nil, path", "a", []any{nil}},
		{"nested arrays", "", []any{[]any{"x", []any{"y"}}, "z"}},
		{"nil array", "", []any(nil)},
		{"empty top-level array", "a", []any{}},
		{"nil under the key", "a", map[string]any{"a": nil}},
		{"nil under the key, more path", "a.b", map[string]any{"a": nil}},
		{"pointer to a map", "a", &map[string]any{"a": "v"}},
		{"struct", "A", struct{ A string }{A: "v"}},
	}
	type result struct {
		Case       string   `json:"case"`
		Path       string   `json:"path"`
		Data       any      `json:"data"`
		Values     []any    `json:"values"`
		ValueTypes []string `json:"valueTypes"`
		Found      bool     `json:"found"`
	}
	got := make([]result, 0, len(cases))
	for _, c := range cases {
		values := valuesAt(c.path, c.data)
		var types []string
		for _, v := range values {
			types = append(types, fmt.Sprintf("%T", v))
		}
		got = append(got, result{Case: c.name, Path: c.path, Data: c.data, Values: values, ValueTypes: types, Found: hasPath(c.path, c.data)})
	}
	goldenCheck(t, "walk", got)
}

func TestGoldenListerAccessors(t *testing.T) {
	type check struct {
		Check string `json:"check"`
		Value any    `json:"value"`
	}
	var got []check
	record := func(name string, value any) { got = append(got, check{Check: name, Value: value}) }

	var zero ListerOf[string]
	record("zero: Name", zero.Name())
	record("zero: Provider", zero.Provider())
	record("zero: Filters is nil", zero.Filters() == nil)
	record("zero: Verbose", zero.Verbose())
	record("zero: Wants", zero.Wants("aws_vpc"))
	record("zero: WantsAny", zero.WantsAny("aws_vpc", "aws_subnet"))
	record("zero: WantsAny of nothing", zero.WantsAny())
	record("zero: Context is Background", zero.Context() == context.Background())
	record("zero: Context has no Done channel", zero.Context().Done() == nil)
	record("zero: Resources is nil", zero.Resources == nil)
	record("zero: Settings", zero.Settings())

	ctx := context.WithValue(context.Background(), goldenCtxKey{}, "set")
	filters := []Filter{{Service: "vpc", Path: "id", Values: []string{"vpc-1"}}}
	settings := Settings{
		Service: "ec2", Provider: "aws", Verbose: true, Filters: filters,
		Unwanted: func(typ string) bool { return typ == "aws_iam_access_key" },
	}
	var s ListerOf[string]
	s.SetSettings("eu-west-1")
	listed := []Resource{RecordBare("vpc-1", "vpc-1", "aws_vpc", "aws")}
	listErr := errors.New("listing failed")
	resources, err := s.Run(ctx, settings, func() error {
		record("run: Name", s.Name())
		record("run: Provider", s.Provider())
		record("run: Verbose", s.Verbose())
		record("run: Filters", s.Filters())
		record("run: Context value", s.Context().Value(goldenCtxKey{}))
		record("run: Context is the one given", s.Context() == ctx)
		record("run: Wants a wanted type", s.Wants("aws_vpc"))
		record("run: Wants an unwanted type", s.Wants("aws_iam_access_key"))
		record("run: WantsAny with a wanted type", s.WantsAny("aws_iam_access_key", "aws_vpc"))
		record("run: WantsAny of unwanted types only", s.WantsAny("aws_iam_access_key"))
		record("run: typed settings", s.Settings())
		s.Resources = append(s.Resources, listed...)
		return listErr
	})
	record("run: returns the listed resources", goldenResourceViews(resources))
	record("run: returns the list's error", err == listErr)

	goldenCheck(t, "lister_accessors", got)
}
