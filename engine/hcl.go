// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// Import is one existing resource to bring under Terraform.
type Import struct {
	Type string // resource type, e.g. aws_sqs_queue
	Name string // resource name in the configuration
	ID   string // the provider's import ID
}

// Provider describes the provider the generated configuration uses.
type Provider struct {
	Name    string                 // local name, e.g. aws
	Source  string                 // registry source, e.g. hashicorp/aws
	Version string                 // version constraint; empty means latest
	Config  map[string]interface{} // provider block arguments
}

// ImportsFile renders one import block per resource, sorted by address.
func ImportsFile(imports []Import) ([]byte, error) {
	sorted := append([]Import(nil), imports...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Type != sorted[j].Type {
			return sorted[i].Type < sorted[j].Type
		}
		return sorted[i].Name < sorted[j].Name
	})
	f := hclwrite.NewEmptyFile()
	body := f.Body()
	for i, imp := range sorted {
		for _, part := range []string{imp.Type, imp.Name} {
			if !hclsyntax.ValidIdentifier(part) {
				return nil, fmt.Errorf("invalid resource address %s.%s", imp.Type, imp.Name)
			}
		}
		if i > 0 {
			body.AppendNewline()
		}
		block := body.AppendNewBlock("import", nil).Body()
		block.SetAttributeTraversal("to", hcl.Traversal{
			hcl.TraverseRoot{Name: imp.Type},
			hcl.TraverseAttr{Name: imp.Name},
		})
		block.SetAttributeValue("id", cty.StringVal(imp.ID))
	}
	return hclwrite.Format(f.Bytes()), nil
}

// VersionsFile renders the terraform block: the Terraform versions the
// configuration accepts (none if requiredVersion is empty) and the provider
// requirement.
func VersionsFile(requiredVersion string, p Provider) []byte {
	f := hclwrite.NewEmptyFile()
	terraform := f.Body().AppendNewBlock("terraform", nil).Body()
	if requiredVersion != "" {
		terraform.SetAttributeValue("required_version", cty.StringVal(requiredVersion))
	}
	requirement := map[string]cty.Value{"source": cty.StringVal(p.Source)}
	if p.Version != "" {
		requirement["version"] = cty.StringVal(p.Version)
	}
	terraform.AppendNewBlock("required_providers", nil).Body().SetAttributeValue(p.Name, cty.ObjectVal(requirement))
	return hclwrite.Format(f.Bytes())
}

// ProvidersFile renders the provider block.
func ProvidersFile(p Provider) ([]byte, error) {
	f := hclwrite.NewEmptyFile()
	if err := writeBody(f.Body().AppendNewBlock("provider", []string{p.Name}).Body(), p.Config); err != nil {
		return nil, fmt.Errorf("provider %s: %w", p.Name, err)
	}
	return hclwrite.Format(f.Bytes()), nil
}

// writeBody writes arguments in sorted order. Maps become nested blocks;
// lists of maps become repeated nested blocks.
func writeBody(body *hclwrite.Body, args map[string]interface{}) error {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch v := args[k].(type) {
		case map[string]interface{}:
			if err := writeBody(body.AppendNewBlock(k, nil).Body(), v); err != nil {
				return err
			}
		case []map[string]interface{}:
			for _, item := range v {
				if err := writeBody(body.AppendNewBlock(k, nil).Body(), item); err != nil {
					return err
				}
			}
		default:
			val, err := ctyValue(v)
			if err != nil {
				return fmt.Errorf("argument %s: %w", k, err)
			}
			body.SetAttributeValue(k, val)
		}
	}
	return nil
}

func ctyValue(v interface{}) (cty.Value, error) {
	switch v := v.(type) {
	case string:
		return cty.StringVal(v), nil
	case bool:
		return cty.BoolVal(v), nil
	case int:
		return cty.NumberIntVal(int64(v)), nil
	case int64:
		return cty.NumberIntVal(v), nil
	case float64:
		return cty.NumberFloatVal(v), nil
	case []string:
		if len(v) == 0 {
			return cty.ListValEmpty(cty.String), nil
		}
		vals := make([]cty.Value, len(v))
		for i, s := range v {
			vals[i] = cty.StringVal(s)
		}
		return cty.ListVal(vals), nil
	default:
		return cty.NilVal, fmt.Errorf("unsupported type %T", v)
	}
}
