// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// readVPCPart reads a resource's tags: its Name tag, which the module sets
// for every resource it makes, and the others. Of tags a root has lifted
// into a local, local.tags or merge(local.tags, {...}), it reads the local
// and the literal tags merged over it.
func readVPCPart(m Resource) (*vpcPart, error) {
	p := &vpcPart{Resource: m, r: Read(m.Address(), m.Body), tags: map[string]string{}}
	if tokens, ok := p.r.Attr("tags"); ok {
		base, v, ok := splitLocalTags(tokens)
		if !ok || (!v.IsNull() && !v.Type().IsMapType() && !v.Type().IsObjectType()) {
			return nil, Declinef("%s's tags aren't literal", m.Address())
		}
		p.base = base
		if !v.IsNull() {
			for it := v.ElementIterator(); it.Next(); {
				k, val := it.Element()
				if val.IsNull() || val.Type() != cty.String {
					return nil, Declinef("%s's tag %s isn't a string", m.Address(), k.AsString())
				}
				p.tags[k.AsString()] = val.AsString()
			}
		}
	}
	p.name = p.tags["Name"]
	delete(p.tags, "Name")
	if p.name == "" {
		return nil, Declinef("%s has no Name tag, and the module names it", m.Address())
	}
	return p, nil
}

// splitLocalTags splits tags that refer to a local, local.<name> or
// merge(local.<name>, {...}), into the local and the literal tags merged
// over it, a null value for none; literal tags have no local. It reports
// false for tags that refer to anything else.
func splitLocalTags(tokens hclwrite.Tokens) (hclwrite.Tokens, cty.Value, bool) {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, cty.NilVal, false
	}
	switch e := expr.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		base, ok := localTraversal(e)
		return base, cty.NilVal, ok
	case *hclsyntax.FunctionCallExpr:
		if e.Name != "merge" || len(e.Args) != 2 || len(e.Args[1].Variables()) > 0 {
			return nil, cty.NilVal, false
		}
		traversal, isTraversal := e.Args[0].(*hclsyntax.ScopeTraversalExpr)
		if !isTraversal {
			return nil, cty.NilVal, false
		}
		base, ok := localTraversal(traversal)
		if !ok {
			return nil, cty.NilVal, false
		}
		v, diags := e.Args[1].Value(nil)
		if diags.HasErrors() || !v.IsWhollyKnown() || v.IsNull() {
			return nil, cty.NilVal, false
		}
		return base, v, true
	}
	v, ok := literal(tokens)
	return nil, v, ok
}

// localTraversal returns the tokens of a reference to a local, such as
// local.tags.
func localTraversal(e *hclsyntax.ScopeTraversalExpr) (hclwrite.Tokens, bool) {
	if len(e.Traversal) != 2 || e.Traversal.RootName() != "local" {
		return nil, false
	}
	return hclwrite.TokensForTraversal(e.Traversal), true
}

func readVPCSubnet(m, vpc Resource) (*vpcSubnet, error) {
	p, err := readVPCPart(m)
	if err != nil {
		return nil, err
	}
	s := &vpcSubnet{vpcPart: *p}
	if !s.r.RefersTo("vpc_id", vpc) {
		return nil, Declinef("%s isn't in %s", m.Address(), vpc.Address())
	}
	az, ok := literalString(mustAttr(s.r, "availability_zone"))
	if !ok {
		return nil, Declinef("%s has no literal availability zone", m.Address())
	}
	s.az = az
	if s.cidr, ok = s.r.Attr("cidr_block"); !ok {
		return nil, Declinef("%s has no IPv4 CIDR block", m.Address())
	}
	s.cidrKey = string(s.cidr.Bytes())
	if cidr, ok := literalString(s.cidr); ok {
		s.cidrKey = cidr
	}
	// Set from availability_zone; Terraform-only or the provider's own
	// defaults, which the module keeps.
	s.r.Ignore("availability_zone_id")
	for _, arg := range []string{
		"assign_ipv6_address_on_creation", "enable_dns64", "enable_resource_name_dns_a_record_on_launch",
		"enable_resource_name_dns_aaaa_record_on_launch", "ipv6_native", "map_customer_owned_ip_on_launch",
	} {
		s.r.IgnoreDefault(arg, cty.False)
	}
	s.r.IgnoreDefault("enable_lni_at_device_index", cty.Zero)
	s.r.IgnoreDefault("private_dns_hostname_type_on_launch", cty.StringVal("ip-name"))
	return s, nil
}

// vpcRouteKind returns whether a route table's only route is the default
// route to the internet gateway or to one of the NAT gateways, and which.
func vpcRouteKind(t *vpcPart, igw *vpcPart, nats []*vpcPart) (vpcRoute, *vpcPart, error) {
	if vgws, ok := t.r.Attr("propagating_vgws"); ok && !emptyList(vgws) {
		return 0, nil, Declinef("%s propagates virtual private gateway routes", t.Address())
	}
	routes, err := readRoutes(t)
	if err != nil {
		return 0, nil, err
	}
	if len(routes) == 1 && literalEquals(routes[0]["cidr_block"], "0.0.0.0/0") && len(routes[0]) == 2 {
		if gateway, ok := routes[0]["gateway_id"]; ok && igw != nil && refersTo(gateway, igw.Resource) {
			return publicRoute, nil, nil
		}
		if gateway, ok := routes[0]["nat_gateway_id"]; ok {
			for _, nat := range nats {
				if refersTo(gateway, nat.Resource) {
					return privateRoute, nat, nil
				}
			}
		}
	}
	return 0, nil, Declinef("%s has routes other than one default route to the VPC's internet or NAT gateway, and the module can't set them", t.Address())
}

// readRoutes returns a route table's routes, each with its arguments that
// are set.
func readRoutes(t *vpcPart) ([]map[string]hclsyntax.Expression, error) {
	tokens, ok := t.r.Attr("route")
	if !ok {
		return nil, nil
	}
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	tuple, ok := expr.(*hclsyntax.TupleConsExpr)
	if diags.HasErrors() || !ok {
		return nil, Declinef("%s's routes aren't a list", t.Address())
	}
	var routes []map[string]hclsyntax.Expression
	for _, e := range tuple.Exprs {
		object, ok := e.(*hclsyntax.ObjectConsExpr)
		if !ok {
			return nil, Declinef("%s has a route that isn't an object", t.Address())
		}
		route := map[string]hclsyntax.Expression{}
		for _, item := range object.Items {
			key := hcl.ExprAsKeyword(item.KeyExpr)
			if key == "" {
				return nil, Declinef("%s has a route argument without a name", t.Address())
			}
			if unset(item.ValueExpr) {
				continue
			}
			route[key] = item.ValueExpr
		}
		routes = append(routes, route)
	}
	return routes, nil
}

// unset reports whether an expression is null or an empty string, as a
// route's arguments that aren't set are.
func unset(expr hclsyntax.Expression) bool {
	if len(expr.Variables()) > 0 {
		return false
	}
	v, diags := expr.Value(nil)
	return !diags.HasErrors() && (v.IsNull() || (v.Type() == cty.String && v.IsKnown() && v.AsString() == ""))
}

func literalEquals(expr hclsyntax.Expression, want string) bool {
	if expr == nil || len(expr.Variables()) > 0 {
		return false
	}
	v, diags := expr.Value(nil)
	return !diags.HasErrors() && v.Type() == cty.String && v.IsKnown() && !v.IsNull() && v.AsString() == want
}
