// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// fuzzToken makes a credential in a format CredentialFormat knows, a
// GitHub fine-grained token, from seed. Made at run time, as fakeSecret's
// values are, so that secret scanners don't take the source for a leak.
func fuzzToken(seed string) string {
	return "github_pat_" + fakeSecret(seed)[:40]
}

// hasValue reports whether one of found is value.
func hasValue(found []foundSecret, value string) bool {
	for _, f := range found {
		if f.value == value {
			return true
		}
	}
	return false
}

// hclQuoted writes s as the content of an HCL quoted string, with the
// characters HCL reads specially escaped. It reports false for a string a
// quoted string can't hold as written: invalid UTF-8 or control
// characters.
func hclQuoted(s string) (string, bool) {
	if !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
		return "", false
	}
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "${", "$${", "%{", "%%{").Replace(s), true
}

// A credential is found inside the JSON document of a string whatever the
// name it goes by and whatever else the document holds, and looking into
// any string doesn't panic.
func FuzzSecretsInJSONString(f *testing.F) {
	f.Add(`[{"environment":[{"name":"PORT","value":"8080"}],"name":"web"}]`, "API_TOKEN", "8080")
	f.Add(`{"Statement":[{"Condition":{"StringEquals":{"token.actions.githubusercontent.com:sub":"repo:octo/app:ref:refs/heads/main"}}}]}`, "aws:RequestTag/api", "repo:octo/app")
	f.Add(`{"DB_PASSWORD":"`+fakeSecret("db")[:24]+`"}`, "Condition", "")
	f.Add(`[1, true, null, {"tags": {"k": "v"}}]`, "tags", fakeSecret("filler"))
	f.Add(" {", "", "{\"nested\":1}")
	f.Fuzz(func(t *testing.T, value, key, filler string) {
		_ = secretsInJSONString(nil, value, "container_definitions", key)
		_ = Reason(key, value, true)
		_ = Reason(key, value, false)

		token := fuzzToken(value + key)
		doc, err := json.Marshal([]any{
			map[string]any{"name": key, "value": token},
			map[string]any{key: filler},
			filler,
		})
		if err != nil {
			t.Fatal(err)
		}
		if found := secretsInJSONString(nil, string(doc), "container_definitions", "container_definitions"); !hasValue(found, token) {
			t.Errorf("the token isn't found in %s: %+v", doc, found)
		}
	})
}

// A credential written as a map value, or as the value of a name/value pair
// inside jsonencode, is found whatever the key it goes by.
func FuzzSecretsInHCL(f *testing.F) {
	f.Add("WEBHOOK_SECRET", "webhook")
	f.Add("token.actions.githubusercontent.com:sub", "oidc")
	f.Add("Condition", "condition")
	f.Add("tags", "tags")
	f.Add(`a"b\c${d}%{e}`, "escapes")
	f.Add("ключ", "unicode")
	f.Fuzz(func(t *testing.T, key, seed string) {
		// An empty key is no key to secretsInPolicy, as a computed one is.
		quoted, ok := hclQuoted(key)
		if key == "" || !ok {
			t.Skip()
		}
		token := fuzzToken(seed)
		src := `resource "aws_lambda_function" "fuzz" {
  environment {
    variables = {
      "` + quoted + `" = "` + token + `"
    }
  }
}

resource "aws_ecs_task_definition" "fuzz" {
  container_definitions = jsonencode([{ environment = [{ name = "` + quoted + `", value = "` + token + `" }] }])
}
`
		file, diags := hclsyntax.ParseConfig([]byte(src), "fuzz.tf", hcl.InitialPos)
		if diags.HasErrors() {
			t.Skipf("%q doesn't make valid HCL: %s", key, diags)
		}
		for _, b := range file.Body.(*hclsyntax.Body).Blocks {
			if found := secretsInBody(b.Body, ""); !hasValue(found, token) {
				t.Errorf("%s: the token isn't found under the key %q: %+v", strings.Join(b.Labels, "."), key, found)
			}
		}
	})
}

// Scanning any file for secrets, and moving them to variables, doesn't
// panic.
func FuzzLiteralSecrets(f *testing.F) {
	f.Add(`resource "aws_lambda_function" "api" {
  function_name = "api"
  environment {
    variables = {
      LOG_LEVEL      = "debug"
      WEBHOOK_SECRET = "` + fakeSecret("webhook") + `"
    }
  }
}
`)
	f.Add(`resource "aws_ecs_task_definition" "legacy" {
  family                = "legacy"
  container_definitions = "[{\"environment\":[{\"name\":\"API_TOKEN\",\"value\":\"` + fakeSecret("token") + `\"}],\"name\":\"web\"}]"
}
`)
	f.Add(`resource "google_cloud_run_v2_service" "api" {
  template {
    containers {
      env {
        name  = "UPSTREAM_TOKEN"
        value = <<EOT
` + fakeSecret("run") + `
EOT
      }
    }
  }
}
`)
	f.Add(`resource "aws_iam_user" "ci" {
  tags = { ` + testKeyID + ` = "github-actions" }
}
`)
	f.Add(`resource "x" "y" {
  a = { "${b}" = "c", (local.k) = "` + fuzzToken("computed") + `" }
  d = ("` + fuzzToken("parentheses") + `")
  e = [["` + fuzzToken("nested") + `"]]
}
`)
	f.Fuzz(func(t *testing.T, content string) {
		if parsed, err := hclfile.Parse("generated.tf", []byte(content)); err == nil {
			_ = Literals(parsed)
		}
		path := filepath.Join(t.TempDir(), generatedFile)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, _ = LiteralsToVariables(path, nil)
	})
}
