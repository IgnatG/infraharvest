// Copyright 2026 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package linode

import (
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/linode/linodego"
)

func checkResources(t *testing.T, got []terraformutils.Resource, wantType string, wantIDs ...string) {
	t.Helper()
	if len(got) != len(wantIDs) {
		t.Fatalf("got %d resources, want %d", len(got), len(wantIDs))
	}
	for i, r := range got {
		if r.InstanceInfo.Type != wantType {
			t.Errorf("resource %d: type %q, want %q", i, r.InstanceInfo.Type, wantType)
		}
		if r.InstanceState.ID != wantIDs[i] {
			t.Errorf("resource %d: ID %q, want %q", i, r.InstanceState.ID, wantIDs[i])
		}
	}
}

func TestStackScriptsSkipPublic(t *testing.T) {
	got := StackScriptGenerator{}.createResources([]linodego.Stackscript{
		{ID: 1, IsPublic: false},
		{ID: 2, IsPublic: true},
		{ID: 3},
	})
	checkResources(t, got, "linode_stackscript", "1", "3")
}

func TestRDNSUsesAddress(t *testing.T) {
	got := RDNSGenerator{}.createResources([]linodego.InstanceIP{{Address: "192.0.2.1"}})
	checkResources(t, got, "linode_rdns", "192.0.2.1")
}

func TestListerIDs(t *testing.T) {
	checkResources(t, ImageGenerator{}.createResources([]linodego.Image{{ID: "private/42"}}), "linode_image", "private/42")
	checkResources(t, InstanceGenerator{}.createResources([]linodego.Instance{{ID: 7}}), "linode_instance", "7")
	checkResources(t, SSHKeyGenerator{}.createResources([]linodego.SSHKey{{ID: 8}}), "linode_sshkey", "8")
	checkResources(t, TokenGenerator{}.createResources([]linodego.Token{{ID: 9}}), "linode_token", "9")
	checkResources(t, VolumeGenerator{}.createResources([]linodego.Volume{{ID: 10}}), "linode_volume", "10")
}
