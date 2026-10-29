// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/magodo/azlist/azlist"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/providers/providertest"
)

// A child type stays where the import keeps its resources, or those of a
// type under it, at any level; the others go, under every parent.
func TestWithoutUnkeptChildren(t *testing.T) {
	t.Parallel()
	entry := func(children azlist.ARMSchemaTree) *azlist.ARMSchemaEntry {
		return &azlist.ARMSchemaEntry{Children: children, Versions: []string{"2024-01-01"}}
	}
	settings := entry(azlist.ARMSchemaTree{})
	grandchild := entry(azlist.ARMSchemaTree{})
	middle := entry(azlist.ARMSchemaTree{"KEPT": grandchild, "SETTINGS": settings})
	kept := entry(azlist.ARMSchemaTree{"SETTINGS": settings})
	tree := azlist.ARMSchemaTree{
		"NS/PARENTS":                 entry(azlist.ARMSchemaTree{"KEPT": kept, "SETTINGS": settings, "MIDDLE": middle}),
		"NS/PARENTS/KEPT":            kept,
		"NS/PARENTS/SETTINGS":        settings,
		"NS/PARENTS/MIDDLE":          middle,
		"NS/PARENTS/MIDDLE/KEPT":     grandchild,
		"NS/PARENTS/MIDDLE/SETTINGS": settings,
	}
	keeps := func(armType string) bool { return strings.HasSuffix(armType, "/KEPT") }

	withoutUnkeptChildren(tree, keeps)

	for armType, want := range map[string][]string{
		"NS/PARENTS":        {"KEPT", "MIDDLE"},
		"NS/PARENTS/KEPT":   {},
		"NS/PARENTS/MIDDLE": {"KEPT"},
	} {
		if got := slices.Sorted(maps.Keys(tree[armType].Children)); !slices.Equal(got, want) {
			t.Errorf("%s: children %v, want %v", armType, got, want)
		}
	}
}

// The import keeps the types the catalogue's inventory entries or aztft
// map to an azurerm type it wants, and a workspace's tables, which the
// listing maps.
func TestKeptTypes(t *testing.T) {
	t.Parallel()
	cat := catalogue.MustBuiltin("azurerm")
	all := keptTypes(cat, func(string) bool { return true })
	for armType, want := range map[string]bool{
		"Microsoft.Web/sites/slots":                       true,
		"MICROSOFT.WEB/SITES/SLOTS":                       true,
		"Microsoft.Web/sites/hostNameBindings":            true,
		"Microsoft.Sql/servers/databases":                 true,
		"Microsoft.OperationalInsights/workspaces/tables": true,
		// Settings every site has, which azurerm has no type for.
		"Microsoft.Web/sites/config":         false,
		"Microsoft.Web/sites/siteextensions": false,
		"Microsoft":                          false,
	} {
		if got := all(armType); got != want {
			t.Errorf("keeps %s: %v, want %v", armType, got, want)
		}
	}
	noSlots := keptTypes(cat, func(typ string) bool { return !strings.HasSuffix(typ, "_slot") })
	if noSlots("Microsoft.Web/sites/slots") {
		t.Error("keeps slots where the import wants none of their types")
	}
	if !noSlots("Microsoft.Web/sites/hostNameBindings") {
		t.Error("doesn't keep host name bindings, which the import wants")
	}
}

// azlist lists only the child types resource_graph keeps under a site:
// one list each, not one for each of the 20 its schema has.
func TestResourceGraphListsOnlyKeptChildTypes(t *testing.T) {
	t.Parallel()
	const sub = "00000000-0000-0000-0000-000000000001"
	transport := providertest.New(answering(`{"value":[]}`))
	lister, err := azlist.NewLister(listerOption(sub, fakeCredential{}, *fakeOptions(transport)))
	if err != nil {
		t.Fatal(err)
	}
	site := "/subscriptions/" + sub + "/resourceGroups/rg/providers/Microsoft.Web/sites/app"
	if n := len(lister.ARMSchemaTree["MICROSOFT.WEB/SITES"].Children); n < 15 {
		t.Fatalf("azlist's schema has %d child types of a site; the test needs another parent", n)
	}

	withoutUnkeptChildren(lister.ARMSchemaTree, keptTypes(catalogue.MustBuiltin("azurerm"), func(string) bool { return true }))
	if _, err := lister.ListRemaining(t.Context(), []azlist.AzureResource{listed(t, site, nil)}); err != nil {
		t.Fatal(err)
	}

	var lists []string
	for _, p := range transport.Paths() {
		lists = append(lists, strings.ToLower(strings.TrimPrefix(p, site+"/")))
	}
	slices.Sort(lists)
	if want := []string{"functions", "hostnamebindings", "networkconfig", "publiccertificates", "slots"}; !slices.Equal(lists, want) {
		t.Errorf("listed %v under the site, want %v", lists, want)
	}
}
