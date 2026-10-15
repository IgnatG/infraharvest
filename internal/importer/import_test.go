// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
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
