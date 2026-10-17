// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// bucketModule is an organisation's own bucket module: a bucket with its
// versioning, from variables, and a setting of its own.
const bucketModule = `variable "name" {
  type = string
}

variable "tags" {
  type    = map(string)
  default = {}
}

variable "versioning" {
  type    = string
  default = "Enabled"
}

resource "aws_s3_bucket" "this" {
  bucket        = var.name
  force_destroy = false
  tags          = var.tags
}

resource "aws_s3_bucket_versioning" "this" {
  bucket = aws_s3_bucket.this.id
  versioning_configuration {
    status = var.versioning
  }
}

output "arn" {
  value = aws_s3_bucket.this.arn
}

output "name" {
  value = aws_s3_bucket.this.id
}
`

const bucketCluster = `resource "aws_s3_bucket" "logs" {
  bucket        = "logs"
  force_destroy = false
}
resource "aws_s3_bucket_versioning" "logs" {
  bucket = aws_s3_bucket.logs.id
  versioning_configuration {
    status = "Suspended"
  }
}
`

func moduleDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestGenericMatchesAModulesSignature(t *testing.T) {
	t.Parallel()
	a, err := Generic("git::https://example.com/modules.git//bucket?ref=v1.0.0", "", moduleDir(t, map[string]string{"main.tf": bucketModule}))
	if err != nil {
		t.Fatal(err)
	}
	if a.Anchor != "aws_s3_bucket" || !reflect.DeepEqual(a.Members, []string{"aws_s3_bucket_versioning"}) {
		t.Fatalf("anchor %s, members %v", a.Anchor, a.Members)
	}
	call, err := a.Map(cluster(t, bucketCluster))
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, a, call)
	for _, want := range []string{`name = "logs"`, `versioning = "Suspended"`} {
		if !strings.Contains(got, want) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "tags") {
		t.Errorf("tags set though the bucket has none, and the module's default is empty:\n%s", got)
	}
	wantAddresses := map[string]string{"aws_s3_bucket.logs": "aws_s3_bucket.this", "aws_s3_bucket_versioning.logs": "aws_s3_bucket_versioning.this"}
	if !reflect.DeepEqual(call.Addresses, wantAddresses) {
		t.Errorf("addresses: %v", call.Addresses)
	}
	if !reflect.DeepEqual(call.Outputs["aws_s3_bucket.logs"], map[string]string{"arn": "arn", "id": "name"}) {
		t.Errorf("outputs: %v", call.Outputs)
	}
}

func TestGenericDeclines(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ module, cluster, reason string }{
		"a setting the module can't set": {
			bucketModule, strings.Replace(bucketCluster, `force_destroy = false`, "force_destroy = false\n  object_lock_enabled = true", 1),
			"the module can't set aws_s3_bucket.logs.object_lock_enabled",
		},
		"a resource the cluster doesn't have": {
			bucketModule, strings.Split(bucketCluster, `resource "aws_s3_bucket_versioning"`)[0],
			"has no aws_s3_bucket_versioning, and the module makes one",
		},
		"a variable nothing sets": {
			strings.Replace(bucketModule, "  tags          = var.tags\n", "  tags          = merge(var.tags, { owner = var.owner })\n", 1) + "\nvariable \"owner\" {\n  type = string\n}\n",
			bucketCluster,
			"the module's variable owner has no default",
		},
		"a reference elsewhere": {
			bucketModule, strings.Replace(bucketCluster, "bucket = aws_s3_bucket.logs.id", `bucket = "other"`, 1),
			"doesn't refer to aws_s3_bucket.logs",
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, err := Generic("example/bucket/aws", "1.0.0", moduleDir(t, map[string]string{"main.tf": tc.module}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.Map(cluster(t, tc.cluster))
			if !IsDecline(err) || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("got %v, want a decline with %q", err, tc.reason)
			}
		})
	}
}

func TestGenericFailsForModulesItCantMatch(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ module, reason string }{
		"count":           {strings.Replace(bucketModule, `  bucket        = var.name`, "  count         = 1\n  bucket        = var.name", 1), "aws_s3_bucket.this has count"},
		"two of a type":   {bucketModule + "\nresource \"aws_s3_bucket\" \"other\" {\n  bucket = \"other\"\n}\n", "it has two aws_s3_bucket resources"},
		"a nested module": {bucketModule + "\nmodule \"inner\" {\n  source = \"./inner\"\n}\n", "it calls module inner"},
		"dynamic blocks":  {strings.Replace(bucketModule, "  versioning_configuration {\n    status = var.versioning\n  }", "  dynamic \"versioning_configuration\" {\n    for_each = [1]\n    content {\n      status = var.versioning\n    }\n  }", 1), "dynamic versioning_configuration blocks"},
		"no anchor":       {"resource \"aws_s3_bucket\" \"a\" {\n  bucket = \"a\"\n}\nresource \"aws_sqs_queue\" \"q\" {\n  name = \"q\"\n}\n", "no resource of it is one all its others refer to"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Generic("example/bucket/aws", "1.0.0", moduleDir(t, map[string]string{"main.tf": tc.module}))
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("got %v, want %q", err, tc.reason)
			}
		})
	}
}
