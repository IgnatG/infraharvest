// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
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
// changes (see changeSignature), keeps no tags; if the configuration
// doesn't plan, none does. It reports whether it changed generated.tf.
func fillEffectiveTags(ctx context.Context, tf Terraform, dir string, et EffectiveTags, values map[string]map[string]any, changes map[string]string, stateOnly map[string][]string, vars []tfexec.PlanOption) (bool, error) {
	backup, err := backupFiles(dir, GeneratedFileName)
	if err != nil {
		return false, err
	}
	left := map[string]bool{}
	for round := 0; round < maxFillRounds; round++ {
		if round > 0 {
			if err := backup.restore(); err != nil {
				return false, err
			}
		}
		filled, err := fillTags(dir, et, values, left)
		if err != nil {
			return false, errors.Join(err, backup.restore())
		}
		if len(filled) == 0 {
			return false, nil
		}
		p, _, diags, err := showPlanWithSummary(ctx, tf, vars)
		if err != nil {
			return false, errors.Join(err, backup.restore())
		}
		if len(diags) > 0 || p == nil {
			return false, backup.restore()
		}
		settled := true
		for _, rc := range p.ResourceChanges {
			if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
				continue
			}
			signature := changeSignature(rc)
			if signature == changes[rc.Address] {
				continue
			}
			if _, same := sameButStateOnly(signature, changes[rc.Address], stateOnlyOf(stateOnly, rc.Type)); same {
				continue
			}
			if !slices.Contains(filled, rc.Address) {
				// A change no tags explain: leave the configuration as it was.
				return false, backup.restore()
			}
			left[rc.Address] = true
			settled = false
		}
		if settled {
			return true, nil
		}
	}
	return false, backup.restore()
}

// fillTags sets et.Attribute of each resource in generated.tf whose
// imported values have it, unset, null or empty, to its et.Effective
// without reserved tags, but for the resources in skip. It returns the
// addresses of the resources it set.
func fillTags(dir string, et EffectiveTags, values map[string]map[string]any, skip map[string]bool) ([]string, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	var filled []string
	for _, r := range generated.resources() {
		imported, ok := values[r.address]
		if !ok || skip[r.address] {
			continue
		}
		// Only a type with the attribute: its imported values list every
		// attribute of the type's schema, null or not.
		if _, ok := imported[et.Attribute]; !ok {
			continue
		}
		if attr, ok := r.syntax.Body.Attributes[et.Attribute]; ok {
			if tags, ok := stringMap(attr.Expr); !ok || len(tags) > 0 {
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
		setBeforeBlocks(r.write.Body(), et.Attribute, mapValue(tags))
		filled = append(filled, r.address)
	}
	if len(filled) == 0 {
		return nil, nil
	}
	return filled, generated.save()
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
