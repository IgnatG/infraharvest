// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"context"
	"reflect"
	"testing"
)

// registryListings are what registryProvider's services list. A service that
// is not here lists nothing.
var registryListings = map[string][]Resource{
	"vpc": {
		RecordBare("vpc-1", "one", "aws_vpc", "aws"),
		RecordBare("vpc-2", "two", "aws_vpc", "aws"),
	},
	"subnet": {RecordBare("subnet-1", "one", "aws_subnet", "aws")},
	"bad":    {RecordBare("bad-1", "bad", "aws_bad", "aws")},
}

// registryProvider is a provider whose services list registryListings. Its
// fields show whether Registry copies anything from a base provider.
type registryProvider struct {
	ProviderBase
	region      string
	initialised bool
}

func (p *registryProvider) Init(_ context.Context, args []string) error {
	p.initialised = true
	if len(args) > 0 {
		p.region = args[0]
	}
	return nil
}

func (p *registryProvider) Select(serviceName string, _ bool) error {
	service := &registryService{}
	service.Configure(ListerConfig{Service: serviceName})
	service.SetListed(registryListings[serviceName])
	p.Current = service
	return nil
}

func (*registryProvider) Name() string                          { return "registry" }
func (*registryProvider) Services() map[string]Lister           { return nil }
func (*registryProvider) ProviderBlock() map[string]interface{} { return nil }

type registryService struct{ ListerBase }

func (*registryService) List() error { return nil }

var _ Provider = (*registryProvider)(nil)

func TestNewRegistryIsEmpty(t *testing.T) {
	m := NewRegistry(&registryProvider{})
	if m.Resources == nil || m.Services == nil || m.Providers == nil {
		t.Fatalf("want non-nil sets, got Resources %v, Services %v, Providers %v", m.Resources, m.Services, m.Providers)
	}
	if len(m.Resources)+len(m.Services)+len(m.Providers) != 0 {
		t.Errorf("want empty sets, got Resources %v, Services %v, Providers %v", m.Resources, m.Services, m.Providers)
	}
	if got := m.ByService(); len(got) != 0 {
		t.Errorf("ByService: got %v, want nothing", got)
	}
}

// Every service gets a new provider of the base's type, with nothing of the
// base's state and not yet initialised.
func TestRegisterMakesNewProviders(t *testing.T) {
	base := &registryProvider{region: "eu-west-1", initialised: true}
	m := NewRegistry(base)

	vpc := m.Register("vpc")
	subnet := m.Register("subnet")

	for name, provider := range map[string]Provider{"vpc": vpc, "subnet": subnet} {
		concrete, ok := provider.(*registryProvider)
		if !ok {
			t.Fatalf("%s: got a %T, want a *registryProvider", name, provider)
		}
		if concrete == base {
			t.Errorf("%s: got the base provider, want a new one", name)
		}
		if *concrete != (registryProvider{}) {
			t.Errorf("%s: got %+v, want a zero value", name, *concrete)
		}
	}
	if vpc == subnet {
		t.Error("two services share a provider")
	}
	if want := map[string]bool{"vpc": true, "subnet": true}; !reflect.DeepEqual(m.Services, want) {
		t.Errorf("Services: got %v, want %v", m.Services, want)
	}
	if want := map[Provider]bool{vpc: true, subnet: true}; !reflect.DeepEqual(m.Providers, want) {
		t.Errorf("Providers: got %v, want %v", m.Providers, want)
	}
}

// The resources of the services that were not removed are collected under
// their service, in the lister's order. A removed service's provider is
// never touched: here it has no current service, which would panic.
func TestRegistryCollectsByService(t *testing.T) {
	m := NewRegistry(&registryProvider{})
	for _, service := range []string{"vpc", "none", "subnet", "bad"} {
		provider := m.Register(service)
		if err := provider.Init(t.Context(), []string{"eu-west-1"}); err != nil {
			t.Fatal(err)
		}
		if service == "bad" {
			continue // as if Select had failed
		}
		if err := provider.Select(service, false); err != nil {
			t.Fatal(err)
		}
	}

	m.Unregister([]string{"bad"})
	if want := map[string]bool{"vpc": true, "none": true, "subnet": true}; !reflect.DeepEqual(m.Services, want) {
		t.Errorf("Services after removing bad: got %v, want %v", m.Services, want)
	}
	if got := len(m.Providers); got != 3 {
		t.Errorf("Providers after removing bad: got %d, want 3", got)
	}

	m.Collect()
	if got := len(m.Resources); got != 3 {
		t.Errorf("Resources: got %d, want 3", got)
	}

	want := map[string][]Resource{
		"vpc":    registryListings["vpc"],
		"subnet": registryListings["subnet"],
		"none":   {}, // present and empty, not nil
	}
	for call := 1; call <= 2; call++ {
		if got := m.ByService(); !reflect.DeepEqual(got, want) {
			t.Errorf("ByService, call %d: got %v, want %v", call, got, want)
		}
	}
}

func TestUnregisterIgnoresUnknownNames(t *testing.T) {
	m := NewRegistry(&registryProvider{})
	provider := m.Register("vpc")

	m.Unregister(nil)
	m.Unregister([]string{})
	m.Unregister([]string{"subnet"})

	if want := map[string]bool{"vpc": true}; !reflect.DeepEqual(m.Services, want) {
		t.Errorf("Services: got %v, want %v", m.Services, want)
	}
	if want := map[Provider]bool{provider: true}; !reflect.DeepEqual(m.Providers, want) {
		t.Errorf("Providers: got %v, want %v", m.Providers, want)
	}
}

// A service added twice has two providers, and removing it only drops the
// newer: the older one's resources are still collected under its name.
func TestRegistryServiceRegisteredTwice(t *testing.T) {
	m := NewRegistry(&registryProvider{})
	older := m.Register("vpc")
	newer := m.Register("vpc")
	for _, provider := range []Provider{older, newer} {
		if err := provider.Select("vpc", false); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(m.Providers); got != 2 {
		t.Fatalf("Providers: got %d, want 2", got)
	}

	m.Unregister([]string{"vpc"})
	if len(m.Services) != 0 {
		t.Errorf("Services: got %v, want none", m.Services)
	}
	if want := map[Provider]bool{older: true}; !reflect.DeepEqual(m.Providers, want) {
		t.Errorf("Providers: got %v, want only the older one", m.Providers)
	}

	m.Collect()
	if want := map[string][]Resource{"vpc": registryListings["vpc"]}; !reflect.DeepEqual(m.ByService(), want) {
		t.Errorf("ByService: got %v, want %v", m.ByService(), want)
	}
}
