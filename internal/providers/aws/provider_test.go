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

var _ harvest.ProviderWithCatalogue = (*Provider)(nil)

// The services of AWS services at end of support are services, which *
// leaves out: they are imported only when named.
func TestEndOfSupportServicesAreOptIn(t *testing.T) {
	p := &Provider{}
	services := p.Services()
	wildcard := importer.Services(p)
	for _, name := range p.Profile().OptInServices {
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

// An environment's credentials give the account and the region, and the
// partition, with its DNS suffix, of commercial, China and GovCloud
// regions alone; nothing that isn't known.
func TestEnvironmentData(t *testing.T) {
	t.Parallel()
	partition := func(name, suffix string) map[string]string {
		return map[string]string{"partition": name, "id": name, "dns_suffix": suffix}
	}
	account := map[string]string{"account_id": "111111111111", "id": "111111111111"}
	region := func(name string) map[string]string {
		return map[string]string{"region": name, "name": name, "id": name}
	}
	for _, c := range []struct {
		account, region string
		want            harvest.EnvironmentData
	}{
		{"111111111111", "eu-west-2", harvest.EnvironmentData{"aws_caller_identity": account, "aws_region": region("eu-west-2"), "aws_partition": partition("aws", "amazonaws.com")}},
		{"111111111111", "cn-north-1", harvest.EnvironmentData{"aws_caller_identity": account, "aws_region": region("cn-north-1"), "aws_partition": partition("aws-cn", "amazonaws.com.cn")}},
		{"", "us-gov-west-1", harvest.EnvironmentData{"aws_region": region("us-gov-west-1"), "aws_partition": partition("aws-us-gov", "amazonaws.com")}},
		{"", "us-iso-east-1", harvest.EnvironmentData{"aws_region": region("us-iso-east-1")}},
		// The European Sovereign Cloud's partition isn't aws's.
		{"", "eusc-de-east-1", harvest.EnvironmentData{"aws_region": region("eusc-de-east-1")}},
		{"111111111111", "", harvest.EnvironmentData{"aws_caller_identity": account}},
	} {
		if got := (&Provider{}).Profile().Merge.EnvironmentData(c.account, c.region); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q, %q: got %v, want %v", c.account, c.region, got, c.want)
		}
	}
}
