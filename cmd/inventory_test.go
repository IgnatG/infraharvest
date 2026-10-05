// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// listingProvider fails the test if anything is listed.
type listingProvider struct {
	fakeProvider
	t *testing.T
}

func (p *listingProvider) InitService(name string, verbose bool) error {
	p.t.Errorf("listed %s, though the inventory has it", name)
	return p.fakeProvider.InitService(name, verbose)
}

func TestInventory(t *testing.T) {
	out := t.TempDir()
	args := []string{"eu-west-2", "default"}

	// discover lists and saves what it listed, with what failed.
	listed, failures, err := listResources(t.Context(), &fakeProvider{}, ImportOptions{Discover: true, PathOutput: out, Resources: []string{"good", "bad"}}, args)
	if err != nil || len(listed["good"]) != 1 || len(failures) != 1 {
		t.Fatalf("discover: %v, %v, %v", listed, failures, err)
	}

	// import --reuse-inventory takes it from there, failures included.
	listed, failures, err = listResources(t.Context(), &listingProvider{t: t}, ImportOptions{ReuseInventory: true, PathOutput: out, Resources: []string{"good", "bad"}}, args)
	if err != nil || len(listed["good"]) != 1 || listed["good"][0].InstanceState.ID != "id-1" {
		t.Fatalf("reuse: %v, %v", listed, err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0].Error(), "service bad") {
		t.Errorf("failures: %v", failures)
	}

	// Services the inventory doesn't list, another region, or another
	// --filter, are listed.
	for _, tc := range []struct {
		services []string
		args     []string
		filter   []string
	}{
		{[]string{"good", "other"}, args, nil},
		{[]string{"good"}, []string{"us-east-1", "default"}, nil},
		{[]string{"good"}, args, []string{"Name=tags.env;Value=prod"}},
	} {
		listed, _, err := listResources(t.Context(), &fakeProvider{}, ImportOptions{ReuseInventory: true, PathOutput: out, Resources: tc.services, Filter: tc.filter}, tc.args)
		if err != nil || len(listed["good"]) != 1 {
			t.Errorf("%v %v: %v, %v", tc.services, tc.args, listed, err)
		}
	}

	// The file holds every resource's attributes, like state: only the
	// owner reads it, and the role ARN of the call isn't in it.
	path := inventoryPath(out, "fake", args)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), `"args"`) {
		t.Errorf("the inventory keeps the call's arguments:\n%s", content)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != terraformutils.SecretFilePerm {
			t.Errorf("mode of %s: got %#o, want %#o", path, perm, terraformutils.SecretFilePerm)
		}
	}
}

// A discover with --filter lists a subset: an import without it, or with
// another, must not reuse it.
func TestInventoryKeepsTheFilter(t *testing.T) {
	out := t.TempDir()
	args := []string{"eu-west-2", "default"}
	filter := []string{"Name=tags.env;Value=prod", "Name=id;Value=vpc-1"}
	if _, _, err := listResources(t.Context(), &fakeProvider{}, ImportOptions{Discover: true, PathOutput: out, Resources: []string{"good"}, Filter: filter}, args); err != nil {
		t.Fatal(err)
	}

	// The same filter, in any order, reuses it.
	inv, err := loadInventory(inventoryPath(out, "fake", args), []string{"good"}, []string{filter[1], filter[0]})
	if err != nil || inv == nil {
		t.Fatalf("same filter: %v, %v", inv, err)
	}
	for name, other := range map[string][]string{"no filter": nil, "another filter": {"Name=tags.env;Value=dev"}} {
		if inv, err := loadInventory(inventoryPath(out, "fake", args), []string{"good"}, other); err != nil || inv != nil {
			t.Errorf("%s: reused the filtered inventory: %v, %v", name, inv, err)
		}
	}
}
