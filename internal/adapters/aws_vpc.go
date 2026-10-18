// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// VPC maps a VPC, its subnets, internet gateway and route tables, the
// tables' subnet associations, and its NAT gateways with their Elastic IPs
// onto terraform-aws-modules/vpc/aws. It takes the shapes the module
// builds: public subnets sharing one route table whose only route is the
// default route to the internet gateway, and private subnets sharing one
// route table whose only route is the default route to a single NAT
// gateway in a public subnet (single_nat_gateway), or with a NAT gateway
// in each zone's public subnet, each zone's private subnet having a route
// table of its own whose only route is the default route to the gateway
// in its zone (one_nat_gateway_per_az). The module names every resource
// after the VPC, so it declines resources whose Name tags differ. The root
// keeps the default routes in its route tables; the module keeps them as
// resources of their own, which the call imports. The module leaves the
// VPC's default security group, network ACL and main route table alone:
// route tables without subnets stay in the root.
var VPC = Adapter{
	Source:  "terraform-aws-modules/vpc/aws",
	Version: "6.7.3",
	Anchor:  "aws_vpc",
	Members: []string{
		"aws_eip", "aws_internet_gateway", "aws_nat_gateway", "aws_route_table",
		"aws_route_table_association", "aws_subnet",
	},
	Repeated: []string{"aws_eip", "aws_nat_gateway", "aws_route_table", "aws_route_table_association", "aws_subnet"},
	Through:  []string{"aws_eip", "aws_nat_gateway", "aws_route_table_association"},
	Inputs: []string{
		"azs", "cidr", "enable_dns_hostnames", "enable_dns_support", "enable_nat_gateway",
		"enable_network_address_usage_metrics", "igw_tags", "instance_tenancy",
		"manage_default_network_acl", "manage_default_route_table", "manage_default_security_group",
		"map_public_ip_on_launch", "name", "nat_eip_tags", "nat_gateway_tags", "one_nat_gateway_per_az",
		"private_route_table_tags", "private_subnet_names", "private_subnet_suffix", "private_subnet_tags",
		"private_subnets", "public_route_table_tags", "public_subnet_names", "public_subnet_suffix",
		"public_subnet_tags", "public_subnets", "single_nat_gateway", "tags", "vpc_tags",
	},
	Outputs: []string{
		"default_network_acl_id", "default_route_table_id", "default_security_group_id", "igw_arn", "igw_id",
		"nat_ids", "nat_public_ips", "natgw_ids", "natgw_interface_ids", "private_route_table_association_ids",
		"private_route_table_ids", "private_subnet_arns", "private_subnets", "private_subnets_cidr_blocks",
		"public_route_table_association_ids", "public_route_table_ids", "public_subnet_arns", "public_subnets",
		"public_subnets_cidr_blocks", "vpc_arn", "vpc_cidr_block", "vpc_enable_dns_hostnames",
		"vpc_enable_dns_support", "vpc_id", "vpc_instance_tenancy", "vpc_main_route_table_id", "vpc_owner_id",
	},
	Targets: []string{
		"aws_eip.nat", "aws_internet_gateway.this", "aws_nat_gateway.this", "aws_route.private_nat_gateway",
		"aws_route.public_internet_gateway", "aws_route_table.private", "aws_route_table.public",
		"aws_route_table_association.private", "aws_route_table_association.public", "aws_subnet.private",
		"aws_subnet.public", "aws_vpc.this",
	},
	Map: mapVPC,
	// The module's variables' defaults, from its variables.tf; its
	// enable_network_address_usage_metrics defaults to null, which the
	// provider takes as false. enable_dns_hostnames defaults to true,
	// unlike the provider's false, so a VPC without it passes false.
	Defaults: map[string]cty.Value{
		"enable_dns_hostnames":                 cty.True,
		"enable_dns_support":                   cty.True,
		"enable_network_address_usage_metrics": cty.False,
		"instance_tenancy":                     cty.StringVal("default"),
		"map_public_ip_on_launch":              cty.False,
		"private_subnet_suffix":                cty.StringVal("private"),
		"public_subnet_suffix":                 cty.StringVal("public"),
	},
}

// vpcPart is a resource of a VPC cluster, read.
type vpcPart struct {
	Resource
	r    *Reader
	name string            // its Name tag
	tags map[string]string // its other tags
	// base is the local its tags are merged over, such as local.tags,
	// or nil.
	base hclwrite.Tokens
}

// vpcSubnet is a subnet of a VPC cluster, with its route table.
type vpcSubnet struct {
	vpcPart
	az, cidrKey string
	cidr        hclwrite.Tokens
	public      bool
	association *vpcPart
	table       *vpcPart
}

// vpcRoute is the kind of a route table: public, routing to the internet
// gateway, or private, routing to a NAT gateway.
type vpcRoute int

const (
	publicRoute vpcRoute = iota + 1
	privateRoute
)

func mapVPC(c Cluster) (*Call, error) {
	vpc, err := readVPCPart(c.Anchor)
	if err != nil {
		return nil, err
	}
	call := &Call{
		Addresses: map[string]string{c.Anchor.Address(): "aws_vpc.this[0]"},
		Outputs: map[string]map[string]string{c.Anchor.Address(): {
			"arn":                       "vpc_arn",
			"cidr_block":                "vpc_cidr_block",
			"default_network_acl_id":    "default_network_acl_id",
			"default_route_table_id":    "default_route_table_id",
			"default_security_group_id": "default_security_group_id",
			"enable_dns_hostnames":      "vpc_enable_dns_hostnames",
			"enable_dns_support":        "vpc_enable_dns_support",
			"id":                        "vpc_id",
			"instance_tenancy":          "vpc_instance_tenancy",
			"main_route_table_id":       "vpc_main_route_table_id",
			"owner_id":                  "vpc_owner_id",
		}},
	}
	call.Set("name", hclwrite.TokensForValue(cty.StringVal(vpc.name)))
	cidr, ok := vpc.r.Attr("cidr_block")
	if !ok {
		return nil, Declinef("%s has no CIDR block", vpc.Address())
	}
	call.Set("cidr", cidr)
	for _, arg := range []string{"enable_dns_support", "enable_network_address_usage_metrics", "instance_tenancy"} {
		if value, ok := vpc.r.Attr(arg); ok {
			call.Set(arg, value)
		}
	}
	// The module's default is true, the provider's false.
	hostnames, ok := vpc.r.Attr("enable_dns_hostnames")
	if !ok {
		hostnames = hclwrite.TokensForValue(cty.False)
	}
	call.Set("enable_dns_hostnames", hostnames)
	vpc.r.IgnoreDefault("assign_generated_ipv6_cidr_block", cty.False)
	if err := vpc.r.Done(); err != nil {
		return nil, err
	}
	for _, arg := range []string{"manage_default_network_acl", "manage_default_route_table", "manage_default_security_group"} {
		call.Set(arg, hclwrite.TokensForValue(cty.False))
	}

	var igw *vpcPart
	var nats, eips, tables, associations []*vpcPart
	var subnets []*vpcSubnet
	var tableResources []Resource
	for _, m := range c.Members {
		switch m.Type {
		case "aws_subnet":
			s, err := readVPCSubnet(m, c.Anchor)
			if err != nil {
				return nil, err
			}
			subnets = append(subnets, s)
			continue
		case "aws_route_table_association":
			associations = append(associations, &vpcPart{Resource: m, r: Read(m.Address(), m.Body)})
			continue
		case "aws_route_table":
			tableResources = append(tableResources, m)
			continue
		}
		p, err := readVPCPart(m)
		if err != nil {
			return nil, err
		}
		switch m.Type {
		case "aws_internet_gateway":
			if !p.r.RefersTo("vpc_id", c.Anchor) {
				return nil, Declinef("%s isn't attached to %s", m.Address(), c.Anchor.Address())
			}
			igw = p
		case "aws_nat_gateway":
			nats = append(nats, p)
		case "aws_eip":
			eips = append(eips, p)
		}
	}
	// The route tables of the subnets; the others, such as the VPC's main
	// route table, which the module leaves alone, stay in the root.
	for _, t := range tableResources {
		if !slices.ContainsFunc(associations, func(a *vpcPart) bool { return a.r.RefersTo("route_table_id", t) }) {
			call.Left = append(call.Left, t.Address())
			continue
		}
		p, err := readVPCPart(t)
		if err != nil {
			return nil, err
		}
		if !p.r.RefersTo("vpc_id", c.Anchor) {
			return nil, Declinef("%s isn't in %s", t.Address(), c.Anchor.Address())
		}
		tables = append(tables, p)
	}

	kinds := map[string]vpcRoute{}
	// natOf has, by private route table, the NAT gateway it routes to.
	natOf := map[string]*vpcPart{}
	var publicTable *vpcPart
	var privateTables []*vpcPart
	for _, t := range tables {
		kind, nat, err := vpcRouteKind(t, igw, nats)
		if err != nil {
			return nil, err
		}
		kinds[t.Address()] = kind
		switch {
		case kind == publicRoute && publicTable == nil:
			publicTable = t
		case kind == privateRoute && !slices.ContainsFunc(privateTables, func(p *vpcPart) bool { return natOf[p.Address()] == nat }):
			privateTables = append(privateTables, t)
			natOf[t.Address()] = nat
		default:
			return nil, Declinef("%s has more than one route table that routes the same way, and the module makes one per gateway", c.Anchor.Address())
		}
	}
	for _, a := range associations {
		var subnet *vpcSubnet
		for _, s := range subnets {
			if a.r.RefersTo("subnet_id", s.Resource) {
				subnet = s
			}
		}
		var table *vpcPart
		for _, t := range tables {
			if a.r.RefersTo("route_table_id", t.Resource) {
				table = t
			}
		}
		if subnet == nil || table == nil {
			return nil, Declinef("%s doesn't associate a subnet of %s with one of its route tables", a.Address(), c.Anchor.Address())
		}
		if err := a.r.Done(); err != nil {
			return nil, err
		}
		subnet.association = a
		subnet.table = table
		subnet.public = kinds[table.Address()] == publicRoute
	}

	var public, private []*vpcSubnet
	for _, s := range subnets {
		if s.association == nil {
			return nil, Declinef("%s has no route table of its own, and the module associates every subnet with one", s.Address())
		}
		if s.public {
			public = append(public, s)
		} else {
			private = append(private, s)
		}
	}
	switch {
	case igw != nil && len(public) == 0:
		return nil, Declinef("%s has an internet gateway but no public subnet, and the module makes one only for public subnets", c.Anchor.Address())
	case publicTable != nil && len(public) == 0, len(privateTables) > 0 && len(private) == 0:
		return nil, Declinef("%s has a route table without subnets, and the module makes one only for subnets", c.Anchor.Address())
	}
	for _, nat := range nats {
		switch {
		case mustAttr(nat.r, "subnet_id") == nil:
			return nil, Declinef("%s is a regional NAT gateway, in no subnet, and the module makes zonal ones", nat.Address())
		case !slices.ContainsFunc(privateTables, func(t *vpcPart) bool { return natOf[t.Address()] == nat }):
			return nil, Declinef("%s has a NAT gateway no private route table routes to, and the module routes the private subnets through it", c.Anchor.Address())
		case len(eips) == 0:
			return nil, Declinef("%s has no Elastic IP of its own", nat.Address())
		}
	}

	// The module puts a single NAT gateway in the first public subnet, or
	// one in each public subnet, in order, and the subnets of each kind in
	// the zones of azs, in order.
	sortSubnets(public)
	switch {
	case len(nats) == 1:
		i := slices.IndexFunc(public, func(s *vpcSubnet) bool { return nats[0].r.RefersTo("subnet_id", s.Resource) })
		if i < 0 {
			return nil, Declinef("%s isn't in a public subnet of %s", nats[0].Address(), c.Anchor.Address())
		}
		first := public[i]
		public = append([]*vpcSubnet{first}, slices.Delete(public, i, i+1)...)
	case len(nats) > 1:
		// natsByZone has the NAT gateway of each public subnet, in order.
		natsByZone := make([]*vpcPart, 0, len(nats))
		for _, s := range public {
			i := slices.IndexFunc(nats, func(n *vpcPart) bool { return n.r.RefersTo("subnet_id", s.Resource) })
			if i < 0 {
				return nil, Declinef("%s has no NAT gateway, and with one in each zone the module makes one in each public subnet", s.Address())
			}
			natsByZone = append(natsByZone, nats[i])
		}
		for _, nat := range nats {
			if !slices.Contains(natsByZone, nat) {
				return nil, Declinef("%s isn't in a public subnet of %s, or shares it with another", nat.Address(), c.Anchor.Address())
			}
		}
		nats = natsByZone
	}
	// eipOf has, by NAT gateway, its Elastic IP.
	eipOf := map[string]*vpcPart{}
	for _, nat := range nats {
		i := slices.IndexFunc(eips, func(e *vpcPart) bool { return nat.r.RefersTo("allocation_id", e.Resource) })
		if i < 0 {
			return nil, Declinef("%s doesn't use an Elastic IP of %s", nat.Address(), c.Anchor.Address())
		}
		eipOf[nat.Address()] = eips[i]
	}
	for _, e := range eips {
		if !slices.ContainsFunc(nats, func(n *vpcPart) bool { return eipOf[n.Address()] == e }) {
			return nil, Declinef("%s isn't the Elastic IP of a NAT gateway of %s", e.Address(), c.Anchor.Address())
		}
	}
	azs := make([]string, 0, len(public))
	for _, s := range public {
		azs = append(azs, s.az)
	}
	if private, err = alignSubnets(c.Anchor, private, &azs); err != nil {
		return nil, err
	}
	// With a NAT gateway in each zone, each private subnet routes through
	// the one in its zone, the module's private route table of the same
	// index.
	privateRouteTables := privateTables
	if len(nats) > 1 {
		if len(private) != len(public) {
			return nil, Declinef("%s has %d public and %d private subnets, and with a NAT gateway in each zone the module pairs them by zone", c.Anchor.Address(), len(public), len(private))
		}
		privateRouteTables = make([]*vpcPart, 0, len(private))
		for i, s := range private {
			if natOf[s.table.Address()] != nats[i] {
				return nil, Declinef("%s doesn't route through the NAT gateway in its zone, and the module routes each private subnet through the one in its zone", s.Address())
			}
			privateRouteTables = append(privateRouteTables, s.table)
		}
	}
	call.Set("azs", hclwrite.TokensForValue(stringList(azs)))

	if len(public) > 0 {
		if err := vpcSubnets(call, c.Anchor, "public", public, []*vpcPart{publicTable}, vpc.name); err != nil {
			return nil, err
		}
		call.Imports = append(call.Imports, Import{To: "aws_route.public_internet_gateway[0]", Of: publicTable.Address(), Suffix: "_0.0.0.0/0"})
		if igw.name != vpc.name {
			return nil, Declinef("%s isn't named %q, as the module names it", igw.Address(), vpc.name)
		}
		call.Addresses[igw.Address()] = "aws_internet_gateway.this[0]"
		call.Outputs[igw.Address()] = map[string]string{"arn": "igw_arn", "id": "igw_id"}
		if err := igw.r.Done(); err != nil {
			return nil, err
		}
	}
	if len(private) > 0 {
		if err := vpcSubnets(call, c.Anchor, "private", private, privateRouteTables, vpc.name); err != nil {
			return nil, err
		}
		for i, t := range privateRouteTables {
			call.Imports = append(call.Imports, Import{To: fmt.Sprintf("aws_route.private_nat_gateway[%d]", i), Of: t.Address(), Suffix: "_0.0.0.0/0"})
		}
		call.Set("enable_nat_gateway", hclwrite.TokensForValue(cty.True))
		if len(nats) > 1 {
			call.Set("one_nat_gateway_per_az", hclwrite.TokensForValue(cty.True))
		} else {
			call.Set("single_nat_gateway", hclwrite.TokensForValue(cty.True))
		}
		for i, nat := range nats {
			if err := vpcNAT(call, c.Anchor, i, nat, eipOf[nat.Address()], vpc.name+"-"+azs[i]); err != nil {
				return nil, err
			}
		}
	}

	parts := slices.Concat([]*vpcPart{vpc, igw, publicTable}, nats, eips, privateTables, subnetParts(subnets))
	if err := vpcTags(call, parts, map[string][]*vpcPart{
		"vpc_tags":                 {vpc},
		"igw_tags":                 {igw},
		"nat_gateway_tags":         nats,
		"nat_eip_tags":             eips,
		"public_route_table_tags":  {publicTable},
		"private_route_table_tags": privateTables,
		"public_subnet_tags":       subnetParts(public),
		"private_subnet_tags":      subnetParts(private),
	}); err != nil {
		return nil, err
	}
	return call, nil
}

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

// sortSubnets orders subnets by zone, then CIDR block.
func sortSubnets(subnets []*vpcSubnet) {
	slices.SortFunc(subnets, func(a, b *vpcSubnet) int {
		return cmp.Or(strings.Compare(a.az, b.az), strings.Compare(a.cidrKey, b.cidrKey), strings.Compare(a.Address(), b.Address()))
	})
}

// alignSubnets orders private subnets so that each is in the zone azs has
// at its index, as the module places them, and extends azs with the zones
// of those beyond it.
func alignSubnets(vpc Resource, private []*vpcSubnet, azs *[]string) ([]*vpcSubnet, error) {
	sortSubnets(private)
	left := slices.Clone(private)
	aligned := make([]*vpcSubnet, 0, len(private))
	for i := range private {
		if i >= len(*azs) {
			aligned = append(aligned, left...)
			for _, s := range left {
				*azs = append(*azs, s.az)
			}
			break
		}
		j := slices.IndexFunc(left, func(s *vpcSubnet) bool { return s.az == (*azs)[i] })
		if j < 0 {
			return nil, Declinef("%s has no private subnet in %s for its public subnet there, and the module pairs them by zone", vpc.Address(), (*azs)[i])
		}
		aligned = append(aligned, left[j])
		left = slices.Delete(left, j, j+1)
	}
	return aligned, nil
}

// vpcSubnets maps the subnets of a kind, public or private, their route
// tables and associations: one table they share, named <name>-<suffix>,
// or, with a NAT gateway in each zone, one for each subnet, named
// <name>-<suffix>-<zone>.
func vpcSubnets(call *Call, vpc Resource, kind string, subnets []*vpcSubnet, tables []*vpcPart, name string) error {
	suffix := ""
	for i, table := range tables {
		s, ok := strings.CutPrefix(table.name, name+"-")
		if len(tables) > 1 {
			var inZone bool
			s, inZone = strings.CutSuffix(s, "-"+subnets[i].az)
			if !ok || !inZone || s == "" || (i > 0 && s != suffix) {
				return Declinef("%s isn't named %s-<suffix>-%s, as the module names it", table.Address(), name, subnets[i].az)
			}
		}
		if !ok || s == "" {
			return Declinef("%s isn't named %s-<suffix>, as the module names it", table.Address(), name)
		}
		suffix = s
	}
	call.Set(kind+"_subnet_suffix", hclwrite.TokensForValue(cty.StringVal(suffix)))
	for i, table := range tables {
		call.Addresses[table.Address()] = fmt.Sprintf("aws_route_table.%s[%d]", kind, i)
		call.Outputs[table.Address()] = map[string]string{"id": fmt.Sprintf("%s_route_table_ids[%d]", kind, i)}
		if err := table.r.Done(); err != nil {
			return err
		}
	}
	var cidrs []hclwrite.Tokens
	var names []string
	var mapPublicIP hclwrite.Tokens
	for i, s := range subnets {
		cidrs = append(cidrs, s.cidr)
		names = append(names, s.name)
		if kind == "public" {
			value, ok := s.r.Attr("map_public_ip_on_launch")
			if !ok {
				value = hclwrite.TokensForValue(cty.False)
			}
			if i > 0 && !sameTokens(value, mapPublicIP) {
				return Declinef("%s's public subnets differ in map_public_ip_on_launch, and the module sets it for all of them", vpc.Address())
			}
			mapPublicIP = value
		} else {
			s.r.IgnoreDefault("map_public_ip_on_launch", cty.False)
		}
		if err := s.r.Done(); err != nil {
			return err
		}
		call.Addresses[s.Address()] = fmt.Sprintf("aws_subnet.%s[%d]", kind, i)
		call.Outputs[s.Address()] = map[string]string{
			"arn":        fmt.Sprintf("%s_subnet_arns[%d]", kind, i),
			"cidr_block": fmt.Sprintf("%s_subnets_cidr_blocks[%d]", kind, i),
			"id":         fmt.Sprintf("%s_subnets[%d]", kind, i),
		}
		call.Addresses[s.association.Address()] = fmt.Sprintf("aws_route_table_association.%s[%d]", kind, i)
		call.Outputs[s.association.Address()] = map[string]string{"id": fmt.Sprintf("%s_route_table_association_ids[%d]", kind, i)}
	}
	call.Set(kind+"_subnets", hclwrite.TokensForTuple(cidrs))
	// The module names each subnet <name>-<suffix>-<zone> unless told
	// otherwise.
	if !slices.EqualFunc(subnets, names, func(s *vpcSubnet, n string) bool { return n == name+"-"+suffix+"-"+s.az }) {
		call.Set(kind+"_subnet_names", hclwrite.TokensForValue(stringList(names)))
	}
	if mapPublicIP != nil {
		call.Set("map_public_ip_on_launch", mapPublicIP)
	}
	return nil
}

// vpcNAT maps the module's NAT gateway of index i, which private subnets
// route through, and its Elastic IP, both of which the module names after
// the VPC and the gateway's zone.
func vpcNAT(call *Call, vpc Resource, i int, nat, eip *vpcPart, name string) error {
	for _, p := range []*vpcPart{nat, eip} {
		if p.name != name {
			return Declinef("%s isn't named %q, as the module names it", p.Address(), name)
		}
	}
	nat.r.IgnoreDefault("connectivity_type", cty.StringVal("public"))
	// A zonal gateway, in its subnet's VPC, as the module makes them;
	// AWS reports both.
	nat.r.IgnoreDefault("availability_mode", cty.StringVal("zonal"))
	if _, set := nat.r.Attr("vpc_id"); set && !nat.r.RefersTo("vpc_id", vpc) {
		return Declinef("%s isn't in %s", nat.Address(), vpc.Address())
	}
	// Chosen by AWS when not set, as the module doesn't.
	nat.r.Ignore("private_ip")
	for _, arg := range []string{"secondary_allocation_ids", "secondary_private_ip_addresses"} {
		if value, ok := nat.r.Attr(arg); ok && !emptyList(value) {
			return Declinef("%s has %s, and the module can't set them", nat.Address(), arg)
		}
	}
	nat.r.IgnoreDefault("secondary_private_ip_address_count", cty.Zero)
	// Left out of generated configuration at its default, vpc.
	if tokens, set := eip.r.Attr("domain"); set {
		if domain, ok := literalString(tokens); !ok || domain != "vpc" {
			return Declinef("%s isn't for use in a VPC", eip.Address())
		}
	}
	// Computed: the address's pool, and the gateway's network interface,
	// which AWS attaches it to.
	eip.r.Ignore("network_border_group", "public_ipv4_pool", "network_interface")
	// OpenTofu writes "" for these computed arguments where Terraform
	// leaves them out: the address is on no instance, from no IPAM pool.
	eip.r.IgnoreDefault("instance", cty.StringVal(""))
	eip.r.IgnoreDefault("ipam_pool_id", cty.StringVal(""))
	for _, p := range []*vpcPart{nat, eip} {
		if err := p.r.Done(); err != nil {
			return err
		}
	}
	call.Addresses[nat.Address()] = fmt.Sprintf("aws_nat_gateway.this[%d]", i)
	call.Outputs[nat.Address()] = map[string]string{"id": fmt.Sprintf("natgw_ids[%d]", i), "network_interface_id": fmt.Sprintf("natgw_interface_ids[%d]", i)}
	call.Addresses[eip.Address()] = fmt.Sprintf("aws_eip.nat[%d]", i)
	call.Outputs[eip.Address()] = map[string]string{"id": fmt.Sprintf("nat_ids[%d]", i), "public_ip": fmt.Sprintf("nat_public_ips[%d]", i)}
	return nil
}

// vpcTags sets tags to the tags every resource has, and each input of
// byInput to the other tags of its resources, which must be the same.
// Resources whose tags are merged over a local must all be, over the same
// one, which tags then refers to.
func vpcTags(call *Call, parts []*vpcPart, byInput map[string][]*vpcPart) error {
	var common map[string]string
	var first *vpcPart
	for _, p := range parts {
		if p == nil {
			continue
		}
		if first == nil {
			first = p
			common = maps.Clone(p.tags)
			continue
		}
		if !sameTokens(p.base, first.base) {
			return Declinef("%s and %s don't merge their tags over the same local, and the module sets tags for both", first.Address(), p.Address())
		}
		maps.DeleteFunc(common, func(k, v string) bool { return p.tags[k] != v })
	}
	switch {
	case first != nil && first.base != nil && len(common) > 0:
		call.Set("tags", hclwrite.TokensForFunctionCall("merge", first.base, hclwrite.TokensForValue(stringMap(common))))
	case first != nil && first.base != nil:
		call.Set("tags", first.base)
	case len(common) > 0:
		call.Set("tags", hclwrite.TokensForValue(stringMap(common)))
	}
	for _, input := range slices.Sorted(maps.Keys(byInput)) {
		var extra map[string]string
		for i, p := range byInput[input] {
			if p == nil {
				continue
			}
			own := maps.Clone(p.tags)
			maps.DeleteFunc(own, func(k, v string) bool { return common[k] == v })
			if i > 0 && !maps.Equal(own, extra) {
				return Declinef("%s and %s have different tags, and the module sets %s for both", byInput[input][0].Address(), p.Address(), input)
			}
			extra = own
		}
		if len(extra) > 0 {
			call.Set(input, hclwrite.TokensForValue(stringMap(extra)))
		}
	}
	return nil
}

func subnetParts(subnets []*vpcSubnet) []*vpcPart {
	parts := make([]*vpcPart, 0, len(subnets))
	for _, s := range subnets {
		parts = append(parts, &s.vpcPart)
	}
	return parts
}

func stringMap(m map[string]string) cty.Value {
	values := make(map[string]cty.Value, len(m))
	for k, v := range m {
		values[k] = cty.StringVal(v)
	}
	return cty.MapVal(values)
}
