// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// lastChangeLimit is how many drifted resources a run looks up in the
// cloud's audit log, across every root: CloudTrail answers two lookups a
// second in an account and region, so that a large drift doesn't keep the
// run waiting minutes on it. The others are left without one, and the run
// says so.
const lastChangeLimit = 50

// addLastChanges sets the LastChange of changes, the changes of each of
// targets, from the audit log of each root's provider, where it has one
// (see harvest.ProviderWithChangeHistory): the newest change the log
// records of the resource. A root whose provider keeps no log, or whose
// provider block names no settings to read it with, is left as it is; a
// log that can't be read is logged, and the drift run goes on.
func addLastChanges(ctx context.Context, options DriftOptions, targets []driftTarget, changes [][]report.DriftChange) {
	remaining := lastChangeLimit
	skipped := 0
	for i, target := range targets {
		if len(changes[i]) == 0 {
			continue
		}
		provider := rootProvider(options.Out, target.dir, VerifyOptions{Provider: options.Provider, Names: options.Names})
		history, ok := provider.(harvest.ProviderWithChangeHistory)
		if !ok {
			continue
		}
		var ids []string
		for _, c := range changes[i] {
			if len(c.IDs) == 0 || slices.Contains(ids, c.IDs[0]) {
				continue
			}
			if remaining == 0 {
				skipped++
				continue
			}
			remaining--
			for _, id := range c.IDs {
				if !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) == 0 {
			continue
		}
		name := relativePath(options.Out, target.dir)
		found, err := history.LastChanges(ctx, providerSettings(target.dir, catalogueName(provider.Name())), ids)
		if err != nil {
			log.Printf("drift: %s: who last changed the resources that drifted: %v", name, err)
		}
		for j := range changes[i] {
			c := &changes[i][j]
			for _, id := range c.IDs {
				if r, ok := found[id]; ok {
					c.LastChange = &report.DriftLastChange{By: r.By, Call: r.Call, At: r.At.UTC().Format(time.RFC3339), Log: r.Log}
					break
				}
			}
		}
	}
	if skipped > 0 {
		log.Printf("drift: looked up who last changed %d resources, the most a run looks up; %d more drifted, without it", lastChangeLimit, skipped)
	}
}

// providerSettings returns the arguments that the provider block of dir's
// providers.tf without an alias, of the provider whose catalogue is name
// (see catalogueName), sets to a string, such as its region: what the
// provider's audit log is read with. None when there is no such block,
// or its file doesn't parse.
func providerSettings(dir, name string) map[string]string {
	path := filepath.Join(dir, rootfile.ProvidersFileName)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	f, diags := hclsyntax.ParseConfig(content, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil
	}
	body, ok := f.Body.(*hclsyntax.Body)
	if !ok {
		return nil
	}
	for _, b := range body.Blocks {
		if b.Type != "provider" || len(b.Labels) != 1 || catalogueName(b.Labels[0]) != name {
			continue
		}
		if _, aliased := b.Body.Attributes["alias"]; aliased {
			continue
		}
		settings := map[string]string{}
		for attrName, attr := range b.Body.Attributes {
			if v, ok := hclfile.ConstantString(attr.Expr); ok {
				settings[attrName] = v
			}
		}
		return settings
	}
	return nil
}
