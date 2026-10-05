package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/terraformutils"
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
	mapping := terraformutils.NewProvidersMapping(&fakeProvider{})
	options := ImportOptions{Resources: []string{"good", "bad"}}

	failures, err := initAllServicesResources(t.Context(), mapping, options, nil)
	if err != nil {
		t.Fatalf("a failing service must not abort the run: %v", err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0].Error(), "service bad") {
		t.Errorf("want one failure for service bad, got %v", failures)
	}
	if _, ok := mapping.Services["bad"]; ok {
		t.Error("failed service should be removed from the mapping")
	}
	if got := len(mapping.Resources); got != 1 {
		t.Errorf("want the good service's resource only, got %d resources", got)
	}
}

func TestInitAllServicesResourcesTimesOutAService(t *testing.T) {
	mapping := terraformutils.NewProvidersMapping(&fakeProvider{})
	options := ImportOptions{Resources: []string{"slow", "good"}, ListTimeout: 10 * time.Millisecond}

	failures, err := initAllServicesResources(t.Context(), mapping, options, nil)
	if err != nil {
		t.Fatalf("a slow service must not abort the run: %v", err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0].Error(), "service slow: listing took longer than --list-timeout 10ms") {
		t.Errorf("want the slow service reported, got %v", failures)
	}
	if got := len(mapping.Resources); got != 1 {
		t.Errorf("want the good service's resource only, got %d resources", got)
	}
}

// An interrupt stops listing.
func TestInitAllServicesResourcesStopsWhenInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	mapping := terraformutils.NewProvidersMapping(&fakeProvider{})

	_, err := initAllServicesResources(ctx, mapping, ImportOptions{Resources: []string{"slow", "good"}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want the run stopped, got %v", err)
	}
}

// fakeProvider lists one resource per service, fails for service "bad",
// and lists service "slow" until its context is done.
type fakeProvider struct {
	terraformutils.ProviderGenerator
	service *fakeService
}

func (p *fakeProvider) Init([]string) error { return nil }
func (p *fakeProvider) GetName() string     { return "fake" }
func (p *fakeProvider) GetService() terraformutils.ServiceGenerator {
	return p.service
}
func (p *fakeProvider) InitService(name string, _ bool) error {
	p.service = &fakeService{}
	p.service.SetName(name)
	return nil
}

type fakeService struct {
	terraformutils.Service
}

func (s *fakeService) InitResources() error {
	switch s.GetName() {
	case "bad":
		return errors.New("access denied")
	case "slow":
		// Like listers that log a failed call and go on.
		<-s.Context().Done()
		return nil
	}
	s.Resources = []terraformutils.Resource{
		terraformutils.NewSimpleResource("id-1", "one", "fake_thing", "fake"),
	}
	return nil
}
