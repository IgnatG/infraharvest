// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// EffectiveTags describes a provider whose resources' tags attribute holds
// only the tags their configuration sets, such as the Google provider's
// labels since its version 5.0: an import leaves it empty, and another
// attribute, such as effective_labels, has every tag the resource has.
// Terraform then generates no tags, so Generate writes them from that
// attribute (see fillEffectiveTags).
type EffectiveTags struct {
	// Attribute is the resources' tags attribute, e.g. labels.
	Attribute string
	// Effective is the attribute with every tag a resource has, e.g.
	// effective_labels.
	Effective string
	// ReservedPrefix starts the keys of tags the cloud or the provider
	// sets itself, e.g. goog-, which Generate leaves out: the tags
	// attribute only manages the tags it has, so those stay as they are.
	ReservedPrefix string
}

// maxFillRounds bounds how often fillEffectiveTags plans.
const maxFillRounds = 2

// fillEffectiveTags writes each resource's tags, without reserved ones,
// from its imported et.Effective into et.Attribute, where the generated
// configuration doesn't set it: values are the imported attribute values
// by address (see planValues). The plan then updates et.Attribute and what
// the provider computes from it in state, which stateOnly names for the
// type (the Google provider's labels and terraform_labels), and nothing
// in the cloud. A resource that then plans any other change, against
// baseline (see StateOnly), keeps no tags, and is logged; the others keep
// theirs. A change of a resource it didn't fill is the plan's own,
// as an autoscaled group's size may be, and changes nothing. If the
// configuration doesn't plan, no resource keeps tags. It reports whether
// it changed generated.tf and, where a plan showed generated.tf as it
// leaves it, that plan (see shownPlan).
func fillEffectiveTags(ctx context.Context, tf tfrun.Terraform, dir string, et EffectiveTags, values map[string]map[string]any, baseline Baseline, vars []tfexec.PlanOption) (bool, *shownPlan, error) {
	backup, err := fsutil.BackupFiles(dir, rootfile.GeneratedFileName)
	if err != nil {
		return false, nil, err
	}
	left := map[string]bool{}
	for round := 0; round < maxFillRounds; round++ {
		if round > 0 {
			if err := backup.Restore(); err != nil {
				return false, nil, err
			}
		}
		filled, err := fillTags(dir, et, values, left)
		if err != nil {
			return false, nil, errors.Join(err, backup.Restore())
		}
		if len(filled) == 0 {
			return false, nil, nil
		}
		p, summary, diags, err := tfrun.ShowPlan(ctx, tf, vars)
		if err != nil {
			return false, nil, errors.Join(err, backup.Restore())
		}
		shown := shownPlan{p, summary}
		verdict := baseline.Judge(shown, diags, StateOnly)
		if verdict.Failed {
			return false, nil, backup.Restore()
		}
		settled := true
		for _, rc := range verdict.Changed {
			if !slices.Contains(filled, rc.Address) {
				// Tags on other resources can't change it.
				continue
			}
			left[rc.Address] = true
			settled = false
		}
		if settled {
			logUnfilled(dir, et, left)
			return true, &shownPlan{plan: p, summary: summary}, nil
		}
	}
	// Out of rounds: the resources that settled keep their tags.
	if err := backup.Restore(); err != nil {
		return false, nil, err
	}
	filled, err := fillTags(dir, et, values, left)
	if err != nil {
		return false, nil, errors.Join(err, backup.Restore())
	}
	logUnfilled(dir, et, left)
	return len(filled) > 0, nil, nil
}

// logUnfilled logs the resources fillEffectiveTags left without their
// tags, as their plan changed with them: the tags stay on the resources,
// but the configuration doesn't manage them.
func logUnfilled(dir string, et EffectiveTags, left map[string]bool) {
	if len(left) == 0 {
		return
	}
	log.Printf("%s: leaving %s out of %s, whose plans change with them; the resources keep them, unmanaged", dir, et.Attribute, strings.Join(slices.Sorted(maps.Keys(left)), ", "))
}

// fillTags sets et.Attribute of each resource in generated.tf whose
// imported values have it, unset, null or empty, to its et.Effective
// without reserved tags, but for the resources in skip. It returns the
// addresses of the resources it set.
func fillTags(dir string, et EffectiveTags, values map[string]map[string]any, skip map[string]bool) ([]string, error) {
	generated, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		return nil, err
	}
	var filled []string
	for _, r := range generated.Resources() {
		imported, ok := values[r.Address]
		if !ok || skip[r.Address] {
			continue
		}
		// Only a type with the attribute: its imported values list every
		// attribute of the type's schema, null or not.
		if _, ok := imported[et.Attribute]; !ok {
			continue
		}
		if attr, ok := r.Syntax.Body.Attributes[et.Attribute]; ok {
			if tags, ok := hclfile.StringMap(attr.Expr); !ok || len(tags) > 0 {
				continue
			}
		}
		effective, _ := imported[et.Effective].(map[string]any)
		tags := map[string]string{}
		for k, v := range effective {
			if s, ok := v.(string); ok && (et.ReservedPrefix == "" || !strings.HasPrefix(k, et.ReservedPrefix)) {
				tags[k] = s
			}
		}
		if len(tags) == 0 {
			continue
		}
		setBeforeBlocks(r.Write.Body(), et.Attribute, mapValue(tags))
		filled = append(filled, r.Address)
	}
	if len(filled) == 0 {
		return nil, nil
	}
	return filled, generated.Save()
}

// setBeforeBlocks sets the attribute name of body to value where the style
// guide has arguments, before the nested blocks: a new attribute would
// otherwise go after them, at the end of the body.
func setBeforeBlocks(body *hclwrite.Body, name string, value cty.Value) {
	if body.GetAttribute(name) != nil {
		body.SetAttributeValue(name, value)
		return
	}
	blocks := body.Blocks()
	for _, b := range blocks {
		body.RemoveBlock(b)
	}
	body.SetAttributeValue(name, value)
	for _, b := range blocks {
		body.AppendBlock(b)
	}
}
