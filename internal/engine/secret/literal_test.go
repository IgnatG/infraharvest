// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// fakeSecret makes a 64-character hex secret from seed, like the Lambda
// environment secrets of the sandbox run. Made at run time, so that secret
// scanners don't take the source for a leak.
func fakeSecret(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// writeGenerated writes content into the generated configuration of dir
// and loads it.
func writeGenerated(t *testing.T, dir, content string) *hclfile.File {
	t.Helper()
	path := filepath.Join(dir, generatedFile)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := hclfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// readGenerated returns the generated configuration of dir.
func readGenerated(t *testing.T, dir string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, generatedFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// generatedFile is the file Terraform generates configuration into, which
// the engine moves secrets out of.
const generatedFile = "generated.tf"

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

func TestLiteralSecretsInContainerDefinitions(t *testing.T) {
	t.Parallel()
	password := fakeSecret("db")[:24]
	token := fakeSecret("token")
	dir := t.TempDir()
	writeGenerated(t, dir, `resource "aws_ecs_task_definition" "app" {
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

	secrets, _, err := LiteralsToVariables(filepath.Join(dir, generatedFile), nil)
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
	generated := readGenerated(t, dir)
	for _, want := range []string{
		`value = var.aws_ecs_task_definition_app_db_password`,
		`value = "8080"`,
		// A JSON string keeps its text, with the value read in place,
		// JSON-encoded.
		`\"value\":${jsonencode(var.aws_ecs_task_definition_legacy_api_token)}`,
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
	t.Parallel()
	key := "github_pat_" + fakeSecret("pat")[:40]
	dir := t.TempDir()
	writeGenerated(t, dir, `resource "aws_iam_role" "ci" {
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

	secrets, _, err := LiteralsToVariables(filepath.Join(dir, generatedFile), nil)
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
	generated := readGenerated(t, dir)
	for _, want := range []string{`= "sts.amazonaws.com"`, `= "repo:octo/app:environment:prod"`, `repo:octo/app:ref:refs/heads/main`} {
		if !strings.Contains(generated, want) {
			t.Errorf("missing %q in:\n%s", want, generated)
		}
	}
	assertNotWritten(t, dir, key)
}

func TestLiteralSecretsInAppSettingsAndEnv(t *testing.T) {
	t.Parallel()
	apiKey := fakeSecret("api")
	password := fakeSecret("sql")[:20]
	token := fakeSecret("run")
	dir := t.TempDir()
	writeGenerated(t, dir, `resource "azurerm_linux_web_app" "web" {
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

	secrets, _, err := LiteralsToVariables(filepath.Join(dir, generatedFile), map[string]bool{"var.azurerm_linux_web_app_web_api_key": true})
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
	generated := readGenerated(t, dir)
	for _, want := range []string{`"@Microsoft.KeyVault(`, `WEBSITE_RUN_FROM_PACKAGE = "1"`, `value = "production"`, `value = var.google_cloud_run_v2_service_api_upstream_token`} {
		if !strings.Contains(generated, want) {
			t.Errorf("missing %q in:\n%s", want, generated)
		}
	}
	assertNotWritten(t, dir, apiKey, password, token)
}

func TestSecretReason(t *testing.T) {
	t.Parallel()
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
		{"Sid", "AWSLogDeliveryWrite3fG9kQ2xR7mT", true, false}, // a policy statement's ID
		{"SESSION_SID", "AWSLogDeliveryWrite3fG9kQ2xR7mT", true, true},
		{"description", "keep the API secret safe", true, false},
	} {
		if got := Reason(tc.key, tc.value, tc.freeForm) != ""; got != tc.secret {
			t.Errorf("Reason(%q, %q, %v): got secret %v, want %v", tc.key, tc.value, tc.freeForm, got, tc.secret)
		}
	}
}

func TestKeyWords(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	dir := t.TempDir()
	writeGenerated(t, dir, `resource "aws_iam_user" "ci" {
  name = "ci"
  tags = {
    `+testKeyID+` = "github-actions"
  }
}
`)

	secrets, warnings, err := LiteralsToVariables(filepath.Join(dir, generatedFile), nil)
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

// The secrets a run killed before it could remove them go once they are a
// day old; those of a run still going, and other directories, stay.
func TestRemoveStaleSecretValues(t *testing.T) {
	t.Parallel()
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

// Scripts hide secrets the other checks can't see: in base64, as a launch
// template keeps its user data, or on a line of their own.
func TestLiteralSecretsInScripts(t *testing.T) {
	t.Parallel()
	password := fakeSecret("script-db")[:20]
	token := fakeSecret("script-token")[:32]
	userData := base64.StdEncoding.EncodeToString([]byte("#!/bin/bash\nyum install -y mysql\nexport DB_PASSWORD='" + password + "'\n"))
	clean := base64.StdEncoding.EncodeToString([]byte("#!/bin/bash\nmkdir -p /opt/app\nexport DB_PASSWORD=$(aws secretsmanager get-secret-value --secret-id db --query SecretString --output text)\n"))
	dir := t.TempDir()
	writeGenerated(t, dir, `resource "aws_launch_template" "app" {
  name      = "app"
  user_data = "`+userData+`"
}

resource "aws_launch_template" "clean" {
  name      = "clean"
  user_data = "`+clean+`"
}

resource "google_compute_instance" "web" {
  name = "web"
  metadata = {
    enable-oslogin = "TRUE"
    startup-script = "#!/bin/bash\ncd /opt/app\nAPI_TOKEN=`+token+` ./run.sh --port 8080\n"
  }
}
`)

	secrets, _, err := LiteralsToVariables(filepath.Join(dir, generatedFile), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range secrets {
		got = append(got, s.Address+" "+s.Attribute)
	}
	want := []string{"aws_launch_template.app user_data", "google_compute_instance.web metadata.startup-script"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	generated := readGenerated(t, dir)
	for _, want := range []string{clean, `enable-oslogin = "TRUE"`} {
		if !strings.Contains(generated, want) {
			t.Errorf("missing %q in:\n%s", want, generated)
		}
	}
	assertNotWritten(t, dir, userData, token)
}

func TestScriptSecret(t *testing.T) {
	t.Parallel()
	secret := fakeSecret("script")[:20]
	for script, want := range map[string]bool{
		"export DB_PASSWORD=" + secret:                                         true,
		"#!/bin/sh\nmysql -u root -p'" + secret + "' app < x.sql":              true,
		"run --api-token " + secret:                                            true,
		"#cloud-config\nchpasswd:\n  list: x\npassword: " + secret:             true,
		"$env:CLIENT_SECRET = '" + secret + "'":                                true,
		base64.StdEncoding.EncodeToString([]byte("API_KEY=" + secret)):         true,
		"export DB_PASSWORD=$DB_PASSWORD_FROM_SSM":                             false,
		"export DB_PASSWORD_FILE=/run/secrets/db":                              false,
		"mkdir -p /opt/app && echo ECS_CLUSTER=prod >> /etc/ecs/ecs.config":    false,
		"/etc/eks/bootstrap.sh prod --b64-cluster-ca " + secret:                false,
		base64.StdEncoding.EncodeToString([]byte("#!/bin/bash\necho hello\n")): false,
	} {
		if got := scriptSecret(script) != ""; got != want {
			t.Errorf("scriptSecret(%q): got secret %v, want %v", script, got, want)
		}
	}
}

// A value a JSON string repeats becomes one variable, read wherever the
// value was.
func TestLiteralSecretsRepeatedInAJSONString(t *testing.T) {
	t.Parallel()
	token := fakeSecret("token")
	dir := t.TempDir()
	writeGenerated(t, dir, `resource "aws_ecs_task_definition" "legacy" {
  family                = "legacy"
  container_definitions = "[{\"environment\":[{\"name\":\"API_TOKEN\",\"value\":\"`+token+`\"},{\"name\":\"OLD_API_TOKEN\",\"value\":\"`+token+`\"}],\"name\":\"web\"}]"
}
`)

	secrets, _, err := LiteralsToVariables(filepath.Join(dir, generatedFile), nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(secrets) != 1 || secrets[0].Variable != "aws_ecs_task_definition_legacy_api_token" {
		t.Errorf("want one variable, got %+v", secrets)
	}
	if n := strings.Count(readGenerated(t, dir), "${jsonencode(var.aws_ecs_task_definition_legacy_api_token)}"); n != 2 {
		t.Errorf("the variable is read %d times, want 2", n)
	}
	assertNotWritten(t, dir, token)
}

// A value jsonencode would write otherwise than the string has it can't
// be read in place without changing the plan: the whole string moves.
func TestLiteralSecretsJSONEncodedOtherwise(t *testing.T) {
	t.Parallel()
	token := fakeSecret("amp")[:32] + "&" + fakeSecret("amp")[32:]
	dir := t.TempDir()
	writeGenerated(t, dir, `resource "aws_ecs_task_definition" "legacy" {
  family                = "legacy"
  container_definitions = "[{\"environment\":[{\"name\":\"API_TOKEN\",\"value\":\"`+token+`\"}],\"name\":\"web\"}]"
}
`)

	secrets, _, err := LiteralsToVariables(filepath.Join(dir, generatedFile), nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(secrets) != 1 {
		t.Fatalf("want one variable, got %+v", secrets)
	}
	if generated := readGenerated(t, dir); strings.Contains(generated, "jsonencode(") {
		t.Errorf("the value is read in place:\n%s", generated)
	}
	assertNotWritten(t, dir, token)
}
