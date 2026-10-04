// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

func TestBootstrap(t *testing.T) {
	for name, tc := range map[string]struct {
		backend Backend
		want    map[string][]string
	}{
		"s3": {
			Backend{S3: &S3{Bucket: "acme-state", Region: "eu-west-2", KMSKeyID: "alias/state"}},
			map[string][]string{
				"versions.tf": {`required_version = ">= 1.10, < 2.0"`, `source  = "hashicorp/aws"`, `version = "~> 6.0"`},
				"main.tf": {
					`bucket = "acme-state"`, `region = "eu-west-2"`, "prevent_destroy = true", `status = "Enabled"`,
					`kms_master_key_id = "alias/state"`, "restrict_public_buckets = true", `"aws:SecureTransport" = "false"`,
				},
			},
		},
		"azurerm": {
			Backend{AzureRM: &AzureRM{ResourceGroupName: "state-rg", StorageAccountName: "acmestate", ContainerName: "tfstate"}},
			map[string][]string{
				"main.tf":      {`name     = "state-rg"`, `min_tls_version                 = "TLS1_2"`, "versioning_enabled = true", `container_access_type = "private"`},
				"variables.tf": {`variable "location"`},
			},
		},
		"gcs": {
			Backend{GCS: &GCS{Bucket: "acme-state"}},
			map[string][]string{
				"main.tf":      {`name                        = "acme-state"`, `public_access_prevention    = "enforced"`, "enabled = true"},
				"variables.tf": {`variable "project"`, `variable "location"`},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			files, err := tc.backend.Bootstrap()
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := files["README.md"]; !ok {
				t.Error("no README.md")
			}
			for file, content := range files {
				if !strings.HasSuffix(file, ".tf") {
					continue
				}
				if _, diags := hclsyntax.ParseConfig(content, file, hcl.InitialPos); diags.HasErrors() {
					t.Errorf("%s doesn't parse: %v\n%s", file, diags, content)
				}
				if !bytes.Equal(hclwrite.Format(content), content) {
					t.Errorf("%s isn't formatted:\n%s", file, content)
				}
			}
			for file, wants := range tc.want {
				for _, want := range wants {
					if !strings.Contains(squash(string(files[file])), squash(want)) {
						t.Errorf("%s misses %q:\n%s", file, want, files[file])
					}
				}
			}
		})
	}
	if _, err := (&Backend{}).Bootstrap(); err == nil {
		t.Error("want an error without a backend")
	}
}

// squash collapses whitespace, so that checks don't depend on alignment.
func squash(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
