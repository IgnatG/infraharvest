// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"reflect"
	"testing"
)

func TestParseInputPath(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]InputPath{
		"tags":               {Variable: "tags"},
		"policies[each.key]": {Variable: "policies", Steps: []InputStep{{EachKey: true}}},
		"federated_identity_credentials[each.key].audience": {Variable: "federated_identity_credentials", Steps: []InputStep{{EachKey: true}, {Key: "audience"}}},
		`rules["a.b"][0]`: {Variable: "rules", Steps: []InputStep{{Key: "a.b"}, {Index: 0, IsIndex: true}}},
	} {
		got, err := ParseInputPath(path)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v, %v, want %+v", path, got, err, want)
		}
	}
	for _, path := range []string{"", "1tags", "tags[", "tags[x]", "tags..a", "tags[-1]"} {
		if _, err := ParseInputPath(path); err == nil {
			t.Errorf("%q parses", path)
		}
	}
}

func TestInverseTarget(t *testing.T) {
	t.Parallel()
	a := Adapter{Targets: []string{"aws_iam_role.this", "module.vpc.google_compute_network.network"}}
	for key, want := range map[string]string{
		"aws_iam_role.this.tags":                                "aws_iam_role.this tags",
		"module.vpc.google_compute_network.network.description": "module.vpc.google_compute_network.network description",
	} {
		target, attribute, ok := a.InverseTarget(key)
		if !ok || target+" "+attribute != want {
			t.Errorf("%s: got %s %s %v", key, target, attribute, ok)
		}
	}
	if _, _, ok := a.InverseTarget("aws_iam_role.other.tags"); ok {
		t.Error("an attribute of no target has one")
	}
}
