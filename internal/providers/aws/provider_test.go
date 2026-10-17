// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"reflect"
	"slices"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/importer"
)

var _ harvest.ProviderWithOptInServices = (*Provider)(nil)

// The services of AWS services at end of support are services, which *
// leaves out: they are imported only when named.
func TestEndOfSupportServicesAreOptIn(t *testing.T) {
	p := &Provider{}
	services := p.Services()
	wildcard := importer.Services(p)
	for _, name := range p.OptInServices() {
		if _, ok := services[name]; !ok {
			t.Errorf("opt-in service %s isn't a service", name)
		}
		if slices.Contains(wildcard, name) {
			t.Errorf("* lists %s", name)
		}
	}
	if !slices.Contains(wildcard, "cloud9") || !slices.Contains(wildcard, "wafv2_regional") {
		t.Errorf("* leaves out services still supported: %v", wildcard)
	}
}

// Select makes only the lister of the service it names, the one Services
// has for it, and refuses a name that isn't a service.
func TestSelectMakesTheServicesLister(t *testing.T) {
	p := &Provider{}
	for name, want := range p.Services() {
		if err := p.Select(name, false); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, ok := p.Current.(*guardedLister)
		if !ok || reflect.TypeOf(got.lister) != reflect.TypeOf(want.(*guardedLister).lister) {
			t.Errorf("%s: selected %T, want %T", name, p.Current, want)
		}
	}
	if err := p.Select("no-such-service", false); err == nil {
		t.Error("want an error for a service that doesn't exist")
	}
}
