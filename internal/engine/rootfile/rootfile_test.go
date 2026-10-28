// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package rootfile

import (
	"reflect"
	"strings"
	"testing"
)

func TestImportsFile(t *testing.T) {
	t.Parallel()
	got, err := ImportsFile([]Import{
		{Type: "aws_sqs_queue", Name: "r--orders", ID: "https://sqs.eu-west-1.amazonaws.com/123/orders"},
		{Type: "aws_s3_bucket", Name: "r--logs", ID: `odd"id`},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `import {
  to = aws_s3_bucket.r--logs
  id = "odd\"id"
}

import {
  to = aws_sqs_queue.r--orders
  id = "https://sqs.eu-west-1.amazonaws.com/123/orders"
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestImportsFileRejectsInvalidAddress(t *testing.T) {
	t.Parallel()
	if _, err := ImportsFile([]Import{{Type: "aws_sqs_queue", Name: "has space", ID: "x"}}); err == nil {
		t.Error("want an error for an invalid resource name")
	}
}

// A resource imported with a provider other than the one its type implies
// says so, or Terraform would use the implied one.
func TestImportsFileNamesTheProvider(t *testing.T) {
	t.Parallel()
	got, err := ImportsFile([]Import{{Type: "google_compute_network", Name: "main", ID: "projects/p/global/networks/main", Provider: "google-beta"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `import {
  to       = google_compute_network.main
  id       = "projects/p/global/networks/main"
  provider = google-beta
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if _, err := ImportsFile([]Import{{Type: "google_compute_network", Name: "main", ID: "x", Provider: "google beta"}}); err == nil {
		t.Error("want an error for an invalid provider name")
	}
}

func TestProvidersFile(t *testing.T) {
	t.Parallel()
	got, err := ProvidersFile(Provider{
		Name:    "azurerm",
		Source:  "hashicorp/azurerm",
		Version: "~> 4.0",
		Config: map[string]interface{}{
			"subscription_id":            "sub",
			"features":                   map[string]interface{}{},
			"skip_provider_registration": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `provider "azurerm" {
  features {
  }
  skip_provider_registration = true
  subscription_id            = "sub"
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestVersionsFile(t *testing.T) {
	t.Parallel()
	got := VersionsFile(">= 1.16, < 2.0", Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.67"})

	want := `terraform {
  required_version = ">= 1.16, < 2.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.67"
    }
  }
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestProvidersFileLeavesOutCredentials(t *testing.T) {
	t.Parallel()
	got, err := ProvidersFile(Provider{
		Name:   "pagerduty",
		Source: "PagerDuty/pagerduty",
		Config: map[string]interface{}{
			"token":            "u+secret",
			"api_url_override": "https://api.eu.pagerduty.com",
			"service_region":   "eu",
			"nested":           map[string]interface{}{"client_secret": "s", "url": "https://x"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `provider "pagerduty" {
  api_url_override = "https://api.eu.pagerduty.com"
  nested {
    url = "https://x"
  }
  service_region = "eu"
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	_, dropped := WithoutCredentials(map[string]interface{}{
		"token": "t", "access_token": "a", "api_key": "k", "apikey": "k", "password": "p", "client_secret": "s",
		"secret_key": "s", "private_key": "pk", "credentials": "c", "nested": map[string]interface{}{"token": "t"},
		"region": "eu", "token_url": "u", "skip_credentials_validation": true, "api_url": "u",
	})
	wantDropped := []string{"access_token", "api_key", "apikey", "client_secret", "credentials", "nested.token", "password", "private_key", "secret_key", "token"}
	if !reflect.DeepEqual(dropped, wantDropped) {
		t.Errorf("dropped %v, want %v", dropped, wantDropped)
	}
}

func TestProvidersFileRejectsUnsupportedValue(t *testing.T) {
	t.Parallel()
	_, err := ProvidersFile(Provider{Name: "aws", Source: "hashicorp/aws", Config: map[string]interface{}{"region": struct{}{}}})
	if err == nil || !strings.Contains(err.Error(), "region") {
		t.Errorf("want an error naming the argument, got %v", err)
	}
}
