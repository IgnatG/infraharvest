// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// GoogleKMS maps a key ring and its crypto keys onto
// terraform-google-modules/kms/google. The module gives every key of the
// ring the same settings (purpose, rotation, algorithm, protection level,
// labels), so it declines rings whose keys differ, and it keeps the keys
// from being destroyed (prevent_destroy).
var GoogleKMS = Adapter{
	Source:   "terraform-google-modules/kms/google",
	Version:  "4.2.1",
	Anchor:   "google_kms_key_ring",
	Members:  []string{"google_kms_crypto_key"},
	Repeated: []string{"google_kms_crypto_key"},
	Inputs: []string{
		"crypto_key_backend", "import_only", "key_algorithm", "key_destroy_scheduled_duration",
		"key_protection_level", "key_rotation_period", "keyring", "keys", "labels", "location",
		"project_id", "purpose", "skip_initial_version_creation",
	},
	Outputs: []string{"keyring", "keyring_name", "keys"},
	Map:     mapGoogleKMS,
}

// kmsKeySettings are the crypto key arguments the module sets the same for
// every key, with its input for each, and the value to pass when the keys
// don't set one, where the module's default differs from the provider's.
var kmsKeySettings = []struct {
	arg, input string
	unset      cty.Value
}{
	{"purpose", "purpose", cty.NilVal},
	{"rotation_period", "key_rotation_period", cty.NullVal(cty.String)},
	{"import_only", "import_only", cty.NilVal},
	{"skip_initial_version_creation", "skip_initial_version_creation", cty.NilVal},
	{"crypto_key_backend", "crypto_key_backend", cty.NilVal},
	{"destroy_scheduled_duration", "key_destroy_scheduled_duration", cty.NilVal},
	{"labels", "labels", cty.NilVal},
}

func mapGoogleKMS(c Cluster) (*Call, error) {
	call := &Call{
		Addresses: map[string]string{c.Anchor.Address(): "google_kms_key_ring.key_ring"},
		Outputs: map[string]map[string]string{c.Anchor.Address(): {
			"id":   "keyring",
			"name": "keyring_name",
		}},
	}
	ring := Read(c.Anchor.Address(), c.Anchor.Body)
	for _, required := range []struct{ arg, input string }{{"name", "keyring"}, {"project", "project_id"}, {"location", "location"}} {
		value, ok := ring.Attr(required.arg)
		if !ok {
			return nil, Declinef("%s has no %s", c.Anchor.Address(), required.arg)
		}
		call.Set(required.input, value)
	}
	if err := ring.Done(); err != nil {
		return nil, err
	}

	// The keys in name order, as the module indexes them.
	type key struct {
		name     string
		resource Resource
		settings map[string]hclwrite.Tokens
	}
	var keys []key
	for _, m := range c.Members {
		r := Read(m.Address(), m.Body)
		if !r.RefersTo("key_ring", c.Anchor) {
			return nil, Declinef("%s isn't in %s", m.Address(), c.Anchor.Address())
		}
		name, ok := literalString(mustAttr(r, "name"))
		if !ok {
			return nil, Declinef("%s has no literal name", m.Address())
		}
		k := key{name: name, resource: m, settings: map[string]hclwrite.Tokens{}}
		for _, s := range kmsKeySettings {
			if value, ok := r.Attr(s.arg); ok {
				k.settings[s.input] = value
			}
		}
		template, err := r.Block("version_template")
		if err != nil {
			return nil, err
		}
		if template != nil {
			for _, s := range []struct{ arg, input string }{{"algorithm", "key_algorithm"}, {"protection_level", "key_protection_level"}} {
				if value, ok := template.Attr(s.arg); ok {
					k.settings[s.input] = value
				}
			}
		}
		// Terraform's own setting, which an import records at its default.
		r.IgnoreDeletionPolicy()
		if err := r.Done(); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int { return strings.Compare(a.name, b.name) })

	names := make([]string, 0, len(keys))
	for i, k := range keys {
		if i > 0 && !sameSettings(k.settings, keys[0].settings) {
			return nil, Declinef("%s and %s have different settings, and the module sets them for every key of the ring", keys[0].resource.Address(), k.resource.Address())
		}
		if i > 0 && k.name == keys[i-1].name {
			return nil, Declinef("%s has two keys named %s", c.Anchor.Address(), k.name)
		}
		names = append(names, k.name)
		call.Addresses[k.resource.Address()] = fmt.Sprintf("google_kms_crypto_key.key[%d]", i)
		call.Outputs[k.resource.Address()] = map[string]string{"id": "keys[" + strconv.Quote(k.name) + "]"}
	}
	call.Set("keys", hclwrite.TokensForValue(stringList(names)))
	if len(keys) > 0 {
		for _, s := range kmsKeySettings {
			value, ok := keys[0].settings[s.input]
			switch {
			case ok:
				call.Set(s.input, value)
			case s.unset != cty.NilVal:
				call.Set(s.input, hclwrite.TokensForValue(s.unset))
			}
		}
		for _, input := range []string{"key_algorithm", "key_protection_level"} {
			if value, ok := keys[0].settings[input]; ok {
				call.Set(input, value)
			}
		}
	}
	return call, nil
}

// sameSettings reports whether two keys' settings are the same.
func sameSettings(a, b map[string]hclwrite.Tokens) bool {
	return maps.EqualFunc(a, b, sameTokens)
}
