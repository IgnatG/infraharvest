// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/secret"
)

// driftSensitiveForms returns the values the plans mark sensitive, in
// the forms text quotes them in (see secret.TextForms): no report may hold one.
func driftSensitiveForms(plans ...*tfjson.Plan) []string {
	seen := map[string]bool{}
	forEachChange(plans, func(c *tfjson.Change) {
		secret.CollectSensitive(c.Before, c.BeforeSensitive, seen)
		secret.CollectSensitive(c.After, c.AfterSensitive, seen)
	})
	values := make([]string, 0, len(seen))
	for v := range seen {
		values = append(values, v)
	}
	sort.Strings(values)
	return secret.TextForms(values)
}

// driftSecretForms returns the values a report mustn't show, in the forms
// text quotes them in (see secret.TextForms): those state and plans mark
// sensitive, and the secrets redactDriftValue replaces in the others.
// Terraform's diagnostics, which can quote a value a provider rejects,
// have them replaced, as the changes' values do.
func driftSecretForms(state *tfjson.State, plans ...*tfjson.Plan) []string {
	seen := map[string]bool{}
	if state != nil && state.Values != nil {
		modules := []*tfjson.StateModule{state.Values.RootModule}
		for len(modules) > 0 {
			m := modules[0]
			modules = modules[1:]
			if m == nil {
				continue
			}
			modules = append(modules, m.ChildModules...)
			for _, r := range m.Resources {
				var mask any
				_ = json.Unmarshal(r.SensitiveValues, &mask)
				values := r.AttributeValues
				secret.CollectSensitive(values, mask, seen)
				collectDriftSecrets(values, "", true, seen)
			}
		}
	}
	forEachChange(plans, func(c *tfjson.Change) {
		secret.CollectSensitive(c.Before, c.BeforeSensitive, seen)
		secret.CollectSensitive(c.After, c.AfterSensitive, seen)
		collectDriftSecrets(c.Before, "", true, seen)
		collectDriftSecrets(c.After, "", true, seen)
	})
	return secret.TextForms(slices.Sorted(maps.Keys(seen)))
}

// forEachChange calls visit with the change of each resource the plans
// show, those of their drift and of their changes, in order; a plan that
// is nil has none, nor has a resource change without a change.
func forEachChange(plans []*tfjson.Plan, visit func(*tfjson.Change)) {
	for _, p := range plans {
		if p == nil {
			continue
		}
		for _, rcs := range [][]*tfjson.ResourceChange{p.ResourceDrift, p.ResourceChanges} {
			for _, rc := range rcs {
				if rc.Change != nil {
					visit(rc.Change)
				}
			}
		}
	}
}

// collectDriftSecrets adds to out the strings of v, which goes by key,
// that redactNamed replaces as secrets: a value driftSecret judges one,
// in a JSON document too, and a map key in a credential format.
func collectDriftSecrets(v any, key string, named bool, out map[string]bool) {
	switch v := v.(type) {
	case string:
		if !named {
			key = ""
		}
		if driftSecret(key, v) {
			out[v] = true
		} else if doc, ok := decodeJSONDocument(v); ok {
			collectDriftSecrets(doc, key, named, out)
		}
	case map[string]any:
		for k, sub := range v {
			if secret.CredentialFormat(k) != "" {
				out[k] = true
			}
			collectDriftSecrets(sub, entryKey(v, k), named && k != "Condition", out)
		}
	case []any:
		for _, sub := range v {
			collectDriftSecrets(sub, key, named, out)
		}
	}
}

// redactDriftValue returns v, which goes by key (see entryKey), as a
// report may show it: "(sensitive value)" if sensitive, else with every
// string that is a secret (see driftSecret) replaced, its own key or that
// of the map or JSON document it is in deciding, map keys in a credential
// format too, and every sensitive value of forms in the others.
func redactDriftValue(v any, key string, sensitive bool, forms []string) any {
	if sensitive {
		return secret.Redacted
	}
	return redactNamed(v, key, true, forms)
}

// redactNamed is redactDriftValue for a value that isn't sensitive. named
// says keys say what a value is: not below an IAM policy's Condition,
// whose keys are AWS's condition keys, as secretsInJSONString has it.
func redactNamed(v any, key string, named bool, forms []string) any {
	switch v := v.(type) {
	case string:
		if !named {
			key = ""
		}
		if driftSecret(key, v) {
			return secret.Redacted
		}
		// A JSON document, such as a container definition, is looked
		// into as import looks into one, and written again only if a
		// secret in it was replaced.
		if doc, ok := decodeJSONDocument(v); ok {
			if out := redactNamed(doc, key, named, forms); !reflect.DeepEqual(out, doc) {
				if b, err := json.Marshal(out); err == nil {
					return secret.RedactText(string(b), forms)
				}
				return secret.Redacted
			}
		}
		return secret.RedactText(v, forms)
	case map[string]any:
		out := make(map[string]any, len(v))
		// A key in a credential format, such as a map keyed by tokens,
		// is a secret too: each becomes "(sensitive value) #n", n
		// counting them in the order of the keys, so that they stay
		// apart and a report is the same each time.
		hidden := 0
		for _, k := range slices.Sorted(maps.Keys(v)) {
			shown := secret.RedactText(k, forms)
			if secret.CredentialFormat(k) != "" {
				hidden++
				shown = fmt.Sprintf("%s #%d", secret.Redacted, hidden)
			}
			out[shown] = redactNamed(v[k], entryKey(v, k), named && k != "Condition", forms)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, sub := range v {
			out[i] = redactNamed(sub, key, named, forms)
		}
		return out
	}
	return v
}

// driftSecret reports whether value, which goes by key, is a secret a
// report mustn't show: one in a credential format, or one whose key names
// a secret and that holds one, as import decides for a map key (see
// secret.Reason). Looking random alone, which import takes for a secret only
// in a map, doesn't make it one here: a report can't tell a map key from a
// provider's argument, whose IDs and digests look random.
func driftSecret(key, value string) bool {
	return secret.Reason(key, value, secret.IsName(key)) != ""
}

// entryKey returns the name the entry k of m goes by, as import has it
// (see the secret package's literal check): k, but for the value of a name/value pair, such as
// a container's environment variable, the pair's name, and "" if the
// pair's type says the value names a secret kept elsewhere.
func entryKey(m map[string]any, k string) string {
	if k != "value" && k != "Value" {
		return k
	}
	name := firstString(m, "name", "Name", "key", "Key")
	switch {
	case name == "":
		return k
	case secret.IsReferenceType(firstString(m, "type", "Type")):
		return ""
	}
	return name
}
