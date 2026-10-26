// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/stackintent"
	"github.com/IgnatG/infraharvest/pkg/cfn"
)

// mergedRootNoTemplate is the warning of a root merged with its stack's
// other environments, which takes no stack template (see generate).
const mergedRootNoTemplate = "the CloudFormation stack's template isn't carried into this root, as it is merged with its stack's other environments, whose stacks have parameter values of their own"

// stackRefine is an engine.Options.Refine.
type stackRefine = func(context.Context, string, []engine.Managed, *tfjson.ProviderSchemas) ([]string, error)

// stackIntent returns the engine.Options.Refine that carries the templates
// of the CloudFormation stacks the call is scoped to (AWS --cfn-stack)
// into each root it generates (see stackintent.Apply), or nil when it has
// none. The stacks are read once, before the roots generate: each root
// gets the resources of each stack it imports, by where the call listed
// them and their type and import ID, which is how the report matches
// them too. The provider names the Terraform side of the templates if it
// can (see harvest.ProviderWithStackMapper), and cfn.DefaultMapper
// otherwise, whose guesses stackintent doesn't apply.
//
// An incremental import, which adds to a root rather than generating it,
// doesn't carry the templates into it.
//
// The key it also returns identifies the templates, parameter values and
// logical IDs, for the fingerprint --resume compares (see
// engine.Options.RefineKey).
func (c *listedCall) stackIntent() (stackRefine, string) {
	var inputs []stackInput
	for _, s := range c.run.report.CloudFormationStacks {
		body := s.TemplateBody()
		if body == "" {
			continue
		}
		in := stackInput{
			stack:     stackintent.Stack{Name: s.Name, ID: s.ID, Template: body, Parameters: s.Parameters()},
			resources: map[engine.External][]string{},
		}
		for _, res := range s.Resources {
			for _, m := range res.Matches {
				if m.Scope != c.scope {
					continue
				}
				key := engine.External{Type: m.Type, ID: m.ID}
				in.resources[key] = append(in.resources[key], res.LogicalID)
			}
		}
		if len(in.resources) > 0 {
			inputs = append(inputs, in)
		}
	}
	if len(inputs) == 0 {
		return nil, ""
	}
	key, err := stackIntentKey(inputs)
	if err != nil {
		return nil, ""
	}
	withMapper, _ := c.provider.(harvest.ProviderWithStackMapper)
	refine := func(_ context.Context, dir string, addresses []engine.Managed, schemas *tfjson.ProviderSchemas) ([]string, error) {
		var stacks []stackintent.Stack
		for _, in := range inputs {
			st := in.stack
			st.Addresses = map[string][]string{}
			for _, a := range addresses {
				for _, logicalID := range in.resources[engine.External{Type: a.Type, ID: a.ID}] {
					st.Addresses[logicalID] = append(st.Addresses[logicalID], a.Address)
				}
			}
			if len(st.Addresses) > 0 {
				stacks = append(stacks, st)
			}
		}
		if len(stacks) == 0 {
			return nil, nil
		}
		mapper := cfn.DefaultMapper()
		if withMapper != nil {
			mapper = withMapper.StackMapper(schemas)
		}
		return stackintent.Apply(dir, stacks, mapper)
	}
	return refine, key
}

// stackInput is a stack whose template stackIntent carries into roots.
type stackInput struct {
	stack     stackintent.Stack
	resources map[engine.External][]string // logical IDs by type and import ID
}

// stackIntentKey identifies inputs (see engine.Options.RefineKey).
func stackIntentKey(inputs []stackInput) (string, error) {
	type keyed struct {
		Name, ID, Template string
		Parameters         map[string]string
		Resources          []string
	}
	var all []keyed
	for _, in := range inputs {
		k := keyed{Name: in.stack.Name, ID: in.stack.ID, Template: in.stack.Template, Parameters: in.stack.Parameters}
		for r, ids := range in.resources {
			k.Resources = append(k.Resources, r.Type+" "+r.ID+" "+strings.Join(ids, ","))
		}
		sort.Strings(k.Resources)
		all = append(all, k)
	}
	content, err := json.Marshal(all)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}
