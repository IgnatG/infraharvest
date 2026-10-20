// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// stackData is a data source the roots of a stack read, by its address
// (type.name), with its arguments merged as a resource's are (see
// mergeBody).
type stackData struct {
	address string
	body    *mergedBody
}

// mergeData merges the data sources the roots read, by address, as the
// merged root reads them. It returns why they don't merge, if they
// don't: one only some environments read, as their resources refer to
// it with its address, or one whose arguments don't merge.
func (m *stackMerge) mergeData() string {
	byAddress := map[string][]*envResource{}
	for e, root := range m.roots {
		for _, b := range dataBlocksOf(root.data) {
			if byAddress[b.address] == nil {
				byAddress[b.address] = make([]*envResource, len(m.roots))
			}
			typ, _, _ := strings.Cut(b.address, ".")
			byAddress[b.address][e] = &envResource{env: e, block: b, resourceType: typ}
		}
	}
	for _, address := range slices.Sorted(maps.Keys(byAddress)) {
		members := byAddress[address]
		if slices.Contains(members, nil) {
			return fmt.Sprintf("data.%s is read in some environments only", address)
		}
		typ, name, _ := strings.Cut(address, ".")
		body, reason := m.mergeBody(&stackResource{resourceType: typ, label: name, members: members})
		if reason != "" {
			return fmt.Sprintf("data.%s: %s", address, reason)
		}
		m.rootData = append(m.rootData, &stackData{address: address, body: body})
	}
	return ""
}
