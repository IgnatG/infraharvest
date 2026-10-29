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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	options := Options{Services: []string{"good", "bad"}}

	resources, failures, err := listServices(t.Context(), &fakeProvider{}, callOf(options, ""), options.ListParallel)
	if err != nil {
		t.Fatalf("a failing service must not abort the run: %v", err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0].Error(), "service bad") {
		t.Errorf("want one failure for service bad, got %v", failures)
	}
	if _, ok := resources["bad"]; ok {
		t.Error("a failed service should have no resources")
	}
	if got := len(resources["good"]); len(resources) != 1 || got != 1 {
		t.Errorf("want the good service's resource only, got %v", resources)
	}
}

// A service that lists in part keeps what it listed, and fails the import
// all the same.
func TestInitAllServicesResourcesKeepsAPartialListing(t *testing.T) {
	options := Options{Services: []string{"good", "partial", "bad"}}

	resources, failures, err := listServices(t.Context(), &fakeProvider{}, callOf(options, ""), options.ListParallel)
	if err != nil {
		t.Fatalf("a partial listing must not abort the run: %v", err)
	}
	if len(failures) != 2 || !strings.Contains(failures[0].Error(), "service partial: a zone's records: access denied") || !strings.Contains(failures[1].Error(), "service bad") {
		t.Errorf("want failures for services partial and bad, got %v", failures)
	}
	if got := resources["partial"]; len(got) != 1 || got[0].Observed.ID != "id-2" {
		t.Errorf("want the partial service's resource kept, got %v", got)
	}
	if _, ok := resources["bad"]; ok {
		t.Error("a failed service should have no resources")
	}
	if len(resources) != 2 {
		t.Errorf("want the good and the partial services' resources, got %v", resources)
	}
}

// The clock is virtual (synctest), so the good service finishes within the
// deadline however loaded the runner.
func TestInitAllServicesResourcesTimesOutAService(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		options := Options{Services: []string{"slow", "good"}, ListTimeout: 10 * time.Millisecond}

		resources, failures, err := listServices(t.Context(), &fakeProvider{}, callOf(options, ""), options.ListParallel)
		if err != nil {
			t.Fatalf("a slow service must not abort the run: %v", err)
		}
		if len(failures) != 1 || !strings.Contains(failures[0].Error(), "service slow: listing took longer than --list-timeout 10ms, so it stopped: what it listed by then is imported") || !harvest.IsPartial(failures[0]) {
			t.Errorf("want the slow service reported as listed in part, got %v", failures)
		}
		// What the slow service listed before its deadline is kept.
		if got := resources["slow"]; len(got) != 1 || got[0].Observed.ID != "id-3" {
			t.Errorf("want the slow service's resource kept, got %v", got)
		}
		if len(resources) != 2 {
			t.Errorf("want the good and the slow services' resources, got %v", resources)
		}
	})
}

// settingsProvider is fakeProvider whose listers record the settings each
// is given.
type settingsProvider struct {
	fakeProvider
	mu    sync.Mutex
	given map[string]harvest.Settings
}

func (p *settingsProvider) Lister(string) (harvest.Lister, error) {
	return settingsRecorder{p}, nil
}

type settingsRecorder struct{ p *settingsProvider }

func (r settingsRecorder) List(_ context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	r.p.mu.Lock()
	defer r.p.mu.Unlock()
	r.p.given[s.Service] = s
	return nil, nil
}

// Each lister gets the call's --id, --tag and --where filters and the types
// it leaves out, so that it can skip the calls of what the import won't keep.
func TestListServicesGivesEachListerTheCallsSettings(t *testing.T) {
	options := Options{
		Services:     []string{"s3", "vpc"},
		Verbose:      true,
		IDFilters:    []string{"aws_vpc=vpc-1"},
		TagFilters:   []string{"env=prod"},
		WhereFilters: []string{"cidr_block=10.0.0.0/16"},
	}
	c := callOf(options, "")
	c.unwanted = func(typ string) bool { return typ == "aws_iam_access_key" }
	p := &settingsProvider{given: map[string]harvest.Settings{}}
	if _, failures, err := listServices(t.Context(), p, c, 2); err != nil || len(failures) > 0 {
		t.Fatalf("listServices: %v %v", failures, err)
	}
	filters, err := options.filters()
	if err != nil || len(filters) != 3 {
		t.Fatalf("filters: %v %v", filters, err)
	}
	for _, service := range options.Services {
		s, ok := p.given[service]
		if !ok {
			t.Errorf("%s: never listed", service)
			continue
		}
		if s.Service != service || s.Provider != "fake" || !s.Verbose {
			t.Errorf("%s: got service %q, provider %q, verbose %v", service, s.Service, s.Provider, s.Verbose)
		}
		if !reflect.DeepEqual(s.Filters, filters) {
			t.Errorf("%s: filters: got %+v, want %+v", service, s.Filters, filters)
		}
		if s.Wants("aws_iam_access_key") || !s.Wants("aws_vpc") {
			t.Errorf("%s: the settings don't leave out the call's unwanted types", service)
		}
	}
}

// An interrupt stops listing.
func TestInitAllServicesResourcesStopsWhenInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := listServices(ctx, &fakeProvider{}, callOf(Options{Services: []string{"slow", "good"}}, ""), 0)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want the run stopped, got %v", err)
	}
}

// fakeProvider lists one resource per service, fails for service "bad",
// lists one and fails in part for service "partial", and lists one for
// service "slow", then fails when its context is done.
// fakeScope is a provider command's scope (see Options.ProviderScope)
// with each capability harvest declares for one.
type fakeScope struct {
	stacks         bool
	profile        string
	partial, group string
}

func (s fakeScope) StackScoped() bool   { return s.stacks }
func (s fakeScope) ListsInPart() string { return s.partial }
func (s fakeScope) ScopeGroup() string  { return s.group }
func (s fakeScope) ManagedStateSource(source string) string {
	if s.profile == "" {
		return source
	}
	return source + "?profile=" + s.profile
}

type fakeProvider struct {
	harvest.Provider
}

func (p *fakeProvider) Init(context.Context, []string) error { return nil }
func (p *fakeProvider) Name() string                         { return "fake" }
func (p *fakeProvider) Profile() harvest.Profile             { return harvest.Profile{} }
func (p *fakeProvider) Lister(string) (harvest.Lister, error) {
	return fakeService{}, nil
}

type fakeService struct{}

func (fakeService) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	switch s.Service {
	case "bad":
		return nil, errors.New("access denied")
	case "partial":
		return []harvest.Resource{harvest.RecordBare("id-2", "two", "fake_thing", "fake")}, harvest.Partial(errors.New("a zone's records: access denied"))
	case "slow":
		// Lists one, then waits on a call that fails when its context is
		// done.
		<-ctx.Done()
		return []harvest.Resource{harvest.RecordBare("id-3", "three", "fake_thing", "fake")}, ctx.Err()
	}
	return []harvest.Resource{harvest.RecordBare("id-1", "one", "fake_thing", "fake")}, nil
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

func (*optInProvider) Profile() harvest.Profile {
	return harvest.Profile{OptInServices: []string{"s3"}}
}

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
		if got := ResolveServices(&optInProvider{}, Options{Services: tc.services}); !slices.Equal(got, tc.want) {
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
		{Options{Services: []string{"vpc"}, All: true, Engine: "pulumi"}, `unknown --engine "pulumi"`},
		{Options{All: true}, "--services is required"},
		{Options{ProviderScope: fakeScope{stacks: true}}, ""},
		{Options{Services: []string{"vpc"}}, "selection"},
		{Options{Services: []string{"vpc"}, Selection: goodSelection}, ""},
		{Options{Services: []string{"vpc"}, Selection: badSelection}, "--selection: " + badSelection + ": version 99"},
		{Options{Services: []string{"vpc"}, Selection: missing}, "--selection"},
		{Options{Services: []string{"vpc"}, Discover: true, Selection: missing}, ""},
		{Options{Services: []string{"vpc"}, All: true, Catalogue: filepath.Join(dir, "catalogue")}, "--catalogue"},
		{Options{Services: []string{"vpc"}, All: true, ManagedState: []string{state, filepath.Dir(state), "backend", "s3://bucket/prefix", "tfc://org/app-*"}}, ""},
		{Options{Services: []string{"vpc"}, All: true, ManagedState: []string{"s3:///prefix"}}, "--managed-state: state s3:///prefix: name a bucket"},
		{Options{Services: []string{"vpc"}, All: true, ManagedState: []string{"tfc://org"}}, "name an organization and a workspace"},
		{Options{Services: []string{"vpc"}, All: true, ManagedState: []string{missing}}, "--managed-state: state " + missing},
		{Options{Services: []string{"vpc"}, All: true, Layout: "{out}/{provider}/{account}/{region}/{service}/{env}/"}, ""},
		{Options{Services: []string{"vpc"}, All: true, Layout: `{out}\{provider}\`}, ""},
		{Options{Services: []string{"vpc"}, All: true, Layout: "{out}"}, ""},
		{Options{Services: []string{"vpc"}, All: true, Layout: "{out}/{project}/"}, "unknown placeholder {project}"},
		{Options{Services: []string{"vpc"}, All: true, Layout: "{provider}/{account}/"}, "must start with {out}/"},
		{Options{Services: []string{"vpc"}, All: true, Layout: "/srv/roots/{provider}/"}, "must start with {out}/"},
		{Options{Services: []string{"vpc"}, All: true, Layout: "{out}-{provider}/"}, "must start with {out}/"},
		{Options{Services: []string{"vpc"}, All: true, Layout: "{out}/../{provider}/"}, "leads out of --out"},
		{Options{Services: []string{"vpc"}, All: true, EnvTags: []string{"Environment"}, Layout: DefaultLayout}, ""},
		{Options{Services: []string{"vpc"}, All: true, EnvTags: []string{"Environment"}, Layout: "{out}/{env}/{provider}/"}, ""},
		{Options{Services: []string{"vpc"}, All: true, EnvTags: []string{"Environment"}, Layout: "{out}/{provider}/"}, "has no {env}"},
		{Options{Services: []string{"vpc"}, All: true, EnvTags: []string{"Environment"}, DefaultEnv: "dev"}, ""},
		{Options{Services: []string{"vpc"}, All: true, DefaultEnv: "dev", Layout: "{out}/{env}/{provider}/"}, ""},
		{Options{Services: []string{"vpc"}, All: true, DefaultEnv: "dev", Layout: DefaultLayout}, "use it with --env-tags"},
		{Options{Services: []string{"vpc"}, All: true, RateLimit: 0, ListTimeout: 0, ListParallel: 0, Parallel: 0}, ""},
		{Options{Services: []string{"vpc"}, All: true, RateLimit: -1}, "--rate-limit can't be negative"},
		{Options{Services: []string{"vpc"}, All: true, ListTimeout: -time.Second}, "--list-timeout can't be negative"},
		{Options{Services: []string{"vpc"}, All: true, ListParallel: -1}, "--list-parallel can't be negative"},
		{Options{Services: []string{"vpc"}, All: true, Parallel: -2}, "--parallel can't be negative"},
		{Options{Services: []string{"vpc"}, All: true, EngineTimeout: -time.Second}, "--engine-timeout can't be negative"},
	} {
		err := checkOnly(p, tc.options)
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

// dependentProvider is fakeProvider with the service after, which lists
// from what the other services listed: a resource for each of theirs.
type dependentProvider struct {
	fakeProvider
	listed []harvest.Resource
}

func (*dependentProvider) Profile() harvest.Profile {
	return harvest.Profile{DependentServices: []string{"after"}}
}

func (p *dependentProvider) SetListedByOthers(resources []harvest.Resource) { p.listed = resources }

func (p *dependentProvider) Lister(name string) (harvest.Lister, error) {
	if name != "after" {
		return p.fakeProvider.Lister(name)
	}
	return &afterService{listed: p.listed}, nil
}

// afterService lists a fake_after for each resource its provider was
// given when it made the lister.
type afterService struct {
	listed []harvest.Resource
}

func (s *afterService) List(context.Context, harvest.Settings) ([]harvest.Resource, error) {
	var resources []harvest.Resource
	for _, r := range s.listed {
		resources = append(resources, harvest.RecordBare(r.Observed.ID+"/after", "after", "fake_after", "fake"))
	}
	return resources, nil
}

// A dependent service lists once the others are done, wherever --services
// names it, from what they listed: what a service that failed in part
// listed too, and nothing of one that failed.
func TestListServicesListsDependentServicesAfterTheOthers(t *testing.T) {
	resources, failures, err := listServices(t.Context(), &dependentProvider{}, callOf(Options{Services: []string{"after", "good", "bad", "partial"}}, ""), 4)
	if err != nil {
		t.Fatal(err)
	}

	if len(failures) != 2 {
		t.Errorf("want the failures of bad and partial, got %v", failures)
	}
	var got []string
	for _, r := range resources["after"] {
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
	if err := checkOnly(&catalogueProvider{}, Options{All: true, Services: []string{"ivs"}, Catalogue: dir}); err != nil {
		t.Errorf("with the overlay: %v", err)
	}
	if err := checkOnly(&catalogueProvider{}, Options{All: true, Services: []string{"ivs"}}); err == nil || !strings.Contains(err.Error(), "has no service ivs") {
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

// blockedChildren is fakeProvider whose ChildImports waits until release
// is closed, counting the calls that started.
type blockedChildren struct {
	fakeProvider
	started atomic.Int32
	release chan struct{}
}

func (p *blockedChildren) ChildImports(context.Context, harvest.Resource) ([]harvest.Resource, error) {
	p.started.Add(1)
	<-p.release
	return nil, nil
}

// Asking many resources for their children takes parallel goroutines, not
// one for each resource.
func TestWithChildImportsStartsParallelGoroutines(t *testing.T) {
	p := &blockedChildren{release: make(chan struct{})}
	var resources []harvest.Resource
	for i := range 2000 {
		resources = append(resources, harvest.RecordBare(fmt.Sprintf("id-%d", i), "r", "fake_thing", "fake"))
	}
	before := runtime.NumGoroutine()
	done := make(chan struct{})
	go func() {
		defer close(done)
		withChildImports(t.Context(), p, map[string][]harvest.Resource{"a": resources}, 4)
	}()
	for p.started.Load() < 4 {
		runtime.Gosched()
	}
	if extra := runtime.NumGoroutine() - before; extra > 50 {
		t.Errorf("%d goroutines while asking 2000 resources 4 at a time", extra)
	}
	close(p.release)
	<-done
	if got := p.started.Load(); got != 2000 {
		t.Errorf("asked %d resources, want 2000", got)
	}
}

// callOf is an Import call of options in scope, which lists the services
// options name as they are.
func callOf(options Options, scope string) *callContext {
	return &callContext{options: options, services: options.Services, scope: scope}
}
