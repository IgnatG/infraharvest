// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"strings"
	"testing"
)

// A tag with an empty value is common only to the resources that have it,
// and a resource keeps its own empty tag where the common tags lack it.
func TestVPCTagsEmptyValues(t *testing.T) {
	t.Parallel()
	vpc := &vpcPart{tags: map[string]string{"Team": "net", "Foo": ""}}
	subnet := &vpcPart{tags: map[string]string{"Team": "net"}}
	table := &vpcPart{tags: map[string]string{"Team": "net", "Bar": ""}}
	call := &Call{}
	err := vpcTags(call, []*vpcPart{vpc, subnet, table}, map[string][]*vpcPart{
		"vpc_tags": {vpc}, "public_route_table_tags": {table},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range call.Arguments {
		got[a.Name] = strings.Join(strings.Fields(string(a.Value.Bytes())), " ")
	}
	if strings.Contains(got["tags"], "Foo") {
		t.Errorf("tags = %s: Foo is only the VPC's", got["tags"])
	}
	if !strings.Contains(got["vpc_tags"], "Foo") {
		t.Errorf("vpc_tags = %q: want the VPC's own Foo", got["vpc_tags"])
	}
	if !strings.Contains(got["public_route_table_tags"], "Bar") {
		t.Errorf("public_route_table_tags = %q: want the table's own Bar", got["public_route_table_tags"])
	}
}
