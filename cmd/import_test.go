package cmd

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/terraformer/terraformutils"
	"github.com/GoogleCloudPlatform/terraformer/terraformutils/providerwrapper"
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

	if err := checkFailures(failures, true); err != nil {
		t.Errorf("failures with --allow-partial: want nil, got %v", err)
	}
}

func TestInitAllServicesResourcesReportsFailedServices(t *testing.T) {
	mapping := terraformutils.NewProvidersMapping(&fakeProvider{})
	options := ImportOptions{Resources: []string{"good", "bad"}}

	failures, err := initAllServicesResources(mapping, options, nil, nil)
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

func TestRelativeStatePath(t *testing.T) {
	abs := t.TempDir()
	tests := map[string]struct {
		output  string
		pattern string
	}{
		"default pattern":                   {output: DefaultPathOutput, pattern: DefaultPathPattern},
		"output dir contains service name":  {output: "sqs-export", pattern: DefaultPathPattern},
		"absolute output dir":               {output: abs, pattern: DefaultPathPattern},
		"absolute output contains service":  {output: filepath.Join(abs, "sqs"), pattern: DefaultPathPattern},
		"service before provider in layout": {output: "out", pattern: "{output}/{service}/{provider}/"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			from := Path(tc.pattern, "aws", "sqs", tc.output)
			to := Path(tc.pattern, "aws", "sns", tc.output)

			got, err := relativeStatePath(from, to)
			if err != nil {
				t.Fatal(err)
			}

			if strings.Contains(got, `\`) {
				t.Errorf("path %q must use forward slashes", got)
			}
			resolved := filepath.Clean(filepath.Join(from, filepath.FromSlash(got)))
			want := filepath.Clean(filepath.Join(to, "terraform.tfstate"))
			if resolved != want {
				t.Errorf("path %q resolves to %q from %q, want %q", got, resolved, from, want)
			}
		})
	}
}

// fakeProvider lists one resource per service and fails for service "bad".
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
	if s.GetName() == "bad" {
		return errors.New("access denied")
	}
	s.Resources = []terraformutils.Resource{
		terraformutils.NewSimpleResource("id-1", "one", "fake_thing", "fake", nil),
	}
	return nil
}

func (s *fakeService) PopulateIgnoreKeys(*providerwrapper.ProviderWrapper) {}
