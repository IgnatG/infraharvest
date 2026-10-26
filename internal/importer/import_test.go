// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

func TestCheckFailures(t *testing.T) {
	failures := []error{errors.New("service sqs: access denied"), errors.New("refresh aws_sns_topic.a: timeout")}

	if err := checkFailures(nil, false); err != nil {
		t.Errorf("no failures: want nil, got %v", err)
	}

	err := checkFailures(failures, false)
	if err == nil {
		t.Fatal("failures without --allow-partial: want an error")
	}
	for _, want := range []string{"2 services or resources", "service sqs: access denied", "refresh aws_sns_topic.a: timeout", "--allow-partial"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if !errors.Is(err, failures[0]) {
		t.Error("error should wrap each failure")
	}
	if code := ExitCode(err); code != report.ExitIncomplete {
		t.Errorf("exit code without --allow-partial: got %d, want %d", code, report.ExitIncomplete)
	}

	partial := checkFailures(failures, true)
	if code := ExitCode(partial); code != report.ExitPartial {
		t.Errorf("exit code with --allow-partial: got %d, want %d (%v)", code, report.ExitPartial, partial)
	}
}

func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{nil, report.ExitOK},
		{errors.New("no credentials"), report.ExitCouldNotRun},
		{fmt.Errorf("wrapped: %w", &ExitError{Code: report.ExitPartial, Err: errors.New("partial")}), report.ExitPartial},
	} {
		if got := ExitCode(tc.err); got != tc.want {
			t.Errorf("ExitCode(%v): got %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestInitAllServicesResourcesReportsFailedServices(t *testing.T) {
	registry := harvest.NewRegistry(&fakeProvider{})
	options := Options{Services: []string{"good", "bad"}}

	failures, err := listServices(t.Context(), registry, options, nil)
	if err != nil {
		t.Fatalf("a failing service must not abort the run: %v", err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0].Error(), "service bad") {
		t.Errorf("want one failure for service bad, got %v", failures)
	}
	if _, ok := registry.Services["bad"]; ok {
		t.Error("failed service should be removed from the registry")
	}
	if got := len(registry.Resources); got != 1 {
		t.Errorf("want the good service's resource only, got %d resources", got)
	}
}

// A service that lists in part keeps what it listed, and fails the import
// all the same.
func TestInitAllServicesResourcesKeepsAPartialListing(t *testing.T) {
	registry := harvest.NewRegistry(&fakeProvider{})
	options := Options{Services: []string{"good", "partial", "bad"}}

	failures, err := listServices(t.Context(), registry, options, nil)
	if err != nil {
		t.Fatalf("a partial listing must not abort the run: %v", err)
	}
	if len(failures) != 2 || !strings.Contains(failures[0].Error(), "service partial: a zone's records: access denied") || !strings.Contains(failures[1].Error(), "service bad") {
		t.Errorf("want failures for services partial and bad, got %v", failures)
	}
	byService := registry.ByService()
	if got := byService["partial"]; len(got) != 1 || got[0].Observed.ID != "id-2" {
		t.Errorf("want the partial service's resource kept, got %v", got)
	}
	if _, ok := byService["bad"]; ok {
		t.Error("failed service should be removed from the registry")
	}
	if got := len(registry.Resources); got != 2 {
		t.Errorf("want the good and the partial services' resources, got %d resources", got)
	}
}

// The clock is virtual (synctest), so the good service finishes within the
// deadline however loaded the runner.
func TestInitAllServicesResourcesTimesOutAService(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := harvest.NewRegistry(&fakeProvider{})
		options := Options{Services: []string{"slow", "good"}, ListTimeout: 10 * time.Millisecond}

		failures, err := listServices(t.Context(), registry, options, nil)
		if err != nil {
			t.Fatalf("a slow service must not abort the run: %v", err)
		}
		if len(failures) != 1 || !strings.Contains(failures[0].Error(), "service slow: listing took longer than --list-timeout 10ms") {
			t.Errorf("want the slow service reported, got %v", failures)
		}
		if got := len(registry.Resources); got != 1 {
			t.Errorf("want the good service's resource only, got %d resources", got)
		}
	})
}

// An interrupt stops listing.
func TestInitAllServicesResourcesStopsWhenInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	registry := harvest.NewRegistry(&fakeProvider{})

	_, err := listServices(ctx, registry, Options{Services: []string{"slow", "good"}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want the run stopped, got %v", err)
	}
}

// fakeProvider lists one resource per service, fails for service "bad",
// lists one and fails in part for service "partial", and lists service
// "slow" until its context is done.
type fakeProvider struct {
	harvest.Provider
	service *fakeService
}

func (p *fakeProvider) Init(context.Context, []string) error { return nil }
func (p *fakeProvider) Name() string                         { return "fake" }
func (p *fakeProvider) Selected() harvest.Lister {
	return p.service
}
func (p *fakeProvider) Select(name string, _ bool) error {
	p.service = &fakeService{}
	p.service.Configure(harvest.ListerConfig{Service: name})
	return nil
}

type fakeService struct {
	harvest.ListerBase
}

func (s *fakeService) List() error {
	switch s.Name() {
	case "bad":
		return errors.New("access denied")
	case "partial":
		s.Resources = []harvest.Resource{harvest.RecordBare("id-2", "two", "fake_thing", "fake")}
		return harvest.Partial(errors.New("a zone's records: access denied"))
	case "slow":
		// Like listers that log a failed call and go on.
		<-s.Context().Done()
		return nil
	}
	s.Resources = []harvest.Resource{
		harvest.RecordBare("id-1", "one", "fake_thing", "fake"),
	}
	return nil
}

// servicesProvider is fakeProvider with the services vpc and s3.
type servicesProvider struct {
	fakeProvider
}

func (*servicesProvider) Services() map[string]harvest.Lister {
	return map[string]harvest.Lister{"vpc": nil, "s3": nil}
}

// optInProvider is servicesProvider with s3 opt-in.
type optInProvider struct {
	servicesProvider
}

func (*optInProvider) OptInServices() []string { return []string{"s3"} }

// * leaves out the opt-in services, and an opt-in service named beside it
// is listed too, once, as is one that * lists already.
func TestResolveServicesKeepsWhatIsNamedBesideTheWildcard(t *testing.T) {
	for _, tc := range []struct {
		services, want []string
	}{
		{[]string{"*"}, []string{"vpc"}},
		{[]string{"*", "s3"}, []string{"vpc", "s3"}},
		{[]string{"s3", "*", "vpc", "s3"}, []string{"vpc", "s3"}},
	} {
		if got := ResolveServices(&optInProvider{}, Options{Services: tc.services}).Services; !slices.Equal(got, tc.want) {
			t.Errorf("%v: got %v, want %v", tc.services, got, tc.want)
		}
	}
}

// What no Import call could do fails the command once, before any scope:
// an unknown service or engine, an import that says neither --selection
// nor --all, a selection file or catalogue that doesn't load, a
// managed state source that doesn't parse or isn't there, a --layout
// that leads out of --out, has a placeholder nothing fills in, or no
// {env} for --env-tags, --default-env without --env-tags or {env}, and a
// negative number of a flag that counts.
func TestCheckOptions(t *testing.T) {
	p := &servicesProvider{}
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	goodSelection := write("selection.yaml", fmt.Sprintf("version: %d\n", selection.Version))
	badSelection := write("old.yaml", "version: 99\n")
	missing := filepath.Join(dir, "missing")
	write("catalogue/broken.yaml", "types: [\n")
	state := write("state/a.tfstate", `{"version": 4}`)
	for _, tc := range []struct {
		options Options
		want    string
	}{
		{Options{All: true, Services: []string{"vpc", "*"}}, ""},
		{Options{Discover: true, Services: []string{"vpc"}, Skip: []string{"s3"}}, ""},
		{Options{All: true, Services: []string{"vpc", "vcp"}}, "--services: fake has no service vcp"},
		{Options{All: true, Services: []string{"*"}, Skip: []string{"s4"}}, "--skip: fake has no service s4"},
		{Options{All: true, Engine: "pulumi"}, `unknown --engine "pulumi"`},
		{Options{Services: []string{"vpc"}}, "selection"},
		{Options{Selection: goodSelection}, ""},
		{Options{Selection: badSelection}, "--selection: " + badSelection + ": version 99"},
		{Options{Selection: missing}, "--selection"},
		{Options{Discover: true, Selection: missing}, ""},
		{Options{All: true, Catalogue: filepath.Join(dir, "catalogue")}, "--catalogue"},
		{Options{All: true, ManagedState: []string{state, filepath.Dir(state), "backend", "s3://bucket/prefix", "tfc://org/app-*"}}, ""},
		{Options{All: true, ManagedState: []string{"s3:///prefix"}}, "--managed-state: state s3:///prefix: name a bucket"},
		{Options{All: true, ManagedState: []string{"tfc://org"}}, "name an organization and a workspace"},
		{Options{All: true, ManagedState: []string{missing}}, "--managed-state: state " + missing},
		{Options{All: true, Layout: "{out}/{provider}/{account}/{region}/{service}/{env}/"}, ""},
		{Options{All: true, Layout: `{out}\{provider}\`}, ""},
		{Options{All: true, Layout: "{out}"}, ""},
		{Options{All: true, Layout: "{out}/{project}/"}, "unknown placeholder {project}"},
		{Options{All: true, Layout: "{provider}/{account}/"}, "must start with {out}/"},
		{Options{All: true, Layout: "/srv/roots/{provider}/"}, "must start with {out}/"},
		{Options{All: true, Layout: "{out}-{provider}/"}, "must start with {out}/"},
		{Options{All: true, Layout: "{out}/../{provider}/"}, "leads out of --out"},
		{Options{All: true, EnvTags: []string{"Environment"}, Layout: DefaultLayout}, ""},
		{Options{All: true, EnvTags: []string{"Environment"}, Layout: "{out}/{env}/{provider}/"}, ""},
		{Options{All: true, EnvTags: []string{"Environment"}, Layout: "{out}/{provider}/"}, "has no {env}"},
		{Options{All: true, EnvTags: []string{"Environment"}, DefaultEnv: "dev"}, ""},
		{Options{All: true, DefaultEnv: "dev", Layout: "{out}/{env}/{provider}/"}, ""},
		{Options{All: true, DefaultEnv: "dev", Layout: DefaultLayout}, "use it with --env-tags"},
		{Options{All: true, RateLimit: 0, ListTimeout: 0, ListParallel: 0, Parallel: 0}, ""},
		{Options{All: true, RateLimit: -1}, "--rate-limit can't be negative"},
		{Options{All: true, ListTimeout: -time.Second}, "--list-timeout can't be negative"},
		{Options{All: true, ListParallel: -1}, "--list-parallel can't be negative"},
		{Options{All: true, Parallel: -2}, "--parallel can't be negative"},
		{Options{All: true, EngineTimeout: -time.Second}, "--engine-timeout can't be negative"},
	} {
		err := CheckOptions(p, tc.options)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%+v: %v", tc.options, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%+v: got %v, want %q", tc.options, err, tc.want)
		}
	}
}

// catalogueProvider is fakeProvider, named aws, that takes the catalogue
// of the import, and has the service ivs only once given one.
type catalogueProvider struct {
	fakeProvider
	cat *catalogue.Catalogue
}

func (*catalogueProvider) Name() string { return "aws" }

func (p *catalogueProvider) SetCatalogue(c *catalogue.Catalogue) { p.cat = c }

func (p *catalogueProvider) Services() map[string]harvest.Lister {
	services := map[string]harvest.Lister{"vpc": nil}
	if p.cat != nil {
		services["ivs"] = nil
	}
	return services
}

// The provider of each service listed, new for the service, is given the
// catalogue of the import, overlay and all, before it makes the service's
// lister.
func TestListServicesGivesEachServiceTheCatalogue(t *testing.T) {
	cat := catalogue.MustBuiltin("aws")
	registry := harvest.NewRegistry(&catalogueProvider{})

	if _, err := listServices(t.Context(), registry, Options{Services: []string{"good", "partial"}, cat: cat}, nil); err != nil {
		t.Fatal(err)
	}
	if len(registry.Providers) != 2 {
		t.Fatalf("want a provider per service, got %d", len(registry.Providers))
	}
	for provider := range registry.Providers {
		if got := provider.(*catalogueProvider).cat; got != cat {
			t.Errorf("a service's provider has catalogue %p, want %p", got, cat)
		}
	}
}

// dependentProvider is fakeProvider with the service after, which lists
// from what the other services listed: a resource for each of theirs.
type dependentProvider struct {
	fakeProvider
	listed []harvest.Resource
	after  *afterService
}

func (*dependentProvider) DependentServices() []string { return []string{"after"} }

func (p *dependentProvider) SetListedByOthers(resources []harvest.Resource) { p.listed = resources }

func (p *dependentProvider) Select(name string, verbose bool) error {
	if name != "after" {
		return p.fakeProvider.Select(name, verbose)
	}
	p.after = &afterService{listed: p.listed}
	p.after.Configure(harvest.ListerConfig{Service: name})
	return nil
}

func (p *dependentProvider) Selected() harvest.Lister {
	if p.after != nil {
		return p.after
	}
	return p.fakeProvider.Selected()
}

// afterService lists a fake_after for each resource its provider was
// given when it was selected.
type afterService struct {
	harvest.ListerBase
	listed []harvest.Resource
}

func (s *afterService) List() error {
	for _, r := range s.listed {
		s.Resources = append(s.Resources, harvest.RecordBare(r.Observed.ID+"/after", "after", "fake_after", "fake"))
	}
	return nil
}

// A dependent service lists once the others are done, wherever --services
// names it, from what they listed: what a service that failed in part
// listed too, and nothing of one that failed.
func TestListServicesListsDependentServicesAfterTheOthers(t *testing.T) {
	registry := harvest.NewRegistry(&dependentProvider{})

	failures, err := listServices(t.Context(), registry, Options{Services: []string{"after", "good", "bad", "partial"}, ListParallel: 4}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(failures) != 2 {
		t.Errorf("want the failures of bad and partial, got %v", failures)
	}
	var got []string
	for _, r := range registry.ByService()["after"] {
		got = append(got, r.Observed.ID)
	}
	if want := []string{"id-1/after", "id-2/after"}; !slices.Equal(got, want) {
		t.Errorf("after listed %v, want %v", got, want)
	}
}

// A service an overlay adds is known when --services is checked.
func TestCheckOptionsKnowsTheServicesOfTheOverlay(t *testing.T) {
	dir := t.TempDir()
	overlay := "provider: aws\nservice: ivs\ntypes:\n  aws_ivs_channel:\n    cloud_control: { type: \"AWS::IVS::Channel\", identifier: Arn }\n"
	if err := os.WriteFile(filepath.Join(dir, "aws.yaml"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckOptions(&catalogueProvider{}, Options{All: true, Services: []string{"ivs"}, Catalogue: dir}); err != nil {
		t.Errorf("with the overlay: %v", err)
	}
	if err := CheckOptions(&catalogueProvider{}, Options{All: true, Services: []string{"ivs"}}); err == nil || !strings.Contains(err.Error(), "has no service ivs") {
		t.Errorf("without the overlay: got %v, want the service unknown", err)
	}
}

// childrenProvider is fakeProvider with a child for each resource but
// those named "lone", which fails; the first resources answer last. It
// counts how many are asked at once.
type childrenProvider struct {
	fakeProvider
	mu            sync.Mutex
	running, most int
}

func (p *childrenProvider) ChildImports(_ context.Context, r harvest.Resource) ([]harvest.Resource, error) {
	p.mu.Lock()
	p.running++
	p.most = max(p.most, p.running)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.running--
		p.mu.Unlock()
	}()
	n, _ := strconv.Atoi(strings.TrimPrefix(r.Observed.ID, "id-"))
	time.Sleep(time.Duration(10-n) * time.Millisecond)
	if r.RawName == "lone" {
		return nil, errors.New("access denied")
	}
	return []harvest.Resource{harvest.RecordBare(r.Observed.ID, r.RawName, "fake_child", "fake")}, nil
}

// Resources are asked for their children up to parallel at once, and
// each service has its resources, then their children, in their order,
// whatever order the answers come in; failures come in the order of the
// services, by name, and of their resources.
func TestWithChildImportsAsksInParallelAndKeepsTheOrder(t *testing.T) {
	p := &childrenProvider{}
	byService := map[string][]harvest.Resource{
		"b": {harvest.RecordBare("id-3", "lone", "fake_thing", "fake"), harvest.RecordBare("id-4", "four", "fake_thing", "fake")},
		"a": {harvest.RecordBare("id-0", "zero", "fake_thing", "fake"), harvest.RecordBare("id-1", "lone", "fake_thing", "fake"), harvest.RecordBare("id-2", "two", "fake_thing", "fake")},
	}

	all, parents, failures := withChildImports(t.Context(), p, byService, 2)

	ids := func(resources []harvest.Resource) []string {
		var got []string
		for _, r := range resources {
			got = append(got, r.Address.Type+" "+r.Observed.ID)
		}
		return got
	}
	for service, want := range map[string][]string{
		"a": {"fake_thing id-0", "fake_thing id-1", "fake_thing id-2", "fake_child id-0", "fake_child id-2"},
		"b": {"fake_thing id-3", "fake_thing id-4", "fake_child id-4"},
	} {
		if got := ids(all[service]); !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", service, got, want)
		}
	}
	if parents["fake_child id-2"] != "fake_thing id-2" {
		t.Errorf("parents %v", parents)
	}
	if len(failures) != 2 || !strings.HasPrefix(failures[0].Error(), "a: fake_thing id-1") || !strings.HasPrefix(failures[1].Error(), "b: fake_thing id-3") {
		t.Errorf("failures %v, want id-1's then id-3's", failures)
	}
	if p.most > 2 || p.most < 1 {
		t.Errorf("%d asked at once, want at most 2", p.most)
	}
	if len(byService["a"]) != 3 {
		t.Error("the resources given were changed")
	}
}
