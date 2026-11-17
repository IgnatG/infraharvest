// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// A run calls the curated modules of the registry it is handed, and two
// runs with registries of their own never see each other's.
func TestRunUsesTheRegistryItIsGiven(t *testing.T) {
	t.Parallel()
	own := adapters.Adapter{Source: "example.com/own/queue", Version: "1.0.0", Anchor: "aws_sqs_queue"}
	reg := adapters.NewRegistry()
	reg.Add("aws", own)

	selected := map[string][]harvest.Resource{"sqs": {harvest.RecordBare("q", "q", "aws_sqs_queue", "aws")}}
	var looked []string
	tools := &callTools{moduleReleases: func(_ context.Context, source string) ([]engine.ModuleRelease, error) {
		looked = append(looked, source)
		return nil, errors.New("offline")
	}}
	given := NewRun().WithRegistry(reg)
	given.tools = tools
	list, _, _ := given.registryModules(t.Context(), &moduleQueryProvider{}, ModulesRegistry, selected)
	if len(list) != 1 || list[0].Source != own.Source || len(looked) != 1 || looked[0] != own.Source {
		t.Errorf("the run called %+v and looked up %v, want the given registry's %s alone", list, looked, own.Source)
	}

	// Another run, without a registry of its own, has the built-in ones, which the
	// first one's change left as they were.
	other := NewRun()
	other.tools = tools
	looked = nil
	if list, _, _ := other.registryModules(t.Context(), &moduleQueryProvider{}, ModulesRegistry, selected); len(list) != 0 || len(looked) != 0 {
		t.Errorf("another run called %+v, want no module for a queue", list)
	}
	if got := other.curated().For("aws"); len(got) != len(adapters.Builtin().For("aws")) {
		t.Errorf("another run has %d aws adapters, want the built-in ones", len(got))
	}
	if got := (&Run{}).curated(); got == nil || len(got.For("aws")) == 0 {
		t.Error("a run without a registry has no built-in one")
	}
}
