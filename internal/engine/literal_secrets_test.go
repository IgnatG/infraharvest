// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeSecret makes a 64-character hex secret from seed, like the Lambda
// environment secrets of the sandbox run. Made at run time, so that secret
// scanners don't take the source for a leak.
func fakeSecret(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// assertNotWritten fails if any file under dir holds one of values.
func assertNotWritten(t *testing.T, dir string, values ...string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, v := range values {
			if strings.Contains(string(content), v) {
				t.Errorf("%s holds the secret value %q:\n%s", path, v, content)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The sandbox run wrote two Lambda environment secrets verbatim: the
// provider doesn't mark map values sensitive, so Terraform generated them.
func TestGenerateMovesEnvironmentSecretsToVariables(t *testing.T) {
	dir := t.TempDir()
	secret := fakeSecret("webhook")
	tf := &fakeTerraform{dir: dir, generated: `resource "aws_lambda_function" "api" {
  function_name = "api"
  role          = "arn:aws:iam::123456789012:role/api"
  environment {
    variables = {
      LOG_LEVEL      = "debug"
      TABLE_ARN      = "arn:aws:dynamodb:eu-west-2:123456789012:table/orders"
      WEBHOOK_SECRET = "` + secret + `"
    }
  }
}
`}

	result, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_lambda_function", Name: "api", ID: "api"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := []Secret{{Variable: "aws_lambda_function_api_webhook_secret", Address: "aws_lambda_function.api", Attribute: "environment.variables.WEBHOOK_SECRET"}}
	var got []Secret
	for _, s := range result.Secrets {
		got = append(got, Secret{Variable: s.Variable, Address: s.Address, Attribute: s.Attribute})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("secrets: got %+v, want %+v", got, want)
	}
	generated := readFile(t, dir, GeneratedFileName)
	for _, line := range []string{"WEBHOOK_SECRET = var.aws_lambda_function_api_webhook_secret", `LOG_LEVEL      = "debug"`, `TABLE_ARN      = "arn:aws:dynamodb:`} {
		if !strings.Contains(generated, line) {
			t.Errorf("missing %q in:\n%s", line, generated)
		}
	}
	variables := readFile(t, dir, VariablesFileName)
	for _, want := range []string{`variable "aws_lambda_function_api_webhook_secret" {`, "type        = string", "sensitive   = true", "secret store", "TF_VAR_aws_lambda_function_api_webhook_secret"} {
		if !strings.Contains(variables, want) {
			t.Errorf("missing %q in variables.tf:\n%s", want, variables)
		}
	}
	if strings.Contains(variables, "default") {
		t.Errorf("a secret variable has a default:\n%s", variables)
	}
	if readme := readFile(t, dir, ReadmeFileName); !strings.Contains(readme, "`aws_lambda_function_api_webhook_secret`") {
		t.Errorf("README doesn't list the variable:\n%s", readme)
	}
	// Infraharvest's own plans had the value, from outside the output, so
	// the plan stays clean; the file is gone afterwards.
	if len(tf.varFiles) == 0 {
		t.Fatal("no plan was given the secret's value")
	}
	for path, content := range tf.varFiles {
		if !strings.Contains(content, secret) {
			t.Errorf("var file %s lacks the value: %s", path, content)
		}
		if strings.HasPrefix(path, dir) {
			t.Errorf("var file %s is in the output", path)
		}
		if _, err := os.Stat(path); err == nil {
			t.Errorf("var file %s left behind", path)
		}
	}
	if !result.Gate.check(CheckSecrets).Passed {
		t.Errorf("G6 failed: %+v", result.Gate.check(CheckSecrets))
	}
	assertNotWritten(t, dir, secret)
}

func TestLiteralSecretsInContainerDefinitions(t *testing.T) {
	password := fakeSecret("db")[:24]
	token := fakeSecret("token")
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_ecs_task_definition" "app" {
  family = "app"
  container_definitions = jsonencode([{
    environment = [{ name = "DB_PASSWORD", value = "`+password+`" }, { name = "PORT", value = "8080" }]
    image       = "public.ecr.aws/nginx/nginx:1.27"
    name        = "web"
    secrets     = [{ name = "API_KEY", valueFrom = "arn:aws:secretsmanager:eu-west-2:123456789012:secret:api" }]
  }])
}

resource "aws_ecs_task_definition" "legacy" {
  family                = "legacy"
  container_definitions = "[{\"environment\":[{\"name\":\"API_TOKEN\",\"value\":\"`+token+`\"}],\"name\":\"web\"}]"
}
`)

	secrets, _, err := literalSecretsToVariables(filepath.Join(dir, GeneratedFileName), nil)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, s := range secrets {
		got = append(got, s.Variable+" "+s.Attribute)
	}
	want := []string{
		"aws_ecs_task_definition_app_db_password container_definitions[0].environment[0].value",
		"aws_ecs_task_definition_legacy_api_token jsondecode(container_definitions)[0].environment[0].value",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	generated := readFile(t, dir, GeneratedFileName)
	for _, want := range []string{
		`value = var.aws_ecs_task_definition_app_db_password`,
		`value = "8080"`,
		// A JSON string keeps its text, with the value read in place.
		`\"value\":\"${var.aws_ecs_task_definition_legacy_api_token}\"`,
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("missing %q in:\n%s", want, generated)
		}
	}
	assertNotWritten(t, dir, password, token)
}

// The condition keys of an IAM policy are AWS's, such as GitHub's OIDC
// token.actions.githubusercontent.com:sub, so they don't make the values
// secrets; a credential under a condition still is one.
func TestLiteralSecretsLeaveOutPolicyConditions(t *testing.T) {
	key := "github_pat_" + fakeSecret("pat")[:40]
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_iam_role" "ci" {
  name = "ci"
  assume_role_policy = jsonencode({
    Statement = [{
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = { "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com" }
        StringLike   = { "token.actions.githubusercontent.com:sub" = "repo:octo/app:environment:prod" }
      }
      Effect    = "Allow"
      Principal = { Federated = "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com" }
    }]
    Version = "2012-10-17"
  })
}

resource "aws_iam_role" "legacy" {
  name               = "legacy"
  assume_role_policy = "{\"Statement\":[{\"Condition\":{\"StringEquals\":{\"token.actions.githubusercontent.com:sub\":\"repo:octo/app:ref:refs/heads/main\",\"aws:RequestTag/api\":\"`+key+`\"}}}]}"
}
`)

	secrets, _, err := literalSecretsToVariables(filepath.Join(dir, GeneratedFileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range secrets {
		got = append(got, s.Attribute)
	}
	if want := []string{`jsondecode(assume_role_policy).Statement[0].Condition.StringEquals["aws:RequestTag/api"]`}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	generated := readFile(t, dir, GeneratedFileName)
	for _, want := range []string{`= "sts.amazonaws.com"`, `= "repo:octo/app:environment:prod"`, `repo:octo/app:ref:refs/heads/main`} {
		if !strings.Contains(generated, want) {
			t.Errorf("missing %q in:\n%s", want, generated)
		}
	}
	assertNotWritten(t, dir, key)
}

func TestLiteralSecretsInAppSettingsAndEnv(t *testing.T) {
	apiKey := fakeSecret("api")
	password := fakeSecret("sql")[:20]
	token := fakeSecret("run")
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "azurerm_linux_web_app" "web" {
  name = "web"
  app_settings = {
    API_KEY                  = "`+apiKey+`"
    DB_CONNECTION            = "Server=tcp:sql.database.windows.net;Database=app;User ID=app;Password=`+password+`;"
    KEY_VAULT_REF            = "@Microsoft.KeyVault(SecretUri=https://kv.vault.azure.net/secrets/db/)"
    WEBSITE_RUN_FROM_PACKAGE = "1"
  }
  tags = { owner = "web" }
}

resource "google_cloud_run_v2_service" "api" {
  name = "api"
  template {
    containers {
      env {
        name  = "MODE"
        value = "production"
      }
      env {
        name  = "UPSTREAM_TOKEN"
        value = "`+token+`"
      }
    }
  }
}
`)

	secrets, _, err := literalSecretsToVariables(filepath.Join(dir, GeneratedFileName), Names{"var.azurerm_linux_web_app_web_api_key": true})
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, s := range secrets {
		got = append(got, s.Variable+" "+s.Attribute)
	}
	want := []string{
		// A name the root takes already gets a number.
		"azurerm_linux_web_app_web_api_key_2 app_settings.API_KEY",
		"azurerm_linux_web_app_web_db_connection app_settings.DB_CONNECTION",
		"google_cloud_run_v2_service_api_upstream_token template.containers.env[1].value",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	generated := readFile(t, dir, GeneratedFileName)
	for _, want := range []string{`"@Microsoft.KeyVault(`, `WEBSITE_RUN_FROM_PACKAGE = "1"`, `value = "production"`, `value = var.google_cloud_run_v2_service_api_upstream_token`} {
		if !strings.Contains(generated, want) {
			t.Errorf("missing %q in:\n%s", want, generated)
		}
	}
	assertNotWritten(t, dir, apiKey, password, token)
}

func TestSecretReason(t *testing.T) {
	secret := fakeSecret("x")
	for _, tc := range []struct {
		key, value string
		freeForm   bool
		secret     bool
	}{
		{"API_SECRET", secret, true, true},
		{"dbPassword", "hunter2x", true, true},
		{"DB_PASS", "correct-horse", true, true},
		{"clientSecret", "abc.DEF-123_xyz", true, true},
		{"AWS_SECRET_ACCESS_KEY", "Ab3+cD4/eF5gH6iJ7kL8" + "mN9oP0qR1sT2uV3wX4yZ", true, true}, // betterleaks:allow
		{"SIGNING", secret, true, true},                                                        // random enough to be a key
		{"X", "ghp_" + strings.Repeat("a1", 18), false, true},                                  // a token format, anywhere
		{"DATABASE_URL", "postgres://app:" + "pw123456@db:5432/app", true, true},
		// A key that names a secret: spaces and digits don't make the
		// value plain.
		{"DB_PASSWORD", "correct horse battery staple", true, true},
		{"DB_PASSWORD", "12345678", true, true},
		{"PIN_SECRET", "84629137", true, true},
		{"Authorization", "Bearer " + secret[:24], true, true},
		{"Cookie", "session=" + secret[:24], true, true},
		// Authorization headers and webhook URLs that carry a token, under
		// any key.
		{"headers", "Bearer " + secret[:24], false, true},
		{"X", "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pa55word")), true, true},
		{"WEBHOOK_URL", "https://discord.com/api/webhooks/123456789012345678/" + secret[:40], true, true},
		{"WEBHOOK_URL", "https://contoso.webhook.office.com/webhookb2/" + secret[:36] + "/IncomingWebhook/" + secret[:32], true, true},
		{"WEBHOOK_URL", "https://hooks.zapier.com/hooks/catch/123456/" + secret[:8], true, true},
		// Not secrets.
		{"DB_PASSWORD", "{{resolve:secretsmanager:prod/db:SecretString:password}}", true, false},
		{"TOKEN_TTL_SECONDS", "86400000", true, false},
		{"DB_PASSWORD", "disabled", true, false},
		{"API_TOKEN", "https://vault.example.com/v1/secret/api", true, false},
		{"description", "Basic authentication for the admin pages", true, false},
		{"X", "Bearer authentication", true, false},
		{"WEBHOOK_URL", "https://discord.com/channels/1234", true, false},
		{"API_SECRET_ARN", "arn:aws:secretsmanager:eu-west-2:123456789012:secret:api", true, false},
		{"TOKEN_URL", "https://login.example.com/oauth2/token", true, false},
		{"AWS_ACCESS_KEY_ID", "AKIA" + "EXAMPLE" + "EXAMPLE12", true, false},
		{"PASSWORD_MIN_LENGTH", "12", true, false},
		{"TOKEN_ENABLED", "true", true, false},
		{"SECRET", "abc", true, false},
		{"SOURCE_HASH", secret, true, false},
		{"IMAGE_TAG", secret[:40], true, false},
		{"TENANT", "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0", true, false},
		{"DB_PASSWORD", "/prod/db/password", true, false}, // a parameter's name
		{"HOME_PAGE", "https://example.com/docs", true, false},
		{"access_token", "minutes", false, false}, // a schema argument
		{"source_code_hash", secret, false, false},
		{"description", "keep the API secret safe", true, false},
	} {
		if got := secretReason(tc.key, tc.value, tc.freeForm) != ""; got != tc.secret {
			t.Errorf("secretReason(%q, %q, %v): got secret %v, want %v", tc.key, tc.value, tc.freeForm, got, tc.secret)
		}
	}
}

func TestKeyWords(t *testing.T) {
	for key, want := range map[string]string{
		"DB_PASSWORD":   "DB PASSWORD",
		"dbPassword":    "DB PASSWORD",
		"APIKey":        "API KEY",
		"client-secret": "CLIENT SECRET",
		"Oauth2Token":   "OAUTH2 TOKEN",
	} {
		if got := strings.Join(keyWords(key), " "); got != want {
			t.Errorf("keyWords(%q): got %q, want %q", key, got, want)
		}
	}
}

// An access key ID used as a tag key isn't a secret, but is worth a
// warning.
func TestAccessKeyTagWarning(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_iam_user" "ci" {
  name = "ci"
  tags = {
    `+testKeyID+` = "github-actions"
  }
}
`)

	secrets, warnings, err := literalSecretsToVariables(filepath.Join(dir, GeneratedFileName), nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(secrets) != 0 {
		t.Errorf("want no secrets, got %+v", secrets)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "aws_iam_user.ci") || strings.Contains(warnings[0], testKeyID) {
		t.Errorf("want one warning naming the user without the whole ID, got %q", warnings)
	}
}

// The gate fails a root with a secret value left in it, such as one an
// edit put back.
func TestScanSecretsFailsOnPlantedSecret(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_lambda_function" "api" {
  function_name = "api"
  environment {
    variables = {
      API_SECRET = "`+fakeSecret("planted")+`"
    }
  }
}
`)
	writeConfig(t, dir, ImportsFileName, `import {
  to = aws_lambda_function.api
  id = "api"
}
`)

	check, err := scanSecrets(dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	if check.Passed || len(check.Details) != 1 || !strings.Contains(check.Details[0], "generated.tf: environment.variables.API_SECRET of resource.aws_lambda_function.api holds a secret value") {
		t.Errorf("want G6 to fail on the secret, got %+v", check)
	}
}

// The secrets a run killed before it could remove them go once they are a
// day old; those of a run still going, and other directories, stay.
func TestRemoveStaleSecretValues(t *testing.T) {
	parent := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	mkdir := func(name string, modified time.Time) string {
		t.Helper()
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, secretValuesFile)
		if err := os.WriteFile(path, []byte(`{"db_password": "x"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{path, dir} {
			if err := os.Chtimes(p, modified, modified); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	stale := mkdir(secretValuesPattern+"1", old)
	current := mkdir(secretValuesPattern+"2", time.Now())
	other := mkdir("other-1", old)

	removeStaleSecretValues(parent, time.Now().Add(-staleSecretsAge))

	for dir, want := range map[string]bool{stale: false, current: true, other: true} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", filepath.Base(dir), err == nil, want)
		}
	}
}
