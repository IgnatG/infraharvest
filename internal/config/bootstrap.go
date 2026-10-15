// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// BootstrapDir is where infraharvest bootstrap writes the root that creates
// the state backend, in the output directory.
const BootstrapDir = "bootstrap"

// Bootstrap renders a root that creates the backend's storage, by file
// name: versions.tf, main.tf, variables.tf (if it needs inputs) and a
// README. People apply it once, with rights to create storage, before
// planning the generated roots. The storage is versioned, encrypted, not
// public, only reachable over TLS, and protected from terraform destroy.
func (b *Backend) Bootstrap() (map[string][]byte, error) {
	var files map[string]string
	switch {
	case b.S3 != nil:
		files = s3Bootstrap(b.S3)
	case b.AzureRM != nil:
		files = azureRMBootstrap(b.AzureRM)
	case b.GCS != nil:
		files = gcsBootstrap(b.GCS)
	default:
		return nil, errors.New("no backend to bootstrap")
	}
	out := make(map[string][]byte, len(files))
	for name, content := range files {
		if strings.HasSuffix(name, ".tf") {
			out[name] = hclwrite.Format([]byte(content))
		} else {
			out[name] = []byte(content)
		}
	}
	return out, nil
}

// q quotes s as an HCL string.
func q(s string) string {
	return string(hclwrite.TokensForValue(cty.StringVal(s)).Bytes())
}

func versionsTF(requiredVersion, provider, source, version string) string {
	return fmt.Sprintf(`terraform {
  required_version = %s
  required_providers {
    %s = {
      source  = %s
      version = %s
    }
  }
}
`, q(requiredVersion), provider, q(source), q(version))
}

func bootstrapReadme(what, backend string) string {
	return fmt.Sprintf(`# State backend

This root creates %s, which holds the state of the roots infraharvest generated (see their %s). Apply it once, with rights to create storage, before you plan the generated roots:

`+"```sh\nterraform init\nterraform apply\n```"+`

Its own state stays local: keep terraform.tfstate safe, or move it into the new storage afterwards with a backend block and terraform init -migrate-state. The storage is protected with prevent_destroy, as the state is the only record of what Terraform manages.
`, what, backend)
}

func s3Bootstrap(s *S3) map[string]string {
	encryption := `sse_algorithm = "AES256"`
	if s.KMSKeyID != "" {
		encryption = `sse_algorithm     = "aws:kms"
      kms_master_key_id = ` + q(s.KMSKeyID)
	}
	return map[string]string{
		// S3 native state locking (use_lockfile) needs Terraform 1.10.
		"versions.tf": versionsTF(">= 1.10, < 2.0", "aws", "hashicorp/aws", "~> 6.0"),
		"main.tf": fmt.Sprintf(`provider "aws" {
  region = %s
}

resource "aws_s3_bucket" "state" {
  bucket = %s

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default {
      %s
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_policy" "state" {
  bucket = aws_s3_bucket.state.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.state.arn, "${aws_s3_bucket.state.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })

  depends_on = [aws_s3_bucket_public_access_block.state]
}
`, q(s.Region), q(s.Bucket), encryption),
		"README.md": bootstrapReadme("the S3 bucket "+s.Bucket, "backend.tf, which locks with use_lockfile"),
	}
}

func azureRMBootstrap(a *AzureRM) map[string]string {
	return map[string]string{
		"versions.tf": versionsTF(">= 1.10, < 2.0", "azurerm", "hashicorp/azurerm", "~> 4.0"),
		"variables.tf": `variable "location" {
  description = "Azure region of the state storage, such as uksouth."
  type        = string
}
`,
		"main.tf": fmt.Sprintf(`provider "azurerm" {
  features {}
}

resource "azurerm_resource_group" "state" {
  name     = %s
  location = var.location

  lifecycle {
    prevent_destroy = true
  }
}

resource "azurerm_storage_account" "state" {
  name                            = %s
  resource_group_name             = azurerm_resource_group.state.name
  location                        = azurerm_resource_group.state.location
  account_tier                    = "Standard"
  account_replication_type        = "GRS"
  min_tls_version                 = "TLS1_2"
  https_traffic_only_enabled      = true
  allow_nested_items_to_be_public = false

  blob_properties {
    versioning_enabled = true
    delete_retention_policy {
      days = 30
    }
    container_delete_retention_policy {
      days = 30
    }
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "azurerm_storage_container" "state" {
  name                  = %s
  storage_account_id    = azurerm_storage_account.state.id
  container_access_type = "private"
}
`, q(a.ResourceGroupName), q(a.StorageAccountName), q(a.ContainerName)),
		"README.md": bootstrapReadme("the storage account "+a.StorageAccountName+" and its container "+a.ContainerName, "backend.tf"),
	}
}

func gcsBootstrap(g *GCS) map[string]string {
	return map[string]string{
		"versions.tf": versionsTF(">= 1.10, < 2.0", "google", "hashicorp/google", "~> 7.0"),
		"variables.tf": `variable "project" {
  description = "Google Cloud project of the state bucket."
  type        = string
}

variable "location" {
  description = "Location of the state bucket, such as EUROPE-WEST2 or EU."
  type        = string
}
`,
		"main.tf": fmt.Sprintf(`provider "google" {
  project = var.project
}

resource "google_storage_bucket" "state" {
  name                        = %s
  location                    = var.location
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  versioning {
    enabled = true
  }

  lifecycle {
    prevent_destroy = true
  }
}
`, q(g.Bucket)),
		"README.md": bootstrapReadme("the Cloud Storage bucket "+g.Bucket, "backend.tf"),
	}
}
