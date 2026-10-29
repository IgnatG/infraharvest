// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || azure)

package e2e

import (
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// azureMovesResourceGroup is the resource group testdata/azure-moves
// creates its resources in, which no other test lists.
const azureMovesResourceGroup = "infraharvest-e2e-moves"

// TestAzureRegenerateMoves imports testdata/azure-moves, three network
// security groups, one root per environment (--env-tags=env), and applies
// the root, keeping its state, as a team would. It then gives one group
// another env tag, so another root, and generates the roots again
// (--regenerate): the root it applied hands the group over with a removed
// block, and the other environment's root imports it (see
// handover.check). A resource's name is in its ID, so no group gets
// another address: moved blocks are the AWS test's. The resource group
// lands in no environment's root (floci-az doesn't keep its tags), and
// isn't imported: only the groups are listed.
func TestAzureRegenerateMoves(t *testing.T) {
	e := seedAzure(t)
	fixture := seedApplied(e.ctx, t, "azure-moves", e.execPath, e.plugins)
	out := t.TempDir()
	handover{
		ctx: e.ctx, execPath: e.execPath, plugins: e.plugins,
		typ:    "azurerm_network_security_group",
		handed: stateValue(t, fixture.resources(e.ctx, t), "azurerm_network_security_group", "handed", "id"),
		kept:   stateValue(t, fixture.resources(e.ctx, t), "azurerm_network_security_group", "kept", "id"),
		args: []string{
			"import", "azure",
			"--engine=" + e.engineName,
			"--terraform-path=" + e.execPath,
			"--resource-group=" + azureMovesResourceGroup,
			"--services=network_security_group",
			"--env-tags=env",
			"--modules=none",
			"--out=" + out,
			"--all",
			// Keep going after a failure, so one run reports every
			// problem; the checks on report.json still fail the test.
			"--allow-partial",
		},
		out:             out,
		emulatorFailure: azureEmulatorFailure,
		change: func(t *testing.T) {
			fixture.apply(e.ctx, t, tfexec.Var("handed_env=prod"))
		},
	}.check(t)
}
