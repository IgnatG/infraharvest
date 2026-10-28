// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// updateGolden rewrites the golden files of the merged roots from what
// the code writes now:
//
//	go test ./internal/engine -run Golden -update
//
// or, without a local toolchain, the remote workflow's golden task with
// packages=./internal/engine.
var updateGolden = flag.Bool("update", false, "rewrite the golden files in testdata")

// rootSchemas are envSchemas with the types the merged roots' tests use
// besides.
var rootSchemas = func() *tfjson.ProviderSchemas {
	aws := envSchemas.Schemas["registry.terraform.io/hashicorp/aws"]
	resources := maps.Clone(aws.ResourceSchemas)
	resources["aws_cloudwatch_log_group"] = &tfjson.Schema{Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
		"name":              {AttributeType: cty.String},
		"retention_in_days": {AttributeType: cty.Number},
	}}}
	resources["aws_sns_topic"] = &tfjson.Schema{Block: &tfjson.SchemaBlock{Attributes: stringAttributes("name", "display_name", "kms_master_key_id", "tracing_config")}}
	return &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
		"registry.terraform.io/hashicorp/aws": {ResourceSchemas: resources},
	}}
}()

// awsEnvRoot returns environment env's AWS root of generated, in account,
// with its files by address, the provider's default tags and its backend
// if bucket is set.
func awsEnvRoot(t *testing.T, env string, tokens []string, account, generated string, ids, files, tags map[string]string, bucket string) envRoot {
	t.Helper()
	root := testEnvRoot(t, env, tokens, account, generated, ids)
	root.region = "eu-west-2"
	root.provider = rootfile.Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.14", Config: map[string]interface{}{
		"region":              "eu-west-2",
		"allowed_account_ids": []string{account},
		"assume_role":         map[string]interface{}{"role_arn": "arn:aws:iam::" + account + ":role/deploy"},
	}}
	root.requiredVersion = ">= 1.5, < 2.0"
	root.files = files
	root.defaultTags = tags
	if bucket != "" {
		root.backend = []byte(fmt.Sprintf(`terraform {
  backend "s3" {
    bucket       = %q
    key          = "envs/%s/aws/%s/eu-west-2/terraform.tfstate"
    region       = "eu-west-2"
    encrypt      = true
    use_lockfile = true
  }
}
`, bucket, env, account))
	}
	return root
}

// devProdRoots are a dev and a prod root of one stack: a queue whose
// timeout differs, a bucket destroyed with its objects in dev only, a role
// whose values hold the account and region, a database whose password is
// a secret of each environment, a table with a second attribute in prod,
// a function traced in prod, and a VPN prod only has, in a VPC it reads.
func devProdRoots(t *testing.T) []envRoot {
	t.Helper()
	generated := func(env, account, timeout, forceDestroy, class string) string {
		return `resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  name                       = "shop-` + env + `-orders"
  visibility_timeout_seconds = ` + timeout + `
}

resource "aws_s3_bucket" "logs" {
  bucket        = "shop-` + env + `-logs"
  force_destroy = ` + forceDestroy + `
}

resource "aws_iam_role" "ci" {
  description          = "reads arn:aws:sqs:eu-west-2:` + account + `:shop-` + env + `-orders"
  name                 = "shop-` + env + `-ci"
  permissions_boundary = "arn:aws:iam::` + account + `:policy/boundary"
}

resource "aws_db_instance" "main" {
  allocated_storage = 20
  engine            = "postgres"
  identifier        = "shop-` + env + `-db"
  instance_class    = "` + class + `"
  password          = var.shop_` + env + `_password
}
`
	}
	dev := generated("dev", "111111111111", "30", "true", "db.t3.micro") + `
resource "aws_dynamodb_table" "orders" {
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"
  name         = "shop-dev-orders"

  attribute {
    name = "id"
    type = "S"
  }
}

resource "aws_lambda_function" "api" {
  function_name = "shop-dev-api"
  handler       = "index.handler"
  runtime       = "nodejs22.x"
}
`
	prod := generated("prd", "222222222222", "60", "false", "db.r6g.large") + `
resource "aws_dynamodb_table" "orders" {
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"
  name         = "shop-prd-orders"

  attribute {
    name = "id"
    type = "S"
  }

  attribute {
    name = "sk"
    type = "S"
  }
}

resource "aws_lambda_function" "api" {
  function_name = "shop-prd-api"
  handler       = "index.handler"
  runtime       = "nodejs22.x"

  tracing_config {
    mode = "Active"
  }
}

resource "aws_vpn_gateway" "vpn" {
  amazon_side_asn = 64512
  vpc_id          = data.aws_vpc.vpn.id
}

resource "aws_customer_gateway" "vpn_office" {
  bgp_asn    = 65000
  ip_address = "203.0.113.10"
  type       = "ipsec.1"
}

resource "aws_vpn_connection" "office" {
  customer_gateway_id = aws_customer_gateway.vpn_office.id
  type                = "ipsec.1"
  vpn_gateway_id      = aws_vpn_gateway.vpn.id
}
`
	files := map[string]string{
		"aws_sqs_queue.orders":            "sqs.tf",
		"aws_s3_bucket.logs":              "s3.tf",
		"aws_iam_role.ci":                 "iam.tf",
		"aws_db_instance.main":            "rds.tf",
		"aws_dynamodb_table.orders":       "dynamodb.tf",
		"aws_lambda_function.api":         "lambda.tf",
		"aws_vpn_gateway.vpn":             "vpn.tf",
		"aws_customer_gateway.vpn_office": "vpn.tf",
		"aws_vpn_connection.office":       "vpn.tf",
	}
	ids := func(env, account string) map[string]string {
		return map[string]string{
			"aws_sqs_queue.orders":      "https://sqs.eu-west-2.amazonaws.com/" + account + "/shop-" + env + "-orders",
			"aws_s3_bucket.logs":        "shop-" + env + "-logs",
			"aws_iam_role.ci":           "shop-" + env + "-ci",
			"aws_db_instance.main":      "shop-" + env + "-db",
			"aws_dynamodb_table.orders": "shop-" + env + "-orders",
			"aws_lambda_function.api":   "shop-" + env + "-api",
		}
	}
	prodIDs := ids("prd", "222222222222")
	prodIDs["aws_vpn_gateway.vpn"] = "vgw-0abc1234"
	prodIDs["aws_customer_gateway.vpn_office"] = "cgw-0abc1234"
	prodIDs["aws_vpn_connection.office"] = "vpn-0abc1234"
	prodRoot := awsEnvRoot(t, "prod", []string{"prd"}, "222222222222", prod, prodIDs, files, map[string]string{"Environment": "prd", "Owner": "platform"}, "shop-tfstate-222222222222")
	data, err := hclfile.Parse(rootfile.DataFileName, []byte("data \"aws_vpc\" \"vpn\" {\n  id = \"vpc-0abc1234\"\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	prodRoot.data = data
	return []envRoot{
		awsEnvRoot(t, "dev", nil, "111111111111", dev, ids("dev", "111111111111"), files, map[string]string{"Environment": "dev", "Owner": "platform"}, "shop-tfstate-111111111111"),
		prodRoot,
	}
}

// threeEnvRoots are a dev, a stage and a prod root of one stack, with
// local state: a log group kept longer in prod, a queue prod has a second
// of, a topic too different to merge, a database stage and prod have,
// whose passwords are secrets, and a parameter prod alone has, whose value
// a variable of its root sets.
func threeEnvRoots(t *testing.T) []envRoot {
	t.Helper()
	generated := func(env, retention, display, key, tracing string) string {
		return `resource "aws_cloudwatch_log_group" "app" {
  name              = "/shop/` + env + `/app"
  retention_in_days = ` + retention + `
}

resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  name                       = "shop-` + env + `-orders"
  visibility_timeout_seconds = 30
}

resource "aws_sns_topic" "alerts" {
  display_name      = "` + display + `"
  kms_master_key_id = "` + key + `"
  name              = "shop-` + env + `-alerts"
  tracing_config    = "` + tracing + `"
}
`
	}
	database := func(env string) string {
		return `
resource "aws_db_instance" "main" {
  allocated_storage = 20
  engine            = "postgres"
  identifier        = "shop-` + env + `-db"
  instance_class    = "db.t3.micro"
  password          = var.shop_` + env + `_password
}
`
	}
	dev := generated("dev", "7", "Shop alerts", "alias/one", "PassThrough")
	stage := generated("stg", "7", "Shop staging alerts", "alias/two", "Active") + database("stg")
	prod := generated("prd", "90", "Shop production alerts", "alias/three", "PassThrough") + database("prd") + `
resource "aws_sqs_queue" "refunds" {
  delay_seconds              = 0
  name                       = "shop-prd-refunds"
  visibility_timeout_seconds = 30
}

resource "aws_ssm_parameter" "api_key" {
  name  = "/shop/prd/api-key"
  type  = "SecureString"
  value = var.shop_prd_api_key
}
`
	files := map[string]string{
		"aws_cloudwatch_log_group.app": "logs.tf",
		"aws_sqs_queue.orders":         "sqs.tf",
		"aws_sqs_queue.refunds":        "sqs.tf",
		"aws_sns_topic.alerts":         "sns.tf",
		"aws_db_instance.main":         "rds.tf",
		"aws_ssm_parameter.api_key":    "ssm.tf",
	}
	ids := func(env, account string, extra ...string) map[string]string {
		m := map[string]string{
			"aws_cloudwatch_log_group.app": "/shop/" + env + "/app",
			"aws_sqs_queue.orders":         "https://sqs.eu-west-2.amazonaws.com/" + account + "/shop-" + env + "-orders",
			"aws_sns_topic.alerts":         "arn:aws:sns:eu-west-2:" + account + ":shop-" + env + "-alerts",
		}
		for _, address := range extra {
			switch address {
			case "aws_db_instance.main":
				m[address] = "shop-" + env + "-db"
			case "aws_sqs_queue.refunds":
				m[address] = "https://sqs.eu-west-2.amazonaws.com/" + account + "/shop-" + env + "-refunds"
			case "aws_ssm_parameter.api_key":
				m[address] = "/shop/" + env + "/api-key"
			}
		}
		return m
	}
	prodRoot := awsEnvRoot(t, "prod", []string{"prd"}, "222222222222", prod, ids("prd", "222222222222", "aws_db_instance.main", "aws_sqs_queue.refunds", "aws_ssm_parameter.api_key"), files, nil, "")
	variables, err := hclfile.Parse(hclfile.VariablesFileName, []byte(`variable "shop_prd_api_key" {
  description = "The value of aws_ssm_parameter.api_key."
  type        = string
  sensitive   = true
}
`))
	if err != nil {
		t.Fatal(err)
	}
	prodRoot.variables = variables
	return []envRoot{
		awsEnvRoot(t, "dev", nil, "111111111111", dev, ids("dev", "111111111111"), files, nil, ""),
		awsEnvRoot(t, "stage", []string{"stg"}, "333333333333", stage, ids("stg", "333333333333", "aws_db_instance.main"), files, nil, ""),
		prodRoot,
	}
}

// azureRoots are a dev and a prod root of a stack on Azure: a storage
// account in each environment's resource group, in subscriptions the
// provider names.
func azureRoots(t *testing.T) []envRoot {
	t.Helper()
	const id = "/subscriptions/00000000-0000-0000-0000-00000000000%d/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s"
	root := func(env string, tokens []string, n int, group, name string) envRoot {
		subscription := fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", n)
		r := testEnvRoot(t, env, tokens, subscription, `resource "azurerm_storage_account" "logs" {
  account_replication_type = "LRS"
  account_tier             = "Standard"
  location                 = "uksouth"
  name                     = "`+name+`"
  resource_group_name      = "`+group+`"
}
`, map[string]string{"azurerm_storage_account.logs": fmt.Sprintf(id, n, group, name)})
		r.group = group
		r.provider = rootfile.Provider{Name: "azurerm", Source: "hashicorp/azurerm", Version: "~> 4.40", Config: map[string]interface{}{
			"features":        map[string]interface{}{},
			"subscription_id": subscription,
		}}
		r.requiredVersion = ">= 1.5, < 2.0"
		r.files = map[string]string{"azurerm_storage_account.logs": "storage.tf"}
		r.backend = []byte(`terraform {
  backend "azurerm" {
    resource_group_name  = "rg-tfstate"
    storage_account_name = "shoptfstate"
    container_name       = "tfstate"
    key                  = "envs/` + env + `/azurerm/terraform.tfstate"
  }
}
`)
		return r
	}
	return []envRoot{
		root("dev", nil, 1, "rg-blue", "shopdevlogs"),
		root("prod", []string{"prd"}, 2, "rg-green", "shopprdlogs"),
	}
}

// TestGoldenMergedRoots writes the merged roots of stacks of two and
// three AWS environments and of two Azure ones, and compares them with
// testdata/envroot/<name>.golden.txt. Each file parses and is formatted
// as terraform fmt leaves it, and the gate's checks of the files pass on
// them with the fake Terraform.
func TestGoldenMergedRoots(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		roots    func(*testing.T) []envRoot
	}{
		{"aws-two-environments", "aws", devProdRoots},
		{"aws-three-environments", "aws", threeEnvRoots},
		{"azurerm-two-environments", "azurerm", azureRoots},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := mergeStack(tc.provider, mergeProfiles[tc.provider], tc.roots(t), rootSchemas)
			files, err := mergedRootFiles(m)
			if err != nil {
				t.Fatal(err)
			}
			for name, content := range files {
				if strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
					continue
				}
				if _, diags := hclsyntax.ParseConfig(content, name, hcl.InitialPos); diags.HasErrors() {
					t.Errorf("%s doesn't parse: %v\n%s", name, diags, content)
				}
				if formatted := hclwrite.Format(content); string(formatted) != string(content) {
					t.Errorf("%s isn't formatted:\n%s", name, content)
				}
			}
			dir := t.TempDir()
			// As MergeRoots writes them.
			g := &mergeGate{dir: dir}
			if err := g.write(files); err != nil {
				t.Fatal(err)
			}
			if want := slices.Sorted(maps.Keys(files)); !slices.Equal(g.written, want) {
				t.Errorf("wrote %v, want %v", g.written, want)
			}
			policy := fullGate(Options{})
			policy.Plan = func() (gate.Check, []string, error) { return gate.Check{Name: gate.CheckPlan, Passed: true}, nil, nil }
			checks, _, err := policy.Run(context.Background(), &fakeTerraform{dir: dir}, dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range checks {
				if !c.Passed {
					t.Errorf("%s failed: %s", c.Name, strings.Join(c.Details, "; "))
				}
			}
			goldenRoot(t, tc.name, files)
		})
	}
}

// goldenRoot compares files, a merged root's, with
// testdata/envroot/<name>.golden.txt (see goldenFiles).
func goldenRoot(t *testing.T, name string, files map[string][]byte) {
	t.Helper()
	goldenFiles(t, filepath.Join("testdata", "envroot", name+".golden.txt"), files)
}

// goldenFiles compares files with the golden file at path, each file
// after a -- path -- line, or rewrites it under -update.
func goldenFiles(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(&b, "-- %s --\n%s", name, files[name])
		if !strings.HasSuffix(string(files[name]), "\n") {
			b.WriteString("\n")
		}
	}
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v: record it with go test ./internal/engine -run Golden -update (or the remote workflow's golden task)", err)
	}
	want := strings.ReplaceAll(string(content), "\r\n", "\n")
	if got := b.String(); got != want {
		wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
		for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
			w, g := "<end of file>", "<end of file>"
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if w != g {
				t.Errorf("%s doesn't match what the code writes now, first at line %d:\n  golden: %s\n  now:    %s\nif the change is intended, rewrite it with -update and review the diff", path, i+1, w, g)
				return
			}
		}
	}
}

// The merged root's files hold what the model gives: var.env conditions,
// counts, for_each, dynamic blocks, defaults by majority with three
// environments, and secrets in no var file.
func TestMergedRootFiles(t *testing.T) {
	t.Run("two environments", func(t *testing.T) {
		files, err := mergedRootFiles(mergeStack("aws", mergeProfiles["aws"], devProdRoots(t), rootSchemas))
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct{ file, want string }{
			{rootfile.ProvidersFileName, `backend "s3" {}`},
			{rootfile.ProvidersFileName, "region              = var.aws_region"},
			{rootfile.ProvidersFileName, "allowed_account_ids = var.allowed_account_ids"},
			{rootfile.ProvidersFileName, "Environment = var.env"},
			{rootfile.ProvidersFileName, `Owner       = "platform"`},
			{rootfile.ProvidersFileName, `required_version = ">= 1.7, < 2.0"`},
			{"s3.tf", `force_destroy = var.env == "dev"`},
			{"vpn.tf", "count = var.deploy_vpn ? 1 : 0"},
			{"vpn.tf", "vpn_gateway_id      = aws_vpn_gateway.vpn[0].id"},
			{"vpn.tf", "vpc_id = data.aws_vpc.vpn[0].id"},
			{rootfile.DataFileName, "data \"aws_vpc\" \"vpn\" {\n  count = var.deploy_vpn ? 1 : 0"},
			{"dynamodb.tf", `dynamic "attribute" {`},
			{"lambda.tf", "for_each = var.api_tracing_config == null ? [] : [var.api_tracing_config]"},
			{rootfile.ImportsFileName, `id = var.import_ids["aws_sqs_queue.orders"]`},
			{rootfile.ImportsFileName, "for_each = [for k in [0] : k if var.deploy_vpn]"},
			{hclfile.VariablesFileName, `condition     = contains(["dev", "prd"], var.env)`},
			{"vars/prod/terraform.tfvars", `env = "prd"`},
			{"vars/prod/terraform.tfvars", "deploy_vpn = true"},
			{"vars/prod/backend.hcl", `bucket       = "shop-tfstate-222222222222"`},
			{"vars/dev/imports.tfvars", `"aws_s3_bucket.logs"        = "shop-dev-logs"`},
		} {
			if !strings.Contains(flatText(string(files[tc.file])), flatText(tc.want)) {
				t.Errorf("%s doesn't hold %s:\n%s", tc.file, tc.want, files[tc.file])
			}
		}
		if strings.Contains(string(files[rootfile.ProvidersFileName]), "assume_role") {
			t.Error("the provider assumes a role")
		}
		for name, content := range files {
			if strings.HasPrefix(name, hclfile.VarsDir+"/") && strings.Contains(string(content), "password") {
				t.Errorf("%s sets the password:\n%s", name, content)
			}
		}
	})

	t.Run("three environments", func(t *testing.T) {
		files, err := mergedRootFiles(mergeStack("aws", mergeProfiles["aws"], threeEnvRoots(t), rootSchemas))
		if err != nil {
			t.Fatal(err)
		}
		variables := string(files[hclfile.VariablesFileName])
		for _, want := range []string{
			// The value two of three have is the default.
			"variable \"app_retention_in_days\" {\n  description = \"The retention_in_days of aws_cloudwatch_log_group.app.\"\n  type        = number\n  default     = 7\n}",
			// Dev doesn't use the password: null there.
			"variable \"db_instance_password\" {\n  description = \"The password of aws_db_instance.main.\"\n  type        = string\n  default     = null\n  sensitive   = true\n}",
			// Prod's root declared the variable its parameter reads.
			`variable "shop_prd_api_key" {`,
		} {
			if !strings.Contains(flatText(variables), flatText(want)) {
				t.Errorf("variables.tf doesn't hold %s:\n%s", want, variables)
			}
		}
		if got := string(files["vars/dev/terraform.tfvars"]); strings.Contains(got, "app_retention_in_days") {
			t.Errorf("dev sets the default:\n%s", got)
		}
		if got := string(files["vars/prod/terraform.tfvars"]); !strings.Contains(flatText(got), "app_retention_in_days = 90") {
			t.Errorf("prod doesn't set its retention:\n%s", got)
		}
		for _, tc := range []struct{ file, want string }{
			{"sqs.tf", "for_each = var.sqs_queues"},
			{rootfile.ImportsFileName, `id       = var.import_ids[format("aws_sqs_queue.this[%q]", each.key)]`},
			{"sns.tf", `resource "aws_sns_topic" "alerts_stage" {`},
			{"sns.tf", "count = var.deploy_alerts_stage ? 1 : 0"},
			{"vars/stage/terraform.tfvars", "deploy_alerts_stage = true"},
		} {
			if !strings.Contains(flatText(string(files[tc.file])), flatText(tc.want)) {
				t.Errorf("%s doesn't hold %s:\n%s", tc.file, tc.want, files[tc.file])
			}
		}
		if _, ok := files[hclfile.BackendConfigPath("dev")]; ok {
			t.Error("a backend.hcl for roots without a backend")
		}
		if strings.Contains(string(files[rootfile.ProvidersFileName]), "backend") {
			t.Error("a backend for roots without one")
		}
	})

	t.Run("a root without the others' backend", func(t *testing.T) {
		// Its backend.hcl has the settings the others' backend sets, to
		// fill in.
		roots := devProdRoots(t)
		roots[0].backend = nil
		files, err := mergedRootFiles(mergeStack("aws", mergeProfiles["aws"], roots, rootSchemas))
		if err != nil {
			t.Fatal(err)
		}
		want := "# The dev root had no s3 backend: set these.\n# bucket = \"\"\n# key = \"\"\n# region = \"\"\n# encrypt = \"\"\n# use_lockfile = \"\"\n"
		if got := string(files[hclfile.BackendConfigPath("dev")]); !strings.HasSuffix(got, want) {
			t.Errorf("dev's backend.hcl:\n%s\nwant it to end in\n%s", got, want)
		}
	})

	t.Run("different provider versions", func(t *testing.T) {
		// The newest release line any root pins.
		roots := devProdRoots(t)
		roots[1].provider.Version = "~> 6.20"
		files, err := mergedRootFiles(mergeStack("aws", mergeProfiles["aws"], roots, rootSchemas))
		if err != nil {
			t.Fatal(err)
		}
		if got := string(files[rootfile.ProvidersFileName]); !strings.Contains(got, `version = "~> 6.20"`) {
			t.Errorf("want the newest release line pinned:\n%s", got)
		}
		roots[0].provider.Version = "~> 7.0"
		if _, err := mergedRootFiles(mergeStack("aws", mergeProfiles["aws"], roots, rootSchemas)); err == nil || !strings.Contains(err.Error(), "different major releases") {
			t.Errorf("want an error naming the major releases, got %v", err)
		}
		roots[0].provider.Source = "example.com/acme/aws"
		if _, err := mergedRootFiles(mergeStack("aws", mergeProfiles["aws"], roots, rootSchemas)); err == nil || !strings.Contains(err.Error(), "different providers") {
			t.Errorf("want an error naming the providers, got %v", err)
		}
	})

	t.Run("handed over", func(t *testing.T) {
		// What another root hands prod's root over is named at its address
		// in the merged root, with the root it comes from.
		roots := devProdRoots(t)
		roots[1].arrivals = []rootfile.Arrival{{Type: "aws_sqs_queue", ID: "https://sqs.eu-west-2.amazonaws.com/222222222222/shop-prd-orders", Root: "aws/222222222222/eu-west-2", From: "aws_sqs_queue.shop_prd_orders", To: "aws_sqs_queue.orders_prd"}}
		files, err := mergedRootFiles(mergeStack("aws", mergeProfiles["aws"], roots, rootSchemas))
		if err != nil {
			t.Fatal(err)
		}
		readme := string(files[rootfile.ReadmeFileName])
		if want := "| prod | `aws_sqs_queue.orders` | `aws/222222222222/eu-west-2` | `aws_sqs_queue.shop_prd_orders` |"; !strings.Contains(readme, "## Moves") || !strings.Contains(readme, want) {
			t.Errorf("the README doesn't name %s:\n%s", want, readme)
		}
		files, err = mergedRootFiles(mergeStack("aws", mergeProfiles["aws"], devProdRoots(t), rootSchemas))
		if err != nil {
			t.Fatal(err)
		}
		if readme := string(files[rootfile.ReadmeFileName]); strings.Contains(readme, "## Moves") {
			t.Errorf("a README of moves without any:\n%s", readme)
		}
	})

	t.Run("a local of the roots", func(t *testing.T) {
		const generated = `resource "aws_sqs_queue" "orders" {
  name = "shop-%s-orders"
  tags = local.tags
}
`
		roots := []envRoot{
			awsEnvRoot(t, "dev", nil, "111111111111", fmt.Sprintf(generated, "dev"), nil, nil, nil, ""),
			awsEnvRoot(t, "prod", []string{"prd"}, "222222222222", fmt.Sprintf(generated, "prd"), nil, nil, nil, ""),
		}
		if _, err := mergedRootFiles(mergeStack("aws", mergeProfiles["aws"], roots, rootSchemas)); err == nil || !strings.Contains(err.Error(), "local.tags") {
			t.Errorf("want an error naming local.tags, got %v", err)
		}
	})
}

// flatText is s with each run of spaces, tabs and newlines one space.
func flatText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Resources of one shape every environment has become one resource with
// for_each, minRepeated of them or more: over a local map where every
// environment's keys are the same, once normalised, else over a
// variable; references to them are to their keys.
func TestMergedRepeatedShapes(t *testing.T) {
	queues := func(env string, names []string, timeout string) string {
		var b strings.Builder
		for _, name := range names {
			fmt.Fprintf(&b, "resource \"aws_sqs_queue\" %q {\n  name                       = \"shop-%s-%s\"\n  visibility_timeout_seconds = %s\n  delay_seconds              = 0\n  max_message_size           = 262144\n}\n\n", name, env, name, timeout)
		}
		b.WriteString("resource \"aws_sqs_queue_policy\" \"orders\" {\n  queue_url = aws_sqs_queue.orders.url\n  policy    = \"{}\"\n}\n")
		return b.String()
	}
	ids := func(env, account string, names []string) map[string]string {
		m := map[string]string{"aws_sqs_queue_policy.orders": "https://sqs.eu-west-2.amazonaws.com/" + account + "/shop-" + env + "-orders"}
		for _, name := range names {
			m["aws_sqs_queue."+name] = "https://sqs.eu-west-2.amazonaws.com/" + account + "/shop-" + env + "-" + name
		}
		return m
	}
	roots := func(names []string, prodTimeout string) []envRoot {
		return []envRoot{
			awsEnvRoot(t, "dev", nil, "111111111111", queues("dev", names, "30"), ids("dev", "111111111111", names), nil, nil, ""),
			awsEnvRoot(t, "prod", []string{"prd"}, "222222222222", queues("prd", names, prodTimeout), ids("prd", "222222222222", names), nil, nil, ""),
		}
	}
	three := []string{"audit", "events", "orders"}

	t.Run("the same in every environment", func(t *testing.T) {
		files, err := mergedRootFiles(mergeStack("aws", mergeProfiles["aws"], roots(three, "30"), rootSchemas))
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, name := range slices.Sorted(maps.Keys(files)) {
			fmt.Fprintf(&b, "-- %s --\n%s\n", name, files[name])
		}
		all := flatText(b.String())
		for _, want := range []string{
			`resource "aws_sqs_queue" "this" { for_each = local.sqs_queues`,
			"name = each.value.name",
			`"orders" = { name = "${local.naming_prefix}-orders" }`,
			`queue_url = aws_sqs_queue.this["orders"].url`,
			"import { for_each = local.sqs_queues to = aws_sqs_queue.this[each.key]",
		} {
			if !strings.Contains(all, flatText(want)) {
				t.Errorf("the merged root misses %s:\n%s", want, all)
			}
		}
		if strings.Contains(all, "variable \"sqs_queues\"") {
			t.Errorf("a variable of the queues:\n%s", all)
		}
		if got := string(files["vars/dev/imports.tfvars"]); !strings.Contains(got, `"aws_sqs_queue.this[\"audit\"]"`) {
			t.Errorf("dev's imports.tfvars doesn't import the audit queue at its key:\n%s", got)
		}
	})

	t.Run("different in an environment", func(t *testing.T) {
		files, err := mergedRootFiles(mergeStack("aws", mergeProfiles["aws"], roots(three, "60"), rootSchemas))
		if err != nil {
			t.Fatal(err)
		}
		if got := string(files["sqs.tf"]) + string(files[mainFileName]); !strings.Contains(got, "for_each = var.sqs_queues") {
			t.Errorf("the queues aren't over a variable:\n%s", got)
		}
		if got := string(files["vars/prod/terraform.tfvars"]); !strings.Contains(got, "sqs_queues") {
			t.Errorf("prod's terraform.tfvars doesn't set its queues:\n%s", got)
		}
	})

	t.Run("too few", func(t *testing.T) {
		m := mergeStack("aws", mergeProfiles["aws"], roots(three[:2], "30"), rootSchemas)
		if len(m.forEach) != 0 {
			t.Errorf("two queues are one resource: %s", m.forEach[0].address())
		}
	})
}
