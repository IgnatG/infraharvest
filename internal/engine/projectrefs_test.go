// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"
)

// A module call names the project the root manages by a reference, alone
// or in a list; another project, and another argument with the project's
// ID as its value, such as a bucket's name, stay as they are.
func TestReferToProjects(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "google_project" "main" {
  name       = "Acme"
  project_id = "acme-prod"
}

module "network" {
  source       = "terraform-google-modules/network/google"
  version      = "12.0.0"
  network_name = "acme-prod"
  project_id   = "acme-prod"
}

module "project_iam" {
  source   = "terraform-google-modules/iam/google//modules/projects_iam"
  version  = "8.1.0"
  projects = ["acme-prod", "acme-shared"]
}

module "logs" {
  source     = "terraform-google-modules/cloud-storage/google//modules/simple_bucket"
  version    = "9.0.0"
  name       = "acme-prod"
  project_id = "acme-shared"
}
`)

	changed, err := referModulesToResources(dir)
	if err != nil || !changed {
		t.Fatalf("want the calls changed, got %v, %v", changed, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		`network_name = "acme-prod" project_id = google_project.main.project_id`,
		`projects = [google_project.main.project_id, "acme-shared"]`,
		`name = "acme-prod" project_id = "acme-shared"`,
		`project_id = "acme-prod" }`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
}

// Without a project the root manages, nothing changes.
func TestReferToProjectsWithoutAProject(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := `module "network" {
  source       = "terraform-google-modules/network/google"
  version      = "12.0.0"
  network_name = "vpc"
  project_id   = "acme-prod"
}
`
	writeConfig(t, dir, GeneratedFileName, content)
	changed, err := referModulesToResources(dir)
	if err != nil || changed {
		t.Fatalf("want nothing changed, got %v, %v", changed, err)
	}
	if got := readFile(t, dir, GeneratedFileName); got != content {
		t.Errorf("generated.tf changed:\n%s", got)
	}
}
