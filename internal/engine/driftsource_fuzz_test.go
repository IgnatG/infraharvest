// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"
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
			if _, err := parseHCL("main.tf", []byte(src)); err != nil {
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
