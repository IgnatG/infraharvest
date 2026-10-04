// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"strings"
	"testing"
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

	// Services the inventory doesn't list, or another region, are listed.
	for _, tc := range []struct {
		services []string
		args     []string
	}{
		{[]string{"good", "other"}, args},
		{[]string{"good"}, []string{"us-east-1", "default"}},
	} {
		listed, _, err := listResources(t.Context(), &fakeProvider{}, ImportOptions{ReuseInventory: true, PathOutput: out, Resources: tc.services}, tc.args)
		if err != nil || len(listed["good"]) != 1 {
			t.Errorf("%v %v: %v, %v", tc.services, tc.args, listed, err)
		}
	}
}
