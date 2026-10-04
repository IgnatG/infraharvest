// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// DataFileName holds the data sources of resources the configuration
// refers to but doesn't manage.
const DataFileName = "data.tf"

// External is a resource the import listed but doesn't manage, such as one
// the selection left out: the configuration may still refer to it.
type External struct {
	Type, ID string
}

// DataSource reads one resource of a type: the data source's type, and the
// argument that takes the resource's ID.
type DataSource struct {
	Type, Argument string
}

// addDataSources replaces literals in generated.tf that are the ID of an
// external resource with a reference to a data source that reads it, such
// as vpc_id = data.aws_vpc.vpc_0abc1234.id, and writes the data sources to
// data.tf. Like addReferences, a value counts if it identifies a resource
// on its own, or if the argument is named after the resource's type. It
// reports whether it changed anything.
func addDataSources(dir string, external []External, sources map[string]DataSource) (bool, error) {
	index := map[string]referenceTarget{}
	blocks := map[string]External{} // data address -> resource
	used := map[string]bool{}
	ids := map[string]int{}
	for _, e := range external {
		ids[e.ID]++
	}
	for _, e := range external {
		source, ok := sources[e.Type]
		if !ok || ids[e.ID] > 1 {
			continue
		}
		base := source.Type + "." + Label(e.ID)
		address := base
		for n := 2; used[address]; n++ {
			address = fmt.Sprintf("%s_%d", base, n)
		}
		used[address] = true
		index[e.ID] = referenceTarget{address: address, attribute: "id", data: true}
		blocks[address] = e
	}
	if len(index) == 0 {
		return false, nil
	}
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return false, err
	}
	g := dependencies{}
	changed := false
	for _, r := range generated.resources() {
		if referenceAttributes(r.address, r.syntax.Body, r.write.Body(), index, g, generated.src) {
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	if err := generated.save(); err != nil {
		return false, err
	}
	// Write the data sources the configuration now refers to.
	saved, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return false, err
	}
	referred := map[string]bool{}
	for _, r := range saved.resources() {
		for _, t := range allTraversals(r.syntax.Body) {
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
	f.Body().AppendUnstructuredTokens(hclwrite.Tokens{{Type: hclsyntax.TokenComment, Bytes: []byte("# Resources this configuration refers to but doesn't manage: the import\n# listed them, and the selection left them out.\n")}})
	for _, address := range addresses {
		e := blocks[address]
		typ, name, _ := strings.Cut(address, ".")
		f.Body().AppendNewline()
		block := f.Body().AppendNewBlock("data", []string{typ, name})
		block.Body().SetAttributeValue(sources[e.Type].Argument, cty.StringVal(e.ID))
	}
	return true, os.WriteFile(filepath.Join(dir, DataFileName), hclwrite.Format(f.Bytes()), 0o644)
}
