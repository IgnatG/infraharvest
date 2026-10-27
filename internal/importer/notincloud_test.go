// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	azureprovider "github.com/IgnatG/infraharvest/internal/providers/azure"
	googleprovider "github.com/IgnatG/infraharvest/internal/providers/google"
	"github.com/IgnatG/infraharvest/internal/report"
)

// partialProvider is a provider that lists types in part (see
// harvest.ProviderWithPartialTypes).
type partialProvider struct {
	harvest.Provider
	partial []string
}

func (p *partialProvider) PartialTypes() []string { return p.partial }

// sqsListing is a listing of one queue, orders.
func sqsListing() listing {
	return listing{resources: map[string][]harvest.Resource{
		"sqs": {harvest.RecordBare("https://sqs.eu-west-2.amazonaws.com/123/orders", "orders", "aws_sqs_queue", "aws")},
	}}
}

// A listing tells what is gone only where it listed all there is, in an
// account and region whose roots are their own: not when the provider
// can't name them, when the layout puts several in one root, when the
// resources come from what discover saved, when filters or Resource
// Explorer's index left some out, or when the listing failed in part.
// Failing to read tags leaves nothing out.
func TestListedInScopeOnlyWhereTheListingIsComplete(t *testing.T) {
	t.Parallel()
	cat := catalogue.MustBuiltin("aws")
	importID := importIDFunc(cat)
	base := Options{Services: []string{"sqs"}}
	saved := sqsListing()
	saved.saved = true
	for _, tc := range []struct {
		name     string
		options  Options
		pattern  string
		scope    string
		listed   listing
		failures []error
		why      string
	}{
		{name: "no scope", options: base, why: "can't say which account and region"},
		{name: "one root for every region", options: base, pattern: "{out}/{provider}/{account}/", scope: "aws/123/eu-west-2", why: "--layout"},
		{name: "one root for every account", options: base, pattern: "{out}/{provider}/{region}/", scope: "aws/123/eu-west-2", why: "--layout"},
		{name: "saved inventory", options: base, scope: "aws/123/eu-west-2", listed: saved, why: "--reuse-inventory"},
		{name: "--id", options: Options{Services: base.Services, IDFilters: []string{"aws_sqs_queue=orders"}}, scope: "aws/123/eu-west-2", why: "filtered"},
		{name: "--tag", options: Options{Services: base.Services, TagFilters: []string{"team=web"}}, scope: "aws/123/eu-west-2", why: "filtered"},
		{name: "--where", options: Options{Services: base.Services, WhereFilters: []string{"aws_sqs_queue.name=orders"}}, scope: "aws/123/eu-west-2", why: "filtered"},
		{name: "--resource-explorer", options: Options{Services: base.Services, ProviderScope: fakeScope{partial: "--resource-explorer leaves out the services its index shows nothing of"}}, scope: "aws/123/eu-west-2", why: "--resource-explorer"},
		{name: "failed in part", options: base, scope: "aws/123/eu-west-2", failures: []error{errors.New("service sqs: access denied")}, why: "failed in part"},
		{name: "complete", options: base, scope: "aws/123/eu-west-2"},
		{name: "complete, with environments", options: base, pattern: envLayout, scope: "aws/123/eu-west-2"},
		{name: "complete, but for tags", options: base, scope: "aws/123/eu-west-2", failures: []error{fmt.Errorf("%s couldn't read them: access denied", tagsFailure)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			listed := tc.listed
			if listed.resources == nil {
				listed = sqsListing()
			}
			here, why := listedInScope(&fakeProvider{}, &callContext{options: tc.options, cat: cat, services: tc.options.Services, scope: tc.scope, importID: importID}, tc.pattern, listed, tc.failures, nil)
			switch {
			case tc.why == "" && here == nil:
				t.Fatalf("got nothing, %q; want what was listed", why)
			case tc.why != "" && (here != nil || !strings.Contains(why, tc.why)):
				t.Fatalf("got %+v, %q; want nothing, for %q", here, why, tc.why)
			case here != nil && (!here.types["aws_sqs_queue"] || !here.ids["https://sqs.eu-west-2.amazonaws.com/123/orders"]):
				t.Errorf("got %+v; want the queue, of a type listed in full", here)
			}
		})
	}
}

// The types listed in full are those of the services listed, by the
// catalogue, and of its inventory entries for a service that lists
// through an inventory API. Not the children of other types, which only
// what the selection keeps gets; the types an inventory API maps to one
// at a time, without an inventory entry; the types the import doesn't
// list or can't import; nor those the provider lists in part.
func TestTypesListedInFull(t *testing.T) {
	t.Parallel()
	aws := typesListedInFull(&fakeProvider{}, &callContext{cat: catalogue.MustBuiltin("aws"), services: []string{"sqs", "s3", "route_table"}})
	for typ, want := range map[string]bool{
		"aws_sqs_queue":                    true,
		"aws_s3_bucket":                    true,
		"aws_s3_bucket_versioning":         false, // a bucket's child
		"aws_route_table":                  true,
		"aws_main_route_table_association": false, // not importable
		"aws_vpc":                          false, // not listed
	} {
		if aws[typ] != want {
			t.Errorf("aws: %s listed in full: got %v, want %v", typ, aws[typ], want)
		}
	}
	if unwanted := typesListedInFull(&fakeProvider{}, &callContext{cat: catalogue.MustBuiltin("aws"), services: []string{"sqs"}, unwanted: func(typ string) bool { return typ == "aws_sqs_queue" }}); unwanted["aws_sqs_queue"] {
		t.Error("aws: a type the import doesn't list is listed in full")
	}
	if partial := typesListedInFull(&partialProvider{Provider: &fakeProvider{}, partial: []string{"aws_sqs_queue"}}, &callContext{cat: catalogue.MustBuiltin("aws"), services: []string{"sqs"}}); partial["aws_sqs_queue"] {
		t.Error("aws: a type the provider lists in part is listed in full")
	}

	google := catalogue.MustBuiltin("google")
	if got := typesListedInFull(&googleprovider.Provider{}, &callContext{cat: google, services: []string{"cloud_asset"}}); !got["google_compute_subnetwork"] || !got["google_compute_network"] {
		t.Errorf("google: the inventory entries aren't listed in full: %v", got)
	}
	if got := typesListedInFull(&partialProvider{Provider: &googleprovider.Provider{}, partial: []string{"google_compute_subnetwork"}}, &callContext{cat: google, services: []string{"cloud_asset"}}); got["google_compute_subnetwork"] || !got["google_compute_network"] {
		t.Errorf("google: a region's import lists subnetworks in full: %v", got)
	}
	if got := typesListedInFull(&azureprovider.Provider{}, &callContext{cat: catalogue.MustBuiltin("azurerm"), services: []string{"resource_graph"}}); got["azurerm_resource_group"] {
		t.Error("azurerm: a type Resource Graph maps through Resource Manager is listed in full")
	}
	// Azure's provider lists custom roles in part (see its PartialTypes):
	// one made assignable outside the run isn't listed, nor gone.
	roles := typesListedInFull(&partialProvider{Provider: &azureprovider.Provider{}, partial: []string{"azurerm_role_definition"}}, &callContext{cat: catalogue.MustBuiltin("azurerm"), services: []string{"role_assignment"}})
	if roles["azurerm_role_definition"] || !roles["azurerm_role_assignment"] {
		t.Errorf("azurerm: role_assignment lists in full %v, want its assignments and not its custom roles", roles)
	}
}

// What a root has with a resource block, of a type listed in full, that
// the listing didn't find is gone. A resource it found is not, by its
// import ID or its lister's ID, whatever the case, whether the selection
// keeps it or not; nor is one of a type not listed in full, or one the
// root has no resource block for, such as one in a module call or left
// out. Without a listing that can tell, nothing is.
func TestNotInCloud(t *testing.T) {
	t.Parallel()
	here := &listedHere{
		ids:   map[string]bool{"https://sqs.eu-west-2.amazonaws.com/123/orders": true, "/subscriptions/1/resourcegroups/app": true},
		types: map[string]bool{"aws_sqs_queue": true, "azurerm_resource_group": true},
	}
	existing := map[engine.External]string{
		{Type: "aws_sqs_queue", ID: "https://sqs.eu-west-2.amazonaws.com/123/orders"}: "aws_sqs_queue.orders",
		{Type: "aws_sqs_queue", ID: "https://sqs.eu-west-2.amazonaws.com/123/jobs"}:   "aws_sqs_queue.jobs",
		{Type: "aws_sqs_queue", ID: "https://sqs.eu-west-2.amazonaws.com/123/dlq"}:    "aws_sqs_queue.dlq",
		{Type: "aws_sqs_queue", ID: "https://sqs.eu-west-2.amazonaws.com/123/mod"}:    "",
		{Type: "aws_sns_topic", ID: "arn:aws:sns:eu-west-2:123:alerts"}:               "aws_sns_topic.alerts",
		{Type: "azurerm_resource_group", ID: "/subscriptions/1/resourceGroups/app"}:   "azurerm_resource_group.app",
	}

	got := here.notInCloud(existing)

	want := []engine.Import{
		{Type: "aws_sqs_queue", Name: "dlq", ID: "https://sqs.eu-west-2.amazonaws.com/123/dlq"},
		{Type: "aws_sqs_queue", Name: "jobs", ID: "https://sqs.eu-west-2.amazonaws.com/123/jobs"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if got := (*listedHere)(nil).notInCloud(existing); got != nil {
		t.Errorf("without a listing that can tell: got %+v, want nothing", got)
	}
}

// A resource the selection now leaves out, but the listing found, isn't
// gone: what is found counts before the selection decides.
func TestListedInScopeCountsWhatTheSelectionLeavesOut(t *testing.T) {
	t.Parallel()
	cat := catalogue.MustBuiltin("aws")
	listed := listing{resources: map[string][]harvest.Resource{"sqs": {
		harvest.RecordBare("https://sqs.eu-west-2.amazonaws.com/123/orders", "orders", "aws_sqs_queue", "aws"),
		harvest.RecordBare("https://sqs.eu-west-2.amazonaws.com/123/jobs", "jobs", "aws_sqs_queue", "aws"),
	}}}
	r := &Run{discovered: map[string]int{}, options: Options{Selection: "selection.yaml"}}
	selected, _ := r.selectResources(listed, sourcesOf(t, listed, map[string]string{"aws_sqs_queue https://sqs.eu-west-2.amazonaws.com/123/jobs": "excluded by default"}), nil, "aws/123/eu-west-2", "", importIDFunc(cat))

	here, why := listedInScope(&fakeProvider{}, &callContext{cat: cat, services: []string{"sqs"}, scope: "aws/123/eu-west-2", importID: importIDFunc(cat)}, "", listed, nil, selected)
	if here == nil {
		t.Fatal(why)
	}
	existing := map[engine.External]string{
		{Type: "aws_sqs_queue", ID: "https://sqs.eu-west-2.amazonaws.com/123/orders"}: "aws_sqs_queue.orders",
		{Type: "aws_sqs_queue", ID: "https://sqs.eu-west-2.amazonaws.com/123/jobs"}:   "aws_sqs_queue.jobs",
	}
	if got := here.notInCloud(existing); len(got) != 0 {
		t.Errorf("got %+v; want nothing gone", got)
	}
}

// An incremental import reports what the root has that is gone, with the
// root and the scope, even with nothing new to add, and changes nothing
// the root has.
func TestAddToRootReportsWhatIsNoLongerInTheCloud(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "123", "eu-west-2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"sqs.tf":               "resource \"aws_sqs_queue\" \"orders\" {\n  name = \"orders\"\n}\n\nresource \"aws_sqs_queue\" \"jobs\" {\n  name = \"jobs\"\n}\n",
		engine.ImportsFileName: "import {\n  to = aws_sqs_queue.orders\n  id = \"q/orders\"\n}\n\nimport {\n  to = aws_sqs_queue.jobs\n  id = \"q/jobs\"\n}\n",
		engine.ReadmeFileName:  "# Imported resources\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	previous := &engine.Result{Imported: []engine.Import{{Type: "aws_sqs_queue", Name: "orders", ID: "q/orders"}, {Type: "aws_sqs_queue", Name: "jobs", ID: "q/jobs"}}, Files: []string{"sqs.tf"}}
	if err := saveCheckpoint(out, dir, "fp", previous); err != nil {
		t.Fatal(err)
	}
	r := &Run{options: Options{Out: out}, scope: "aws/123/eu-west-2", here: &listedHere{ids: map[string]bool{"q/orders": true}, types: map[string]bool{"aws_sqs_queue": true}}}
	imports := []engine.Import{{Type: "aws_sqs_queue", Name: "orders", ID: "q/orders"}}

	added, result, holds, err := r.addToRoot(context.Background(), dir, imports, engine.Options{}, terraformRun{})

	if err != nil || added != nil || result != nil || holds != nil {
		t.Fatalf("want nothing new, got added=%v result=%v holds=%v err=%v", added, result, holds, err)
	}
	want := []report.NotInCloud{{Path: "aws/123/eu-west-2", Address: "aws_sqs_queue.jobs", ID: "q/jobs", Scope: "aws/123/eu-west-2"}}
	if !reflect.DeepEqual(r.report.NotInCloud, want) {
		t.Errorf("not in the cloud:\ngot  %+v\nwant %+v", r.report.NotInCloud, want)
	}
	for name, content := range files {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != content {
			t.Errorf("%s changed: %q, %v", name, got, err)
		}
	}
	if len(r.failures) != 0 {
		t.Errorf("what is gone is a failure: %v", r.failures)
	}
}
