// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/tfquery"
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
	c.query = func(p harvest.Provider, _ []harvest.Resource) harvest.Query {
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
	r.tools = &callTools{findBinary: func(_ context.Context, b tfrun.Binary, path, _ string) (string, error) {
		minimum, explicit = b.Minimum.String(), path
		return "", errStub
	}}
	options := Options{Services: []string{harvest.QueryService}, QueryTypes: []string{"fake_volume"}, TerraformPath: "/opt/terraform", Out: t.TempDir()}
	if _, err := r.query(callOf(options, ""), &queryProvider{}, nil)(t.Context()); err != errStub {
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

// The query lists with the provider release the call's roots pin, which
// the toolchain then takes as it is, rather than the newest: listing and
// import run one release of the provider.
func TestQueryListsWithTheRootsProvider(t *testing.T) {
	t.Parallel()
	var pins int
	var listedWith engine.Provider
	r := NewRun()
	r.tools = &callTools{
		findBinary: func(context.Context, tfrun.Binary, string, string) (string, error) { return "/opt/terraform", nil },
		rootConfig: func(_ context.Context, _, _ string, p engine.Provider, _ []moduleConstraint) (*rootFiles, error) {
			pins++
			p.Version = "~> 6.14"
			return &rootFiles{provider: p}, nil
		},
		query: func(_ context.Context, req tfquery.Request) ([]harvest.Resource, error) {
			listedWith = req.Provider
			return nil, nil
		},
	}
	options := Options{Services: []string{harvest.QueryService}, QueryTypes: []string{"fake_volume"}, Modules: ModulesNone, Out: t.TempDir()}
	c := callOf(options, "")
	if _, err := r.query(c, &queryProvider{}, nil)(t.Context()); err != nil {
		t.Fatal(err)
	}
	if listedWith.Version != "~> 6.14" || listedWith.Config["region"] != "eu-west-2" {
		t.Errorf("listed with %+v", listedWith)
	}
	tools, err := r.toolchain(t.Context(), &queryProvider{}, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pins != 1 || tools.root.provider.Version != "~> 6.14" {
		t.Errorf("pinned %d times, the roots at %q", pins, tools.root.provider.Version)
	}
}

// moduleQueryProvider is queryProvider with the curated modules of aws.
type moduleQueryProvider struct{ queryProvider }

func (*moduleQueryProvider) Profile() harvest.Profile {
	return harvest.Profile{OptInServices: []string{harvest.QueryService}, ListedTypes: map[string]string{"fake_vpc": "vpc"}, Modules: "aws", Source: "hashicorp/aws"}
}

// The query service and the toolchain resolve the roots' pin from the
// same modules, and calls run at once: each module's releases are looked
// up in the registry once for the command, a module the registry doesn't
// have too, and every call resolves what it would looking them up itself.
func TestModuleReleasesLookedUpOncePerCommand(t *testing.T) {
	t.Parallel()
	tested := map[string]string{}
	for _, a := range adapters.For("aws") {
		module, _, _ := strings.Cut(a.Source, "//")
		tested["/v1/modules/"+module+"/versions"] = a.Version
	}
	missing := "/v1/modules/terraform-aws-modules/s3-bucket/aws/versions"
	var mu sync.Mutex
	requests := map[string]int{}
	answer := func(w http.ResponseWriter, r *http.Request) {
		version, ok := tested[r.URL.Path]
		if !ok || r.URL.Path == missing {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"modules":[{"versions":[{"version":"99.0.0"},{"version":%q,"root":{"providers":[{"name":"aws","source":"hashicorp/aws","version":">= 6.0"}]}}]}]}`, version)
	}
	// The engine remembers the registry's answers by URL, so what the
	// calls should resolve comes from a registry of its own.
	reference := httptest.NewServer(http.HandlerFunc(answer))
	defer reference.Close()
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.URL.Path]++
		mu.Unlock()
		answer(w, r)
	}))
	defer registry.Close()
	lookUpIn := func(server *httptest.Server) func(context.Context, string) ([]engine.ModuleRelease, error) {
		return func(ctx context.Context, source string) ([]engine.ModuleRelease, error) {
			return engine.ModuleReleases(ctx, server.Client(), server.URL, source)
		}
	}
	provider := &moduleQueryProvider{}
	listed := []harvest.Resource{
		harvest.RecordBare("vpc-1", "main", "aws_vpc", "aws"),
		harvest.RecordBare("logs", "logs", "aws_s3_bucket", "aws"),
	}
	selected := map[string][]harvest.Resource{"vpc": listed}
	_, wantNewer, wantConstraints := resolveAdapters(t.Context(), lookUpIn(reference), applicableAdapters(adapters.For("aws"), selected), "hashicorp/aws", false)
	if len(wantConstraints) != 1 || len(wantNewer) != 1 {
		t.Fatalf("looking up without the memo: %+v, %+v", wantNewer, wantConstraints)
	}

	var pinned [][]moduleConstraint
	r := NewRun()
	r.tools = &callTools{
		findBinary:     func(context.Context, tfrun.Binary, string, string) (string, error) { return "/opt/terraform", nil },
		moduleReleases: lookUpIn(registry),
		rootConfig: func(_ context.Context, _, _ string, p engine.Provider, constraints []moduleConstraint) (*rootFiles, error) {
			mu.Lock()
			pinned = append(pinned, constraints)
			mu.Unlock()
			return &rootFiles{provider: p}, nil
		},
		query: func(context.Context, tfquery.Request) ([]harvest.Resource, error) { return nil, nil },
	}
	options := Options{Services: []string{"vpc", harvest.QueryService}, QueryTypes: []string{"fake_volume"}, Modules: ModulesRegistry, Out: t.TempDir()}
	const calls = 4
	newer := make([][]report.ModuleVersion, calls)
	errs := make([]error, calls)
	var wg sync.WaitGroup
	for i := range calls {
		wg.Go(func() {
			c := callOf(options, "")
			if _, errs[i] = r.query(c, provider, listed)(t.Context()); errs[i] != nil {
				return
			}
			var tools *toolchain
			if tools, errs[i] = r.toolchain(t.Context(), provider, c, selected); errs[i] == nil {
				newer[i] = tools.newer
			}
		})
	}
	wg.Wait()

	for i := range calls {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if !reflect.DeepEqual(newer[i], wantNewer) {
			t.Errorf("call %d: newer releases %+v, want %+v", i, newer[i], wantNewer)
		}
	}
	if len(pinned) != calls {
		t.Errorf("pinned %d times for %d calls", len(pinned), calls)
	}
	for _, constraints := range pinned {
		if got, want := fmt.Sprint(constraintStrings(constraints)), fmt.Sprint(constraintStrings(wantConstraints)); got != want {
			t.Errorf("pinned with %s, want %s", got, want)
		}
	}
	if len(requests) != 2 {
		t.Errorf("looked up %v", requests)
	}
	for path, n := range requests {
		if n != 1 {
			t.Errorf("%s: looked up %d times", path, n)
		}
	}
}

// constraintStrings are the module versions of constraints and what they
// accept, to compare.
func constraintStrings(constraints []moduleConstraint) []string {
	var s []string
	for _, c := range constraints {
		s = append(s, c.module+" "+c.version+" "+c.constraint.String())
	}
	return s
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
