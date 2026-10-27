// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// listingProvider fails the test if anything is listed.
type listingProvider struct {
	fakeProvider
	t *testing.T
}

func (p *listingProvider) Select(name string, verbose bool) error {
	p.t.Errorf("listed %s, though the inventory has it", name)
	return p.fakeProvider.Select(name, verbose)
}

func TestInventory(t *testing.T) {
	out := t.TempDir()
	args := []string{"eu-west-2", "default"}

	// discover lists and saves what it listed, with what failed.
	listed, failures, err := listResources(t.Context(), &fakeProvider{}, callOf(Options{Discover: true, Out: out, Services: []string{"good", "bad"}}, "fake/1/eu-west-2"), args)
	if err != nil || len(listed.resources["good"]) != 1 || len(failures) != 1 {
		t.Fatalf("discover: %v, %v, %v", listed, failures, err)
	}

	// import --reuse-inventory takes it from there, failures included.
	listed, failures, err = listResources(t.Context(), &listingProvider{t: t}, callOf(Options{ReuseInventory: true, Out: out, Services: []string{"good", "bad"}}, "fake/1/eu-west-2"), args)
	if err != nil || len(listed.resources["good"]) != 1 || listed.resources["good"][0].Observed.ID != "id-1" {
		t.Fatalf("reuse: %v, %v", listed, err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0].Error(), "service bad") {
		t.Errorf("failures: %v", failures)
	}

	// Services the inventory doesn't list, another region, or another
	// filter, are listed.
	for _, tc := range []struct {
		services []string
		args     []string
		tags     []string
	}{
		{[]string{"good", "other"}, args, nil},
		{[]string{"good"}, []string{"us-east-1", "default"}, nil},
		{[]string{"good"}, args, []string{"env=prod"}},
	} {
		listed, _, err := listResources(t.Context(), &fakeProvider{}, callOf(Options{ReuseInventory: true, Out: out, Services: tc.services, TagFilters: tc.tags}, ""), tc.args)
		if err != nil || len(listed.resources["good"]) != 1 {
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
		if perm := info.Mode().Perm(); perm != harvest.SecretFilePerm {
			t.Errorf("mode of %s: got %#o, want %#o", path, perm, harvest.SecretFilePerm)
		}
	}
}

// A discover with --tag or --where lists a subset: an import without it, or with
// another, must not reuse it.
func TestInventoryKeepsTheFilter(t *testing.T) {
	out := t.TempDir()
	args := []string{"eu-west-2", "default"}
	options := Options{Discover: true, Out: out, Services: []string{"good"}, TagFilters: []string{"env=prod"}, WhereFilters: []string{"cidr_block=10.0.0.0/16"}}
	filter := options.filterArgs()
	if _, _, err := listResources(t.Context(), &fakeProvider{}, callOf(options, ""), args); err != nil {
		t.Fatal(err)
	}

	// The same filter, in any order, reuses it.
	inv, err := loadInventory(inventoryPath(out, "fake", args), []string{"good"}, []string{filter[1], filter[0]})
	if err != nil || inv == nil {
		t.Fatalf("same filter: %v, %v", inv, err)
	}
	for name, other := range map[string][]string{"no filter": nil, "another filter": {"--tag=env=dev"}} {
		if inv, err := loadInventory(inventoryPath(out, "fake", args), []string{"good"}, other); err != nil || inv != nil {
			t.Errorf("%s: reused the filtered inventory: %v, %v", name, inv, err)
		}
	}
}

// taggingProvider reads one tag, env=dev, for every resource.
type taggingProvider struct {
	fakeProvider
}

func (p *taggingProvider) Tags(_ context.Context, resources []harvest.Resource) (map[string]map[string]string, error) {
	tags := map[string]map[string]string{}
	for _, r := range resources {
		tags[tagKey(r)] = map[string]string{"env": "dev"}
	}
	return tags, nil
}

func TestInventoryKeepsTags(t *testing.T) {
	out := t.TempDir()
	args := []string{"eu-west-2", "default"}
	options := Options{Discover: true, Out: out, Services: []string{"good"}}
	listed, _, err := listResources(t.Context(), &taggingProvider{}, callOf(options, "fake/1/eu-west-2"), args)
	if err != nil {
		t.Fatal(err)
	}
	res := listed.resources["good"][0]
	if listed.tagsOf(res)["env"] != "dev" {
		t.Fatalf("listed tags: %v", listed.tags)
	}

	inv, err := loadInventory(inventoryPath(out, "fake", args), []string{"good"}, nil)
	if err != nil || inv == nil {
		t.Fatalf("saved: %v, %v", inv, err)
	}
	if inv.Scope != "fake/1/eu-west-2" || len(inv.Records) != 1 {
		t.Fatalf("saved inventory: %+v", inv)
	}
	if r := inv.Records[0]; r.Service != "good" || r.Type != "fake_thing" || r.ID != "id-1" || r.Name != "one" || r.Tags["env"] != "dev" {
		t.Errorf("record: %+v", r)
	}

	// Reused, the resources are as listed, tags included.
	options = Options{ReuseInventory: true, Out: out, Services: []string{"good"}}
	reused, _, err := listResources(t.Context(), &listingProvider{t: t}, callOf(options, "fake/1/eu-west-2"), args)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reused.resources["good"], listed.resources["good"]) {
		t.Errorf("reused %+v, listed %+v", reused.resources["good"][0], listed.resources["good"][0])
	}
	if reused.tagsOf(reused.resources["good"][0])["env"] != "dev" {
		t.Errorf("reused tags: %v", reused.tags)
	}

	// Discover writes the tags into the selection file, and a rule by tag
	// leaves the resource out of an import.
	run := NewRun()
	run.addDiscovered(listed, sourcesOf(t, listed, nil), "fake/1/eu-west-2", func(r harvest.Resource) (string, bool) { return r.Observed.ID, true })
	if len(run.listed) != 1 || run.listed[0].Tags["env"] != "dev" {
		t.Errorf("selection entries: %+v", run.listed)
	}
	f := &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}, Rules: []selection.Rule{{Exclude: &selection.Match{Tags: map[string]selection.Patterns{"env": {"dev"}}}}}}
	selected, _ := run.selectResources(listed, sourcesOf(t, listed, nil), f, "fake/1/eu-west-2", "", func(r harvest.Resource) (string, bool) { return r.Observed.ID, true })
	if len(selected["good"]) != 0 {
		t.Errorf("selected %v, want the dev resource left out", selected)
	}
}

// Tags the listers record in their attributes count too.
func TestReadTagsFromAttributes(t *testing.T) {
	r := harvest.Record("id-1", "one", "fake_thing", "fake", map[string]string{"tags.%": "1", "tags.team": "data"})
	tags, err := readTags(t.Context(), &taggingProvider{}, map[string][]harvest.Resource{"good": {r}})
	if err != nil {
		t.Fatal(err)
	}
	if got := tags[tagKey(r)]; got["team"] != "data" || got["env"] != "dev" {
		t.Errorf("tags: %v, want team from the attributes and env from the provider", got)
	}
}

// countingProvider counts the services it lists in countingListed: the
// registry lists each service with a provider of its own, the zero value
// of this type, so the count can't be a field.
type countingProvider struct {
	fakeProvider
}

var countingListed atomic.Int32

func (p *countingProvider) Select(name string, verbose bool) error {
	countingListed.Add(1)
	return p.fakeProvider.Select(name, verbose)
}

// The saved inventory of the same arguments is of another account or
// region when the scope they resolve to differs, as with another region in
// the environment: it is listed again.
func TestInventoryOfAnotherScope(t *testing.T) {
	out := t.TempDir()
	args := []string{"", "default"}
	if _, _, err := listResources(t.Context(), &fakeProvider{}, callOf(Options{Discover: true, Out: out, Services: []string{"good"}}, "fake/1/eu-west-1"), args); err != nil {
		t.Fatal(err)
	}
	countingListed.Store(0)
	if _, _, err := listResources(t.Context(), &countingProvider{}, callOf(Options{ReuseInventory: true, Out: out, Services: []string{"good"}}, "fake/1/eu-west-2"), args); err != nil {
		t.Fatal(err)
	}
	if n := countingListed.Load(); n != 1 {
		t.Errorf("listed %d services, want the inventory of eu-west-1 not reused for eu-west-2", n)
	}
}

// failingTagsProvider can't read tags.
type failingTagsProvider struct {
	fakeProvider
}

func (p *failingTagsProvider) Tags(context.Context, []harvest.Resource) (map[string]map[string]string, error) {
	return nil, errors.New("tag:GetResources denied")
}

// Tags that can't be read fail the call, which goes on with what it has,
// and an import that reuses the inventory fails the same way.
func TestTagsThatFailToRead(t *testing.T) {
	out := t.TempDir()
	args := []string{"eu-west-2", "default"}
	listed, failures, err := listResources(t.Context(), &failingTagsProvider{}, callOf(Options{Discover: true, Out: out, Services: []string{"good"}}, "fake/1/eu-west-2"), args)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.resources["good"]) != 1 || len(failures) != 1 || !strings.HasPrefix(failures[0].Error(), tagsFailure) || !strings.Contains(failures[0].Error(), "denied") {
		t.Fatalf("listed %v, failures %v", listed.resources, failures)
	}
	_, failures, err = listResources(t.Context(), &listingProvider{t: t}, callOf(Options{ReuseInventory: true, Out: out, Services: []string{"good"}}, "fake/1/eu-west-2"), args)
	if err != nil || len(failures) != 1 || !strings.HasPrefix(failures[0].Error(), tagsFailure) {
		t.Errorf("reused: failures %v, %v", failures, err)
	}
}
