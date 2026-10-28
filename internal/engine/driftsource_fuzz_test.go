// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"encoding/json"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// Accepting a change to a credential, whatever key it goes by and whether
// the code has the key yet, never writes it: the change is a secret, and
// the file is as it was.
func FuzzDriftNeverWritesSecrets(f *testing.F) {
	f.Add("API_TOKEN", "token")
	f.Add("LOG_LEVEL", "level")
	f.Add("kubernetes.io/role", "dotted")
	f.Add("ingress[0]", "brackets")
	f.Add(`a"b\c${d}%{e}`, "escapes")
	f.Add("ключ", "unicode")
	f.Fuzz(func(t *testing.T, key, seed string) {
		quoted, ok := hclQuoted(key)
		if key == "" || !ok {
			t.Skip()
		}
		token := fuzzToken(seed)
		for name, variables := range map[string]string{
			"a key the code has":     `"` + quoted + `" = "old"`,
			"a key the code doesn't": `LOG = "debug"`,
		} {
			src := `resource "aws_lambda_function" "fuzz" {
  description = "old"

  environment {
    variables = {
      ` + variables + `
    }
  }
}
`
			if _, err := hclfile.Parse("main.tf", []byte(src)); err != nil {
				t.Skipf("%q doesn't make valid HCL: %v", key, err)
			}
			for _, c := range []*DriftChange{
				driftAt("aws_lambda_function.fuzz", "environment[0].variables."+key, token),
				driftAt("aws_lambda_function.fuzz", "description", "rotated "+token),
			} {
				e := testEditor(map[string]map[string]any{"aws_lambda_function.fuzz": {
					"description": "rotated " + token,
					"environment": []any{map[string]any{"variables": map[string]any{key: token}}},
				}})
				contents := map[string][]byte{"main.tf": []byte(src)}
				_, _, failed, err := e.apply(contents, []*DriftChange{c})
				if err != nil {
					t.Fatal(err)
				}
				if s, ok := failed[c]; !ok || !s.Secret {
					t.Errorf("%s, %s: the credential isn't taken for a secret: %+v", name, c.Attribute, s)
				}
				if strings.Contains(string(contents["main.tf"]), token) || string(contents["main.tf"]) != src {
					t.Errorf("%s, %s: the file changed:\n%s", name, c.Attribute, contents["main.tf"])
				}
			}
		}
	})
}

// A value import takes for a secret by its format, or by a key that names
// one, never shows in a drift report: not at the leaf, nor in a map, a
// name/value pair or a JSON document the cloud changed whole, nor as a
// map's key.
func FuzzDriftReportsNoSecrets(f *testing.F) {
	f.Add("DB_PASSWORD", "Summer2026!")
	f.Add("api_key", "hunter22")
	f.Add("LOG_LEVEL", "debug")
	f.Add("SECRET_ARN", "arn:aws:secretsmanager:eu-west-1:123456789012:secret:db")
	f.Add("kubernetes.io/"+"token", "RED"+"ACTED") // joined so a secret scanner sees no key/value literal
	f.Add("ключ", "token")
	f.Add("0000\xa1000token", "00\x9e000") // JSON shows both invalid bytes as U+FFFD, so the key holds the value
	f.Fuzz(func(t *testing.T, key, value string) {
		quotedKey, err := json.Marshal(key)
		if err != nil {
			t.Skip()
		}
		for _, v := range []string{value, fuzzToken(value)} {
			if credentialFormat(v) == "" && (!secretKeyName(key) || secretReason(key, v, true) == "") {
				continue
			}
			quoted, err := json.Marshal(v)
			if err != nil {
				continue
			}
			escaped := string(quoted[1 : len(quoted)-1])
			// What a report shows besides the value mustn't hold it, as it
			// is or as JSON has it.
			if shown := key + string(quotedKey) + redacted + ` #1 keyed {"name":"value":"pairs":[]}`; strings.Contains(shown, v) || strings.Contains(shown, escaped) {
				continue
			}
			pairs := []any{map[string]any{"name": key, "value": v}}
			doc, err := json.Marshal(map[string]any{key: v, "pairs": pairs})
			if err != nil {
				continue
			}
			refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{{
				Address: "aws_lambda_function.fuzz",
				Mode:    tfjson.ManagedResourceMode,
				Type:    "aws_lambda_function",
				Change: &tfjson.Change{
					Actions: tfjson.Actions{tfjson.ActionUpdate},
					Before: map[string]any{
						"environment": []any{map[string]any{"variables": map[string]any{}}},
						"env":         []any{},
						"settings":    "none",
						"definition":  "{}",
					},
					After: map[string]any{
						"environment": []any{map[string]any{"variables": map[string]any{key: v}}},
						"env":         pairs,
						"settings":    map[string]any{key: v, "keyed": map[string]any{fuzzToken(value): "x"}},
						"definition":  string(doc),
					},
				},
			}}}
			for _, c := range classifyDrift(refresh, nil, nil, false) {
				for _, s := range reportStrings(c.After) {
					if strings.Contains(s, v) || strings.Contains(s, escaped) || strings.Contains(s, fuzzToken(value)) {
						t.Errorf("%s shows the secret: %q", c.Attribute, s)
					}
				}
			}
		}
	})
}

// reportStrings returns the strings of a value a report shows, its map
// keys too.
func reportStrings(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case map[string]any:
		var out []string
		for k, sub := range v {
			out = append(out, k)
			out = append(out, reportStrings(sub)...)
		}
		return out
	case []any:
		var out []string
		for _, sub := range v {
			out = append(out, reportStrings(sub)...)
		}
		return out
	}
	return nil
}
