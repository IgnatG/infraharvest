// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// Lifecycle is what every resource of a type gets in its lifecycle block,
// such as create_before_destroy for an API Gateway deployment, which a
// stage refers to: Reason goes in a comment above it. Lifecycle settings
// change how Terraform replaces a resource, never what the plan of an
// import shows.
type Lifecycle struct {
	CreateBeforeDestroy bool
	Reason              string
}

// SetLifecycles adds to the resources in the file at path the lifecycle
// settings lifecycles has for their type, and reports whether it changed
// anything. A resource that already sets one keeps its own.
func SetLifecycles(path string, lifecycles map[string]Lifecycle) (bool, error) {
	if len(lifecycles) == 0 {
		return false, nil
	}
	return rewrite(path, func(f *hclwrite.File, _ *hclsyntax.Body) bool {
		changed := false
		for _, block := range f.Body().Blocks() {
			if block.Type() != "resource" || len(block.Labels()) != 2 {
				continue
			}
			l, ok := lifecycles[block.Labels()[0]]
			if !ok || !l.CreateBeforeDestroy {
				continue
			}
			lifecycle := lifecycleBlock(block.Body())
			if lifecycle.GetAttribute("create_before_destroy") != nil {
				continue
			}
			if l.Reason != "" {
				lifecycle.AppendUnstructuredTokens(hclwrite.Tokens{
					{Type: hclsyntax.TokenComment, Bytes: []byte("# " + strings.ToUpper(l.Reason[:1]) + l.Reason[1:] + ".\n")},
				})
			}
			lifecycle.SetAttributeValue("create_before_destroy", cty.True)
			changed = true
		}
		return changed
	})
}

// lifecycleBlock returns the body of a resource's lifecycle block, which
// it adds if the resource has none.
func lifecycleBlock(body *hclwrite.Body) *hclwrite.Body {
	for _, b := range body.Blocks() {
		if b.Type() == "lifecycle" {
			return b.Body()
		}
	}
	return body.AppendNewBlock("lifecycle", nil).Body()
}
