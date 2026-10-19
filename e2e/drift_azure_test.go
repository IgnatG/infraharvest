// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || azure)

package e2e

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

// TestAzureDrift imports testdata/azure, applies the root into a local
// state, and changes two tags out of band: the network security group's
// env, which it gets from local.tags as the other resources do, and the
// identity's own role tag. Drift reports both as kept, exiting 4 with
// --detailed-exitcode, and leaves the code as it was; accepted, the group
// gets its own env over local.tags, which the others keep, and the
// identity its role, in its module call if the import made one. The code
// then plans no change, and, once the state is refreshed, drift finds
// nothing.
//
// The tags several resources share aren't changed all at once here, as
// the AWS test does: floci-az doesn't keep the resource group's tags, so
// the group can't change with the others. The test changes the estate
// the other Azure tests import, so CI runs it on a runner of its own.
func TestAzureDrift(t *testing.T) {
	e := seedAzure(t)
	out := importAzure(t, e, "", "--all")
	checkCoverage(t, "the import", coverageOf(t, out), azureEmulatorFailure)
	dirs := applyGenerated(e.ctx, t, out, e.execPath, e.plugins)
	drift := driftRun{ctx: e.ctx, engineName: e.engineName, execPath: e.execPath, out: out}

	changeOutOfBand(e.ctx, t, `
terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.0"
    }
  }
}

provider "azurerm" {
  features {}
}

import {
  to = azurerm_network_security_group.app
  id = "`+stateValue(t, e.state, "azurerm_network_security_group", "app", "id")+`"
}

resource "azurerm_network_security_group" "app" {
  name                = "infraharvest-e2e-app"
  location            = "westeurope"
  resource_group_name = "`+azureResourceGroup+`"
  tags                = { project = "infraharvest-e2e", env = "staging" }
}

import {
  to = azurerm_user_assigned_identity.ci
  id = "`+stateValue(t, e.state, "azurerm_user_assigned_identity", "ci", "id")+`"
}

resource "azurerm_user_assigned_identity" "ci" {
  name                = "infraharvest-e2e-ci"
  location            = "westeurope"
  resource_group_name = "`+azureResourceGroup+`"
  tags                = { project = "infraharvest-e2e", env = "test", role = "deploy" }
}
`, e.execPath, e.plugins)

	code := codeFiles(t, out)
	d := drift.mustExit(t, report.ExitDrift, "--detailed-exitcode")
	checkCodeUnchanged(t, "report only", code, out)
	group := driftChange(t, "report only", d, "azurerm_network_security_group", "tags.env")
	checkDriftChange(t, "report only", group, "test", "staging", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	identity := driftChange(t, "report only", d, "azurerm_user_assigned_identity", "tags.role")
	checkDriftChange(t, "report only", identity, "ci", "deploy", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if identity.ModuleCall != "" && (identity.Source == nil || identity.Source.ModulePath == "") {
		t.Errorf("report only: %s isn't traced through its module call %s: %+v", identity.Address, identity.ModuleCall, identity.Source)
	}
	if identity.ModuleCall == "" {
		t.Logf("the identity isn't in a module call (see manifest.json): its change is accepted at the resource")
	}

	d = drift.mustExit(t, report.ExitOK, "--accept", "attribute=tags.*", "--detailed-exitcode")
	checkDriftChange(t, "accepted", driftChange(t, "accepted", d, "azurerm_network_security_group", "tags.env"), "test", "staging", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	checkDriftChange(t, "accepted", driftChange(t, "accepted", d, "azurerm_user_assigned_identity", "tags.role"), "ci", "deploy", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	// The group's own env over the shared tags, which the others keep.
	if !codeMatches(t, out, `merge\(\s*local\.tags\s*,\s*\{\s*env\s*=\s*"staging"\s*\}\s*\)`) || !codeMatches(t, out, `env\s*=\s*"test"`) {
		t.Errorf("accepted: the group doesn't merge env = \"staging\" over local.tags, or local.tags lost env = \"test\"")
	}
	if !codeMatches(t, out, `role\s*=\s*"deploy"`) || codeMatches(t, out, `role\s*=\s*"ci"`) {
		t.Errorf("accepted: the code doesn't have the identity's role = \"deploy\" in place of \"ci\"")
	}

	checkAccepted(e.ctx, t, dirs, e.execPath, e.plugins)
	if d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode"); len(d.Changes) != 0 {
		t.Errorf("with the state refreshed: changes\n%s", driftChanges(d))
	}
}
