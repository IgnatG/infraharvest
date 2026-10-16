// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestInitAllServicesResourcesTimesOutAService(t *testing.T) {
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
// and lists service "slow" until its context is done.
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

// What no Import call could do fails the command once, before any scope:
// an unknown service or engine, an import that says neither --selection
// nor --all, a selection file or catalogue that doesn't load, a
// managed state source that doesn't parse or isn't there, a --layout
// that leads out of --out, has a placeholder nothing fills in, or no
// {env} for --env-tags, and a negative number of a flag that counts.
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
		{Options{All: true, RateLimit: 0, ListTimeout: 0, ListParallel: 0, Parallel: 0}, ""},
		{Options{All: true, RateLimit: -1}, "--rate-limit can't be negative"},
		{Options{All: true, ListTimeout: -time.Second}, "--list-timeout can't be negative"},
		{Options{All: true, ListParallel: -1}, "--list-parallel can't be negative"},
		{Options{All: true, Parallel: -2}, "--parallel can't be negative"},
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
