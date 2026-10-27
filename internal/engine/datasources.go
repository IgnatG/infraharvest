// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// DataFileName holds the data sources of resources the configuration
// refers to but doesn't manage.
const DataFileName = "data.tf"

// External is a resource the import listed but doesn't manage, such as one
// the selection left out: the configuration may still refer to it. Name is
// the name it was listed by, from which the label of the data source that
// reads it is made; with none, the label is made from the ID.
type External struct {
	Type, ID string
	Name     string `json:",omitempty"`
}

// sameResource reports whether a and b are the same resource, whatever
// they are named.
func sameResource(a, b External) bool {
	return a.Type == b.Type && identityID(a.Type, a.ID) == identityID(b.Type, b.ID)
}

// dataLabel is the label of the data source that reads e: its name as a
// resource's label is made (see Label), without the words of its type
// (see withoutTypeWords), as in data.aws_vpc.shop for the VPC named
// shop-vpc; or, without a name, its ID, as in data.aws_vpc.vpc_0abc1234.
func dataLabel(e External) string {
	if e.Name == "" || e.Name == e.ID {
		return Label(e.ID)
	}
	return withoutTypeWords(e.Type, Label(e.Name))
}

// dataFileHeader starts data.tf.
const dataFileHeader = "# What this configuration refers to but doesn't manage: resources the\n# selection left out or other roots manage, and what the cloud or the\n# provider defines.\n"

// DataSource reads one resource of a type: the data source's type, and the
// argument that takes the resource's ID.
type DataSource struct {
	Type, Argument string
}

// addDataSources replaces literals in generated.tf that are the ID of an
// external resource with a reference to a data source that reads it, such
// as vpc_id = data.aws_vpc.vpc_0abc1234.id, and writes the data sources to
// data.tf. Like addReferences, a value counts if it identifies a resource
// on its own, or if the argument is named after the resource's type. Data
// sources are named other than those in taken. It reports whether it
// changed anything.
func addDataSources(dir string, external []External, sources map[string]DataSource, taken Names) (bool, error) {
	edited, err := addDataSourcesSkipping(dir, external, sources, taken, nil)
	return len(edited) > 0, err
}

// addDataSourcesSkipping is addDataSources for the resources not in skip
// (see verifyEach). It returns the resources it changed.
func addDataSourcesSkipping(dir string, external []External, sources map[string]DataSource, taken Names, skip map[string]int) (map[string]bool, error) {
	// In one order whatever the caller's, as the names depend on it, and
	// each resource once, with a name if any of its entries has one.
	external = slices.Clone(external)
	sortExternal(external)
	external = slices.CompactFunc(external, sameResource)
	index := map[string][]referenceTarget{}
	blocks := map[string]External{} // data address -> resource
	used := map[string]bool{}
	for name := range taken {
		if address, ok := strings.CutPrefix(name, "data."); ok {
			used[address] = true
		}
	}
	// An ID two resources with data sources share is ambiguous. Children,
	// such as a bucket's versioning, share their parent's ID, but have no
	// data source.
	ids := map[string]int{}
	for _, e := range external {
		if _, ok := sources[e.Type]; ok {
			ids[e.ID]++
		}
	}
	for _, e := range external {
		source, ok := sources[e.Type]
		if !ok || ids[e.ID] > 1 {
			continue
		}
		base := source.Type + "." + dataLabel(e)
		address := base
		for n := 2; used[address]; n++ {
			address = fmt.Sprintf("%s_%d", base, n)
		}
		used[address] = true
		index[e.ID] = []referenceTarget{{address: address, attribute: "id", data: true}}
		blocks[address] = e
	}
	if len(index) == 0 {
		return nil, nil
	}
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	r := &resolver{index: index, g: dependencies{}, src: generated.src}
	edited := map[string]bool{}
	for _, res := range generated.resources() {
		if skip[res.address] == 0 && r.attributes(res.address, res.syntax.Body, res.write.Body(), "", false) {
			edited[res.address] = true
		}
	}
	if len(edited) == 0 {
		return nil, nil
	}
	if err := generated.save(); err != nil {
		return nil, err
	}
	// Write the data sources the configuration now refers to.
	saved, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	referred := map[string]bool{}
	for _, res := range saved.resources() {
		for _, t := range allTraversals(res.syntax.Body) {
			if t.RootName() != "data" || len(t) < 3 {
				continue
			}
			typ, ok1 := t[1].(hcl.TraverseAttr)
			name, ok2 := t[2].(hcl.TraverseAttr)
			if ok1 && ok2 {
				referred[typ.Name+"."+name.Name] = true
			}
		}
	}
	addresses := make([]string, 0, len(referred))
	for address := range referred {
		if _, ok := blocks[address]; ok {
			addresses = append(addresses, address)
		}
	}
	sort.Strings(addresses)
	f := hclwrite.NewEmptyFile()
	f.Body().AppendUnstructuredTokens(hclwrite.Tokens{{Type: hclsyntax.TokenComment, Bytes: []byte(dataFileHeader)}})
	for _, address := range addresses {
		e := blocks[address]
		typ, name, _ := strings.Cut(address, ".")
		f.Body().AppendNewline()
		block := f.Body().AppendNewBlock("data", []string{typ, name})
		block.Body().SetAttributeValue(sources[e.Type].Argument, cty.StringVal(e.ID))
	}
	return edited, fsutil.WriteFile(filepath.Join(dir, DataFileName), hclwrite.Format(f.Bytes()), 0o644)
}
