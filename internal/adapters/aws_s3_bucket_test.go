// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// cluster parses resources; the first is the anchor.
func cluster(t *testing.T, src string) Cluster {
	t.Helper()
	f, diags := hclwrite.ParseConfig([]byte(src), "generated.tf", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	var c Cluster
	for i, b := range f.Body().Blocks() {
		r := Resource{Type: b.Labels()[0], Name: b.Labels()[1], Body: b.Body()}
		if i == 0 {
			c.Anchor = r
		} else {
			c.Members = append(c.Members, r)
		}
	}
	return c
}

// render writes a call's arguments as a module block's body, squashed.
func render(t *testing.T, a Adapter, call *Call) string {
	t.Helper()
	f := hclwrite.NewEmptyFile()
	body := f.Body().AppendNewBlock("module", []string{"m"}).Body()
	for _, arg := range call.Arguments {
		if !slices.Contains(a.Inputs, arg.Name) {
			t.Errorf("argument %q isn't in the adapter's Inputs", arg.Name)
		}
		body.SetAttributeRaw(arg.Name, arg.Value)
	}
	for _, outputs := range call.Outputs {
		for _, output := range outputs {
			if root := strings.FieldsFunc(output, func(r rune) bool { return r == '[' || r == '.' })[0]; !slices.Contains(a.Outputs, root) {
				t.Errorf("output %q isn't in the adapter's Outputs", output)
			}
		}
	}
	return squash(string(hclwrite.Format(f.Bytes())))
}

// called writes a call's arguments as the engine writes the call: without
// those at the module's defaults (see Adapter.IsDefault).
func called(t *testing.T, a Adapter, call *Call) string {
	t.Helper()
	without := *call
	without.Arguments = nil
	for _, arg := range call.Arguments {
		if !a.IsDefault(arg.Name, arg.Value) {
			without.Arguments = append(without.Arguments, arg)
		}
	}
	return render(t, a, &without)
}

func TestS3BucketMapsEveryMember(t *testing.T) {
	t.Parallel()
	c := cluster(t, `resource "aws_s3_bucket" "artifacts" {
  bucket              = "artifacts"
  bucket_prefix       = null
  force_destroy       = false
  object_lock_enabled = false
  tags                = local.tags
}
resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  bucket                = aws_s3_bucket.artifacts.id
  expected_bucket_owner = null
  rule {
    id     = "expire-old-builds"
    status = "Enabled"
    prefix = null
    expiration {
      days = 90
    }
    filter {
      prefix = "builds/"
    }
  }
  rule {
    id     = "tagged"
    status = "Disabled"
    filter {
      and {
        prefix = "logs/"
        tags   = { Env = "dev" }
      }
    }
    transition {
      days          = 30
      storage_class = "GLACIER"
    }
  }
}
resource "aws_s3_bucket_ownership_controls" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}
resource "aws_s3_bucket_policy" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  policy = jsonencode({ Resource = "${aws_s3_bucket.artifacts.arn}/*" })
}
resource "aws_s3_bucket_public_access_block" "artifacts" {
  bucket                  = aws_s3_bucket.artifacts.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.bucket
  rule {
    bucket_key_enabled = false
    apply_server_side_encryption_by_default {
      sse_algorithm     = "AES256"
      kms_master_key_id = null
    }
  }
}
resource "aws_s3_bucket_versioning" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  versioning_configuration {
    status     = "Enabled"
    mfa_delete = "Disabled"
  }
}
`)
	call, err := S3Bucket.Map(c)
	if err != nil {
		t.Fatal(err)
	}
	got := called(t, S3Bucket, call)
	for _, want := range []string{
		`bucket              = "artifacts"`,
		"tags                = local.tags",
		`id     = "expire-old-builds"`,
		"expiration = {",
		`prefix = "builds/"`,
		"tags   = { Env = \"dev\" }",
		`storage_class = "GLACIER"`,
		"control_object_ownership = true",
		"attach_policy = true",
		`policy        = jsonencode({ Resource = "${aws_s3_bucket.artifacts.arn}/*" })`,
		"skip_destroy_public_access_block = null",
		`sse_algorithm = "AES256"`,
		"bucket_key_enabled = false",
		`status     = "Enabled"`,
	} {
		if !strings.Contains(got, squash(want)) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "attach_public_policy") || strings.Contains(got, "kms_master_key_id") {
		t.Errorf("unexpected argument:\n%s", got)
	}
	// At the module's defaults.
	for _, unwanted := range []string{"force_destroy", "object_lock_enabled", "  object_ownership", "block_public_acls", "block_public_policy", "ignore_public_acls", "restrict_public_buckets", "mfa_delete"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("call sets %s at the module's default:\n%s", unwanted, got)
		}
	}
	if call.Addresses["aws_s3_bucket_versioning.artifacts"] != "aws_s3_bucket_versioning.this[0]" || call.Addresses["aws_s3_bucket.artifacts"] != "aws_s3_bucket.this[0]" {
		t.Errorf("addresses: %v", call.Addresses)
	}
	if call.Outputs["aws_s3_bucket.artifacts"]["arn"] != "s3_bucket_arn" {
		t.Errorf("outputs: %v", call.Outputs)
	}
}

// Settings other than the module's defaults are passed: public access
// allowed, another object ownership, MFA delete on.
func TestS3BucketPassesOtherThanModuleDefaults(t *testing.T) {
	t.Parallel()
	call, err := S3Bucket.Map(cluster(t, `resource "aws_s3_bucket" "site" {
  bucket              = "site"
  force_destroy       = true
  object_lock_enabled = true
}
resource "aws_s3_bucket_ownership_controls" "site" {
  bucket = aws_s3_bucket.site.id
  rule {
    object_ownership = "ObjectWriter"
  }
}
resource "aws_s3_bucket_public_access_block" "site" {
  bucket              = aws_s3_bucket.site.id
  block_public_acls   = true
  block_public_policy = false
}
resource "aws_s3_bucket_versioning" "site" {
  bucket = aws_s3_bucket.site.id
  versioning_configuration {
    mfa_delete = "Enabled"
    status     = "Enabled"
  }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := called(t, S3Bucket, call)
	for _, want := range []string{"force_destroy = true", "object_lock_enabled = true", `object_ownership = "ObjectWriter"`, "block_public_policy = false", "ignore_public_acls = false", "restrict_public_buckets = false", `mfa_delete = "Enabled"`} {
		if !strings.Contains(squash(got), want) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "block_public_acls") {
		t.Errorf("call sets block_public_acls at the module's default:\n%s", got)
	}
}

// Without a public access block, the module must not create one.
func TestS3BucketTurnsOffThePublicAccessBlock(t *testing.T) {
	t.Parallel()
	call, err := S3Bucket.Map(cluster(t, `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := called(t, S3Bucket, call); !strings.Contains(got, "attach_public_policy = false") {
		t.Errorf("public access block not turned off:\n%s", got)
	}
}

func TestS3BucketDeclines(t *testing.T) {
	t.Parallel()
	for name, src := range map[string]string{
		"unmapped bucket argument": `resource "aws_s3_bucket" "b" {
  bucket       = "b"
  something_new = true
}
`,
		"no bucket name": `resource "aws_s3_bucket" "b" {
  bucket_prefix = "b-"
}
`,
		"unmapped nested block": `resource "aws_s3_bucket" "b" {
  bucket = "b"
}
resource "aws_s3_bucket_versioning" "b" {
  bucket = aws_s3_bucket.b.id
  versioning_configuration {
    status = "Enabled"
    future = "x"
  }
}
`,
		"expected owner": `resource "aws_s3_bucket" "b" {
  bucket = "b"
}
resource "aws_s3_bucket_policy" "b" {
  bucket                = aws_s3_bucket.b.id
  expected_bucket_owner = "123456789012"
  policy                = "{}"
}
`,
		"literal bucket": `resource "aws_s3_bucket" "b" {
  bucket = "b"
}
resource "aws_s3_bucket_policy" "b" {
  bucket = "b"
  policy = "{}"
}
`,
		"two encryption rules": `resource "aws_s3_bucket" "b" {
  bucket = "b"
}
resource "aws_s3_bucket_server_side_encryption_configuration" "b" {
  bucket = aws_s3_bucket.b.id
  rule {
    bucket_key_enabled = true
  }
  rule {
    bucket_key_enabled = false
  }
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := S3Bucket.Map(cluster(t, src))
			if !IsDecline(err) {
				t.Errorf("want a decline, got %v", err)
			}
		})
	}
}

// squash collapses whitespace, so that tests don't depend on alignment.
func squash(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// A lifecycle filter's tag key is written as a person writes one.
func TestObjectKey(t *testing.T) {
	t.Parallel()
	for src, want := range map[string]string{
		`"Env"`:         "Env",
		`"cost-center"`: `"cost-center"`,
		`"a b"`:         `"a b"`,
	} {
		f, diags := hclwrite.ParseConfig([]byte("x = "+src+"\n"), "", hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatal(diags)
		}
		if got := strings.TrimSpace(string(objectKey(f.Body().GetAttribute("x").Expr().BuildTokens(nil)).Bytes())); got != want {
			t.Errorf("objectKey(%s): got %s, want %s", src, got, want)
		}
	}
}
