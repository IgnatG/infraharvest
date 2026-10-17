// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

func TestImportsByDir(t *testing.T) {
	resources := map[string][]harvest.Resource{
		"sqs": {harvest.RecordBare("https://sqs/1/orders", "orders", "aws_sqs_queue", "aws")},
		"sns": {harvest.RecordBare("arn:aws:sns:topic", "alerts", "aws_sns_topic", "aws")},
		"s3":  nil, // a service with no resources gets no directory
	}

	t.Run("one directory per service", func(t *testing.T) {
		options := Options{Layout: "{out}/{provider}/{service}/", Out: "out"}

		got, _ := importsByDir("aws", "aws", options, resources, listerID, noEnv, rawName, noFile)

		want := map[string][]engine.Import{
			filepath.Join("out", "aws", "sqs"): {{Type: "aws_sqs_queue", Name: "orders", ID: "https://sqs/1/orders"}},
			filepath.Join("out", "aws", "sns"): {{Type: "aws_sns_topic", Name: "alerts", ID: "arn:aws:sns:topic"}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("one directory for a pattern without {service}", func(t *testing.T) {
		options := Options{Layout: "{out}/{provider}/", Out: "out"}

		got, _ := importsByDir("aws", "aws", options, resources, listerID, noEnv, rawName, noFile)

		if len(got) != 1 || len(got[filepath.Join("out", "aws")]) != 2 {
			t.Errorf("want both resources in out/aws, got %v", got)
		}
	})
}

func listerID(r harvest.Resource) (string, bool) { return r.Observed.ID, true }

// rootDir fills in every placeholder at once, so a value that holds one
// stays as it is, and leaves the others, such as {region}, alone.
func TestRootDir(t *testing.T) {
	got := rootDir("{out}/envs/{env}/{provider}/{service}/{region}/", rootValues{out: "out", provider: "aws", service: "{env}", env: "prod"})
	if want := filepath.Join("out", "envs", "prod", "aws", "{env}", "{region}"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Import blocks name the provider when the root declares it under a local
// name other than the one the resource type implies, such as google-beta
// (--beta) for google_* resources.
func TestImportsByDirNamesOtherProviders(t *testing.T) {
	resources := map[string][]harvest.Resource{
		"cloud_asset": {harvest.RecordBare("main", "main", "google_compute_network", "google-beta")},
	}
	options := Options{Layout: "{out}/{provider}/", Out: "out"}

	beta, _ := importsByDir("google", "google-beta", options, resources, listerID, noEnv, rawName, noFile)
	if imports := beta[filepath.Join("out", "google")]; len(imports) != 1 || imports[0].Provider != "google-beta" {
		t.Errorf("google-beta: got %+v, want provider google-beta in out/google", beta)
	}
	ga, _ := importsByDir("google", "google", options, resources, listerID, noEnv, rawName, noFile)
	if imports := ga[filepath.Join("out", "google")]; len(imports) != 1 || imports[0].Provider != "" {
		t.Errorf("google: got %+v, want no provider", ga)
	}
}

// Roots go under the provider's command name, while import blocks keep
// the Terraform provider: azurerm resources go under out/azure and need no
// provider argument, as azurerm is what their type implies.
func TestImportsByDirSeparatesDirectoryAndProvider(t *testing.T) {
	resources := map[string][]harvest.Resource{
		"resource_group": {harvest.RecordBare("/subscriptions/s/resourceGroups/rg", "rg", "azurerm_resource_group", "azurerm")},
	}
	options := Options{Layout: "{out}/{provider}/", Out: "out"}

	got, _ := importsByDir("azure", "azurerm", options, resources, listerID, noEnv, rawName, noFile)

	want := map[string][]engine.Import{
		filepath.Join("out", "azure"): {{Type: "azurerm_resource_group", Name: "rg", ID: "/subscriptions/s/resourceGroups/rg"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// directoryProvider is a provider whose roots go into a directory not
// named after its Terraform provider, as Azure's do.
type directoryProvider struct{ scopedProvider }

func (directoryProvider) Name() string      { return "azurerm" }
func (directoryProvider) Directory() string { return "azure" }

// The directory, not the Terraform provider, names the roots and the
// selection file's scope; a provider without one uses its name.
func TestProviderDir(t *testing.T) {
	if got := providerDir(&fakeProvider{}); got != "fake" {
		t.Errorf("providerDir(fakeProvider): got %q, want fake", got)
	}
	if got := providerDir(directoryProvider{}); got != "azure" {
		t.Errorf("providerDir(directoryProvider): got %q, want azure", got)
	}
	if got, want := discoveryScope(t.Context(), directoryProvider{}), "azure/111122223333/eu-west-2"; got != want {
		t.Errorf("discoveryScope(directoryProvider): got %q, want %q", got, want)
	}
}

func TestImportsByDirUsesImportIDs(t *testing.T) {
	resources := map[string][]harvest.Resource{
		"route_table": {
			harvest.RecordBare("rtbassoc-1", "a", "aws_route_table_association", "aws"),
			harvest.RecordBare("rtbassoc-2", "main", "aws_main_route_table_association", "aws"),
		},
	}
	importID := func(r harvest.Resource) (string, bool) {
		if r.Address.Type == "aws_main_route_table_association" {
			return "", false
		}
		return "subnet-1/rtb-1", true
	}
	options := Options{Layout: "{out}/{provider}/", Out: "out"}

	got, skipped := importsByDir("aws", "aws", options, resources, importID, noEnv, rawName, noFile)

	want := map[string][]engine.Import{
		filepath.Join("out", "aws"): {{Type: "aws_route_table_association", Name: "a", ID: "subnet-1/rtb-1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !reflect.DeepEqual(skipped, map[string]int{"aws_main_route_table_association": 1}) {
		t.Errorf("skipped: got %v", skipped)
	}
}

func TestImportIDFunc(t *testing.T) {
	cat := catalogue.MustBuiltin("aws")
	queue := harvest.RecordBare("listed", "a", "aws_sqs_queue", "aws")
	if id, ok := importIDFunc(cat)(queue); id != "listed" || !ok {
		t.Errorf("without a template: got %q, %v; want the lister's ID", id, ok)
	}
	assoc := harvest.Record("rtbassoc-1", "a", "aws_route_table_association", "aws", map[string]string{"subnet_id": "subnet-1", "route_table_id": "rtb-1"})
	if id, ok := importIDFunc(cat)(assoc); id != "subnet-1/rtb-1" || !ok {
		t.Errorf("with a template: got %q, %v; want the catalogue's ID", id, ok)
	}
	mainAssoc := harvest.RecordBare("rtbassoc-2", "main", "aws_main_route_table_association", "aws")
	if _, ok := importIDFunc(cat)(mainAssoc); ok {
		t.Error("aws_main_route_table_association: want not importable")
	}
}

type sourcedProvider struct {
	harvest.Provider
	data map[string]interface{}
}

func (p sourcedProvider) Name() string           { return "datadog" }
func (p sourcedProvider) RegistrySource() string { return "DataDog/datadog" }
func (p sourcedProvider) ProviderBlock() map[string]interface{} {
	return p.data
}

func TestEngineProvider(t *testing.T) {
	p := sourcedProvider{data: map[string]interface{}{
		"provider": map[string]interface{}{"datadog": map[string]interface{}{"api_url": "https://api.datadoghq.eu"}},
	}}

	got := engineProvider(p)

	want := engine.Provider{Name: "datadog", Source: "DataDog/datadog", Config: map[string]interface{}{"api_url": "https://api.datadoghq.eu"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// Unexpected provider data must not panic.
	if got := engineProvider(sourcedProvider{data: map[string]interface{}{"provider": "oops"}}); got.Config != nil {
		t.Errorf("want no config from malformed data, got %v", got.Config)
	}
}

func TestCheckImportOptions(t *testing.T) {
	for _, options := range []Options{{}, {Format: FormatText}, {Format: FormatJSON, Modules: ModulesNone}, {IDFilters: []string{"sqs=a,b"}, TagFilters: []string{"Team=web", "owner"}, WhereFilters: []string{"name=a"}}} {
		if err := checkImportOptions(options); err != nil {
			t.Errorf("%+v must be accepted, got %v", options, err)
		}
	}
	for want, options := range map[string]Options{
		"--format must be":  {Format: "yaml"},
		"--modules must be": {Modules: "some"},
		"names no ID":       {IDFilters: []string{"sqs="}},
		"--tag \"=web\"":    {TagFilters: []string{"=web"}},
	} {
		if err := checkImportOptions(options); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: got %v, want an error saying %q", options, err, want)
		}
	}
}

func TestImportRejectsUnknownEngine(t *testing.T) {
	err := Import(context.Background(), &fakeProvider{}, Options{Engine: "tofu-ish"}, nil)
	if err == nil || !strings.Contains(err.Error(), `unknown --engine "tofu-ish"`) {
		t.Errorf("want an unknown engine error, got %v", err)
	}
}

// The engine labels resources by the name the lister gave, as it is: "-"
// is part of names like my-2024-backups, and "-2024-" is kept.
func TestImportsByDirKeepRawNames(t *testing.T) {
	resources := map[string][]harvest.Resource{
		"s3": {
			harvest.RecordBare("my-2024-backups", "my-2024-backups", "aws_s3_bucket", "aws"),
			harvest.RecordBare("logs-2024-a", "logs-2024-a", "aws_s3_bucket", "aws"),
			harvest.RecordBare("logs-2025-a", "logs-2025-a", "aws_s3_bucket", "aws"),
		},
		"ssm": {harvest.RecordBare("/infraharvest-e2e/endpoint", "/infraharvest-e2e/endpoint", "aws_ssm_parameter", "aws")},
	}
	options := Options{Layout: "{out}/{provider}/", Out: "out"}

	got, _ := importsByDir("aws", "aws", options, resources, listerID, noEnv, rawName, noFile)

	names := map[string]bool{}
	for _, imp := range got[filepath.Join("out", "aws")] {
		names[imp.Name] = true
	}
	for _, want := range []string{"my-2024-backups", "logs-2024-a", "logs-2025-a", "/infraharvest-e2e/endpoint"} {
		if !names[want] {
			t.Errorf("name %q lost; got %v", want, names)
		}
	}
}

func TestWriteGitignore(t *testing.T) {
	out := filepath.Join(t.TempDir(), "generated")

	if err := writeGitignore(out); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(out, ".gitignore"))
	if err != nil || !strings.Contains(string(content), "*.tfstate") {
		t.Fatalf("want a .gitignore excluding state, got %q, %v", content, err)
	}

	// A .gitignore the user already has is kept.
	if err := os.WriteFile(filepath.Join(out, ".gitignore"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeGitignore(out); err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(filepath.Join(out, ".gitignore")); string(content) != "mine\n" {
		t.Errorf("existing .gitignore overwritten: %q", content)
	}
}

func TestEngineBinaryAndRegistry(t *testing.T) {
	for _, tc := range []struct{ engineName, binary, source string }{
		{EngineTerraform, "terraform", "registry.terraform.io/hashicorp/aws"},
		{EngineTofu, "tofu", "registry.opentofu.org/hashicorp/aws"},
	} {
		b := engineBinary(tc.engineName)
		if b.Name != tc.binary {
			t.Errorf("--engine=%s runs %s, want %s", tc.engineName, b.Name, tc.binary)
		}
		if got := qualifiedSource(b.Registry, "hashicorp/aws"); got != tc.source {
			t.Errorf("--engine=%s: got %s, want %s", tc.engineName, got, tc.source)
		}
	}
	if got := qualifiedSource("registry.opentofu.org", "example.com/acme/thing"); got != "example.com/acme/thing" {
		t.Errorf("a source with a host must be kept, got %s", got)
	}
}

type scopedProvider struct {
	harvest.Provider
}

func (scopedProvider) Scope(context.Context) (string, string, error) {
	return "111122223333", "eu-west-2", nil
}

func TestRootPathPattern(t *testing.T) {
	for pattern, want := range map[string]string{
		// Not given: one root per account and region.
		"": "{out}/{provider}/111122223333/eu-west-2/",
		// Given without {account} or {region}: followed as it is.
		"{out}/{provider}/{service}/": "{out}/{provider}/{service}/",
		"{out}/{account}/{service}/":  "{out}/111122223333/{service}/",
		"{out}/{provider}/":           "{out}/{provider}/",
	} {
		got, err := rootLayout(t.Context(), scopedProvider{}, pattern)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", pattern, got, err, want)
		}
	}
}

// An AWS resource is named after its Name tag; one without, and resources
// of other providers, keep the lister's name.
func TestResourceName(t *testing.T) {
	subnet := harvest.Resource{RawName: "subnet-0a1b2c3d"}
	for _, tc := range []struct {
		provider string
		tags     map[string]string
		want     string
	}{
		{"aws", map[string]string{"Name": "shop-private-eu-west-2a"}, "shop-private-eu-west-2a"},
		{"aws", map[string]string{"Name": " "}, "subnet-0a1b2c3d"},
		{"aws", nil, "subnet-0a1b2c3d"},
		{"azurerm", map[string]string{"Name": "shop"}, "subnet-0a1b2c3d"},
	} {
		if got := resourceName(tc.provider, subnet, tc.tags); got != tc.want {
			t.Errorf("resourceName(%s, %v) = %q, want %q", tc.provider, tc.tags, got, tc.want)
		}
	}
}

// A word of a resource's name that is another resource's ID becomes that
// resource's name: an association named after its subnet's ID reads as
// the subnet's Name tag, and an Elastic IP named after its NAT gateway as
// the gateway's. A name that is its own ID, or an ID nothing has a name
// for, stays as it is.
func TestResourceNamesResolvesIDs(t *testing.T) {
	subnet := harvest.RecordBare("subnet-1", "subnet-1", "aws_subnet", "aws")
	association := harvest.RecordBare("rtbassoc-1", "subnet-1", "aws_route_table_association", "aws")
	nat := harvest.RecordBare("nat-1", "nat-1", "aws_nat_gateway", "aws")
	eip := harvest.RecordBare("eipalloc-1", "nat-1", "aws_eip", "aws")
	table := harvest.RecordBare("rtb-1", "vpc-9 main", "aws_route_table", "aws")
	listed := listing{
		resources: map[string][]harvest.Resource{"subnet": {subnet}, "route_table": {association, table}, "eip": {eip}},
		tags: map[string]map[string]string{
			tagKey(subnet): {"Name": "shop-public-a"},
			tagKey(nat):    {"Name": "shop-nat"},
		},
	}
	// The NAT gateway is a child, listed only in resourcesByService.
	nameOf := resourceNames("aws", listed, map[string][]harvest.Resource{"nat": {nat}})
	for _, tc := range []struct {
		r    harvest.Resource
		want string
	}{
		{subnet, "shop-public-a"},
		{association, "shop-public-a"},
		{eip, "shop-nat"},
		{table, "vpc-9 main"},
	} {
		if got := nameOf(tc.r); got != tc.want {
			t.Errorf("name of %s %s: got %q, want %q", tc.r.Address.Type, tc.r.Observed.ID, got, tc.want)
		}
	}
}

// An import gets the arguments the catalogue leaves out of its resource
// depending on what the lister recorded: the instance at its type's CPU
// options leaves them out, the other keeps them.
func TestWithResourceOmissions(t *testing.T) {
	atDefaults := harvest.Record("i-1", "app", "aws_instance", "aws", map[string]string{"cpu_options": "default"})
	tuned := harvest.RecordBare("i-2", "db", "aws_instance", "aws")
	byDir := map[string][]engine.Import{"out": {
		{Type: "aws_instance", Name: "app", ID: "i-1"},
		{Type: "aws_instance", Name: "db", ID: "i-2"},
	}}

	withResourceOmissions(byDir, map[string][]harvest.Resource{"ec2_instance": {atDefaults, tuned}}, catalogue.MustBuiltin("aws"), listerID)

	if got := byDir["out"][0].Omit; !reflect.DeepEqual(got, []string{"cpu_options"}) {
		t.Errorf("app: omit %v, want [cpu_options]", got)
	}
	if got := byDir["out"][1].Omit; got != nil {
		t.Errorf("db: omit %v, want none", got)
	}
}

// --modules decides what holds clusters of resources: curated modules, the
// organisation's own and generated local ones (registry, latest-untested
// and the default), generated local modules only (local), or nothing, every
// resource staying in the root (none).
func TestModuleOptions(t *testing.T) {
	out := filepath.Join("out", "aws")
	resolved := []adapters.Adapter{{Source: "acme/bucket/aws", Version: "1.0.0"}}
	local := filepath.Join(out, engine.ModulesDirName)
	for _, tc := range []struct {
		mode       string
		registry   bool
		adapters   []adapters.Adapter
		modulesDir string
	}{
		{"", true, resolved, local},
		{ModulesRegistry, true, resolved, local},
		{ModulesLatestUntested, true, resolved, local},
		{ModulesLocal, false, nil, local},
		{ModulesNone, false, nil, ""},
	} {
		if got := modulesFromRegistry(tc.mode); got != tc.registry {
			t.Errorf("--modules %q: modulesFromRegistry = %v, want %v", tc.mode, got, tc.registry)
		}
		gotAdapters, gotDir := moduleOptions(tc.mode, out, resolved)
		if !reflect.DeepEqual(gotAdapters, tc.adapters) {
			t.Errorf("--modules %q: adapters %+v, want %+v", tc.mode, gotAdapters, tc.adapters)
		}
		if gotDir != tc.modulesDir {
			t.Errorf("--modules %q: modules directory %q, want %q", tc.mode, gotDir, tc.modulesDir)
		}
	}
}

// The command's context ends with its parent's, and stop releases it.
func TestInterruptContext(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	ctx, stop := InterruptContext(parent)
	defer stop()
	cancel()
	<-ctx.Done()

	ctx, stop = InterruptContext(t.Context())
	stop()
	if ctx.Err() == nil {
		t.Error("stop left the context running")
	}
}

// A lock file seeds a root only if its provider release meets the root's
// constraint.
func TestLockMeets(t *testing.T) {
	lock := []byte(`provider "registry.terraform.io/hashicorp/aws" {
  version     = "6.67.0"
  constraints = "~> 6.67"
}
`)
	const source = "registry.terraform.io/hashicorp/aws"
	for _, tc := range []struct {
		lock       []byte
		constraint string
		want       bool
	}{
		{lock, "~> 6.67", true},
		{lock, "~> 6.50", true},
		{lock, ">= 6.50, < 6.51", false},
		{nil, "~> 6.50", true},
		{lock, "", true},
	} {
		if got := lockMeets(tc.lock, source, tc.constraint); got != tc.want {
			t.Errorf("lock %q, constraint %q: got %v, want %v", tc.lock, tc.constraint, got, tc.want)
		}
	}
}

// envProvider is fakeProvider with Terraform credentials that can't be
// refreshed once the import started.
type envProvider struct {
	fakeProvider
	asked int
}

func (p *envProvider) TerraformEnv(context.Context) (map[string]string, error) {
	p.asked++
	if p.asked > 1 {
		return nil, errors.New("the SSO session has expired")
	}
	return map[string]string{"AWS_ACCESS_KEY_ID": "AKIDFIRST"}, nil
}

// A provider that gives Terraform credentials gives them again before each
// plan of a root, not only once for the root; one that gives none has a
// runner of its own.
func TestTerraformRunAsksTheProviderBeforeEachPlan(t *testing.T) {
	p := &envProvider{}
	env, err := terraformEnv(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	tr := terraformRun{execPath: os.Args[0], pluginCacheDir: filepath.Join(base, "plugins"), env: env, freshEnv: freshTerraformEnv(p)}
	tf, err := tr.in(filepath.Join(base, "root"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tf.PlanJSON(t.Context(), io.Discard); err == nil || !strings.Contains(err.Error(), "credentials for Terraform: the SSO session has expired") {
		t.Errorf("want the plan failed with the refresh's error, got %v", err)
	}
	if p.asked != 2 {
		t.Errorf("asked %d times, want once for the root and once for the plan", p.asked)
	}
	if freshTerraformEnv(&fakeProvider{}) != nil {
		t.Error("a provider without Terraform credentials has none to refresh")
	}
}
