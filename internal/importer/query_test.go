// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// queryProvider is a provider with the query service, whose vpc service
// lists fake_vpc.
type queryProvider struct {
	fakeProvider
	lister harvest.Lister
}

func (*queryProvider) Services() map[string]harvest.Lister {
	return map[string]harvest.Lister{"vpc": nil, harvest.QueryService: &harvest.QueryLister{}}
}

func (*queryProvider) Profile() harvest.Profile {
	return harvest.Profile{OptInServices: []string{harvest.QueryService}, ListedTypes: map[string]string{"fake_vpc": "vpc"}}
}

func (*queryProvider) ProviderBlock() map[string]interface{} {
	return map[string]interface{}{"provider": map[string]interface{}{"fake": map[string]interface{}{"region": "eu-west-2"}}}
}

func (p *queryProvider) Selected() harvest.Lister { return p.lister }

func (p *queryProvider) Select(name string, verbose bool) error {
	p.lister = &harvest.QueryLister{}
	p.lister.Configure(harvest.ListerConfig{Service: name, Provider: "fake", Verbose: verbose})
	return nil
}

// The query service needs --query-types, and --query-types the query
// service; OpenTofu, which has no terraform query, is refused, as are
// types of another provider and those a service of infraharvest's lists,
// which the error names.
func TestCheckQuery(t *testing.T) {
	t.Parallel()
	query := []string{"vpc", harvest.QueryService}
	for name, tc := range map[string]struct {
		options Options
		want    string
	}{
		"listed":              {Options{All: true, Services: query, QueryTypes: []string{"fake_volume"}}, ""},
		"not used":            {Options{All: true, Services: []string{"vpc"}}, ""},
		"types without query": {Options{All: true, Services: []string{"vpc"}, QueryTypes: []string{"fake_volume"}}, "--query-types names the types the query service lists: add query to --services"},
		"query skipped":       {Options{All: true, Services: query, Skip: []string{harvest.QueryService}, QueryTypes: []string{"fake_volume"}}, "add query to --services"},
		"query without types": {Options{All: true, Services: query}, "the query service lists the resource types --query-types names, such as fake_...: name at least one"},
		"tofu":                {Options{All: true, Services: query, QueryTypes: []string{"fake_volume"}, Engine: EngineTofu}, "which OpenTofu doesn't have: use --engine terraform"},
		"another provider's":  {Options{All: true, Services: query, QueryTypes: []string{"aws_ebs_volume"}}, "--query-types: aws_ebs_volume isn't a resource type of fake"},
		"listed by a service": {Options{All: true, Services: query, QueryTypes: []string{"fake_volume", "fake_vpc"}}, "--query-types: fake_vpc is listed by the vpc service: use --services vpc"},
		"wildcard leaves it":  {Options{All: true, Services: []string{"*"}, QueryTypes: []string{"fake_volume"}}, "add query to --services"},
		"wildcard and query":  {Options{All: true, Services: []string{"*", harvest.QueryService}, QueryTypes: []string{"fake_volume"}}, ""},
	} {
		err := CheckOptions(&queryProvider{}, tc.options)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
}

// The query service's lister lists with the query the call gives its
// provider, and the other services list as they do.
func TestListServicesGivesTheQueryServiceItsQuery(t *testing.T) {
	t.Parallel()
	registry := harvest.NewRegistry(&queryProvider{})
	options := Options{Services: []string{harvest.QueryService}, QueryTypes: []string{"fake_volume"}}
	c := callOf(options, "")
	volume := harvest.RecordIdentity(map[string]string{"id": "vol-1", "region": "eu-west-2"}, "vol-1", "fake_volume", "fake")
	var asked harvest.Provider
	c.query = func(p harvest.Provider) harvest.Query {
		asked = p
		return func(context.Context) ([]harvest.Resource, error) { return []harvest.Resource{volume}, nil }
	}
	failures, err := listServices(t.Context(), registry, c, 1, nil)
	if err != nil || len(failures) > 0 {
		t.Fatalf("%v, %v", err, failures)
	}
	got := registry.ByService()[harvest.QueryService]
	if !reflect.DeepEqual(got, []harvest.Resource{volume}) {
		t.Errorf("listed %+v", got)
	}
	if asked == nil || !registry.Providers[asked] {
		t.Error("the query isn't the query service's own provider's")
	}
}

// The query lists with Terraform at 1.14 or newer, at --terraform-path
// if set, as the toolchain then generates with too.
func TestQueryFindsTerraformWithQuery(t *testing.T) {
	t.Parallel()
	var minimum, explicit string
	r := NewRun()
	r.tools = &callTools{findBinary: func(_ context.Context, b engine.Binary, path, _ string) (string, error) {
		minimum, explicit = b.Minimum.String(), path
		return "", errStub
	}}
	options := Options{Services: []string{harvest.QueryService}, QueryTypes: []string{"fake_volume"}, TerraformPath: "/opt/terraform", Out: t.TempDir()}
	if _, err := r.query(callOf(options, ""), &queryProvider{})(t.Context()); err != errStub {
		t.Fatalf("got %v", err)
	}
	if minimum != "1.14.0" || explicit != "/opt/terraform" {
		t.Errorf("found Terraform %s at %q", minimum, explicit)
	}
	if got := engineBinaryOf(options).Minimum.String(); got != "1.14.0" {
		t.Errorf("generates with Terraform %s", got)
	}
	if got := engineBinaryOf(Options{Services: []string{"vpc"}}).Minimum.String(); got != "1.5.0" {
		t.Errorf("without the query service, generates with Terraform %s", got)
	}
}

// A resource Terraform's list resources returned is imported by its
// identity, told by the identity's ID whatever the catalogue says of its
// type, and has no fallback; discover's inventory keeps its identity.
func TestImportBlocksByIdentity(t *testing.T) {
	t.Parallel()
	identity := map[string]string{"account_id": "123456789012", "name": "/app", "region": "eu-west-2"}
	r := harvest.RecordIdentity(identity, "/app", "aws_cloudwatch_log_group", "aws")
	cat := catalogue.MustBuiltin("aws")
	importID := importIDFunc(cat)
	id, ok := importID(r)
	if !ok || id != "account_id=123456789012,name=/app,region=eu-west-2" {
		t.Fatalf("import ID %q, %v", id, ok)
	}
	blocks := importBlocks{provider: "aws", cat: cat, importID: importID, nameOf: rawName, fileOf: noFile}
	imp, ok := blocks.of(harvest.QueryService, r)
	if !ok || !reflect.DeepEqual(imp.Identity, identity) || imp.ID != id || imp.Fallback != "" {
		t.Errorf("import %+v, %v", imp, ok)
	}

	listed := listing{resources: map[string][]harvest.Resource{harvest.QueryService: {r}}}
	back := listingOf(listed.records(), []string{harvest.QueryService}).resources[harvest.QueryService]
	if len(back) != 1 || !reflect.DeepEqual(back[0].Observed.Identity, identity) || back[0].Observed.ID != id {
		t.Errorf("from the inventory: %+v", back)
	}
}

// What Terraform's list resources returned isn't asked for children: the
// provider finds them by an import ID it doesn't have.
func TestChildImportsSkipsIdentities(t *testing.T) {
	t.Parallel()
	r := harvest.RecordIdentity(map[string]string{"name": "lone"}, "lone", "fake_volume", "fake")
	got, _, failures := withChildImports(t.Context(), &childrenProvider{}, map[string][]harvest.Resource{harvest.QueryService: {r}}, 1)
	if len(failures) > 0 || len(got[harvest.QueryService]) != 1 {
		t.Errorf("got %v, %v", got, failures)
	}
}

// What the query service listed of a type another service of the call
// listed too, under another name of the type as well, is left out; the
// rest is kept.
func TestLeaveOutQueryOverlap(t *testing.T) {
	t.Parallel()
	vm := harvest.RecordIdentity(map[string]string{"name": "vm"}, "vm", "azurerm_linux_virtual_machine", "azurerm")
	key := harvest.RecordIdentity(map[string]string{"name": "key"}, "key", "azurerm_storage_account_customer_managed_key", "azurerm")
	resources := map[string][]harvest.Resource{
		"resource_graph":     {harvest.RecordBare("/subscriptions/s/vm", "vm", "azurerm_virtual_machine", "azurerm")},
		harvest.QueryService: {vm, key},
	}
	leaveOutQueryOverlap("azurerm", resources)
	if got := resources[harvest.QueryService]; !reflect.DeepEqual(got, []harvest.Resource{key}) {
		t.Errorf("kept %+v", got)
	}
	if len(resources["resource_graph"]) != 1 {
		t.Error("the other service lost what it listed")
	}
}
