// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"fmt"
	"slices"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// awsVPC maps a VPC, its subnets, internet gateway and route tables, the
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
var awsVPC = Adapter{
	Source:  "terraform-aws-modules/vpc/aws",
	Version: "6.8.0",
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
	// The module sets these from the inputs as they are, and each
	// resource's tags by merging tags and then its own input, whose keys
	// win (main.tf): a tag one of them changes goes into its own input.
	Inverse: map[string]string{
		"aws_eip.nat.tags":                                  "nat_eip_tags",
		"aws_internet_gateway.this.tags":                    "igw_tags",
		"aws_nat_gateway.this.tags":                         "nat_gateway_tags",
		"aws_route_table.private.tags":                      "private_route_table_tags",
		"aws_route_table.public.tags":                       "public_route_table_tags",
		"aws_subnet.private.tags":                           "private_subnet_tags",
		"aws_subnet.public.map_public_ip_on_launch":         "map_public_ip_on_launch",
		"aws_subnet.public.tags":                            "public_subnet_tags",
		"aws_vpc.this.enable_dns_hostnames":                 "enable_dns_hostnames",
		"aws_vpc.this.enable_dns_support":                   "enable_dns_support",
		"aws_vpc.this.enable_network_address_usage_metrics": "enable_network_address_usage_metrics",
		"aws_vpc.this.instance_tenancy":                     "instance_tenancy",
		"aws_vpc.this.tags":                                 "vpc_tags",
	},
}

// defaultRouteIdentity is the identity of the route to 0.0.0.0/0 the
// module adds to a route table, where the route table is imported by its
// identity: the route table's account, region and ID, and the
// destination.
var defaultRouteIdentity = map[string]string{
	"account_id":             "{account_id}",
	"region":                 "{region}",
	"route_table_id":         "{id}",
	"destination_cidr_block": "0.0.0.0/0",
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

// vpcMembers are the members of a VPC cluster, read.
type vpcMembers struct {
	igw                *vpcPart
	nats, eips, tables []*vpcPart
	associations       []*vpcPart
	subnets            []*vpcSubnet
}

// vpcRouting is how a VPC's route tables route: by route table, its kind,
// and, by private route table, the NAT gateway it routes to; the one public
// route table, and the private ones, a single one or one for each NAT
// gateway.
type vpcRouting struct {
	kinds         map[string]vpcRoute
	natOf         map[string]*vpcPart
	publicTable   *vpcPart
	privateTables []*vpcPart
}

func mapVPC(c Cluster) (*Call, error) {
	vpc, err := readVPCPart(c.Anchor)
	if err != nil {
		return nil, err
	}
	call, err := vpcCall(c.Anchor, vpc)
	if err != nil {
		return nil, err
	}
	m, err := readVPCMembers(call, c)
	if err != nil {
		return nil, err
	}
	routing, err := routeVPCTables(c.Anchor, m)
	if err != nil {
		return nil, err
	}
	if err := associateVPCSubnets(c.Anchor, m, routing.kinds); err != nil {
		return nil, err
	}
	public, private, err := splitVPCSubnets(c.Anchor, m, routing)
	if err != nil {
		return nil, err
	}
	if err := checkVPCNATs(c.Anchor, m, routing); err != nil {
		return nil, err
	}
	// The module puts a single NAT gateway in the first public subnet, or
	// one in each public subnet, in order, and the subnets of each kind in
	// the zones of azs, in order.
	public, nats, err := orderVPCNATs(c.Anchor, public, m.nats)
	if err != nil {
		return nil, err
	}
	eipOf, err := vpcEIPs(c.Anchor, nats, m.eips)
	if err != nil {
		return nil, err
	}
	azs := make([]string, 0, len(public))
	for _, s := range public {
		azs = append(azs, s.az)
	}
	if private, err = alignSubnets(c.Anchor, private, &azs); err != nil {
		return nil, err
	}
	privateRouteTables, err := vpcPrivateRouteTables(c.Anchor, public, private, nats, routing)
	if err != nil {
		return nil, err
	}
	call.Set("azs", hclwrite.TokensForValue(stringList(azs)))
	if len(public) > 0 {
		if err := setVPCPublic(call, c.Anchor, public, routing.publicTable, m.igw, vpc.name); err != nil {
			return nil, err
		}
	}
	if len(private) > 0 {
		if err := setVPCPrivate(call, c.Anchor, private, privateRouteTables, nats, eipOf, azs, vpc.name); err != nil {
			return nil, err
		}
	}
	parts := slices.Concat([]*vpcPart{vpc, m.igw, routing.publicTable}, nats, m.eips, routing.privateTables, subnetParts(m.subnets))
	if err := vpcTags(call, parts, map[string][]*vpcPart{
		"vpc_tags":                 {vpc},
		"igw_tags":                 {m.igw},
		"nat_gateway_tags":         nats,
		"nat_eip_tags":             m.eips,
		"public_route_table_tags":  {routing.publicTable},
		"private_route_table_tags": routing.privateTables,
		"public_subnet_tags":       subnetParts(public),
		"private_subnet_tags":      subnetParts(private),
	}); err != nil {
		return nil, err
	}
	return call, nil
}

// vpcCall starts the call with the VPC's own arguments, its addresses and
// outputs, and the module's management of the VPC's defaults turned off.
func vpcCall(anchor Resource, vpc *vpcPart) (*Call, error) {
	call := &Call{
		Addresses: map[string]string{anchor.Address(): "aws_vpc.this[0]"},
		Outputs: map[string]map[string]string{anchor.Address(): {
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
	return call, nil
}

// readVPCMembers reads the cluster's members. Of its route tables it reads
// those of the subnets; the others, such as the VPC's main route table,
// which the module leaves alone, it leaves in the root.
func readVPCMembers(call *Call, c Cluster) (vpcMembers, error) {
	var m vpcMembers
	var tableResources []Resource
	for _, r := range c.Members {
		switch r.Type {
		case "aws_subnet":
			s, err := readVPCSubnet(r, c.Anchor)
			if err != nil {
				return m, err
			}
			m.subnets = append(m.subnets, s)
			continue
		case "aws_route_table_association":
			m.associations = append(m.associations, &vpcPart{Resource: r, r: Read(r.Address(), r.Body)})
			continue
		case "aws_route_table":
			tableResources = append(tableResources, r)
			continue
		}
		p, err := readVPCPart(r)
		if err != nil {
			return m, err
		}
		switch r.Type {
		case "aws_internet_gateway":
			if !p.r.RefersTo("vpc_id", c.Anchor) {
				return m, Declinef("%s isn't attached to %s", r.Address(), c.Anchor.Address())
			}
			m.igw = p
		case "aws_nat_gateway":
			m.nats = append(m.nats, p)
		case "aws_eip":
			m.eips = append(m.eips, p)
		}
	}
	for _, t := range tableResources {
		if !slices.ContainsFunc(m.associations, func(a *vpcPart) bool { return a.r.RefersTo("route_table_id", t) }) {
			call.Left = append(call.Left, t.Address())
			continue
		}
		p, err := readVPCPart(t)
		if err != nil {
			return m, err
		}
		if !p.r.RefersTo("vpc_id", c.Anchor) {
			return m, Declinef("%s isn't in %s", t.Address(), c.Anchor.Address())
		}
		m.tables = append(m.tables, p)
	}
	return m, nil
}

// routeVPCTables sorts the route tables into the public one and the
// private ones, declining a VPC with two that route the same way.
func routeVPCTables(vpc Resource, m vpcMembers) (vpcRouting, error) {
	routing := vpcRouting{kinds: map[string]vpcRoute{}, natOf: map[string]*vpcPart{}}
	for _, t := range m.tables {
		kind, nat, err := vpcRouteKind(t, m.igw, m.nats)
		if err != nil {
			return routing, err
		}
		routing.kinds[t.Address()] = kind
		switch {
		case kind == publicRoute && routing.publicTable == nil:
			routing.publicTable = t
		case kind == privateRoute && !slices.ContainsFunc(routing.privateTables, func(p *vpcPart) bool { return routing.natOf[p.Address()] == nat }):
			routing.privateTables = append(routing.privateTables, t)
			routing.natOf[t.Address()] = nat
		default:
			return routing, Declinef("%s has more than one route table that routes the same way, and the module makes one per gateway", vpc.Address())
		}
	}
	return routing, nil
}

// associateVPCSubnets gives each subnet the route table an association
// associates it with, and whether that makes it public.
func associateVPCSubnets(vpc Resource, m vpcMembers, kinds map[string]vpcRoute) error {
	for _, a := range m.associations {
		var subnet *vpcSubnet
		for _, s := range m.subnets {
			if a.r.RefersTo("subnet_id", s.Resource) {
				subnet = s
			}
		}
		var table *vpcPart
		for _, t := range m.tables {
			if a.r.RefersTo("route_table_id", t.Resource) {
				table = t
			}
		}
		if subnet == nil || table == nil {
			return Declinef("%s doesn't associate a subnet of %s with one of its route tables", a.Address(), vpc.Address())
		}
		if err := a.r.Done(); err != nil {
			return err
		}
		subnet.association = a
		subnet.table = table
		subnet.public = kinds[table.Address()] == publicRoute
	}
	return nil
}

// splitVPCSubnets splits the subnets into public and private ones,
// declining gateways and route tables the module would make only for
// subnets of a kind the VPC lacks.
func splitVPCSubnets(vpc Resource, m vpcMembers, routing vpcRouting) (public, private []*vpcSubnet, err error) {
	for _, s := range m.subnets {
		if s.association == nil {
			return nil, nil, Declinef("%s has no route table of its own, and the module associates every subnet with one", s.Address())
		}
		if s.public {
			public = append(public, s)
		} else {
			private = append(private, s)
		}
	}
	switch {
	case m.igw != nil && len(public) == 0:
		return nil, nil, Declinef("%s has an internet gateway but no public subnet, and the module makes one only for public subnets", vpc.Address())
	case routing.publicTable != nil && len(public) == 0, len(routing.privateTables) > 0 && len(private) == 0:
		return nil, nil, Declinef("%s has a route table without subnets, and the module makes one only for subnets", vpc.Address())
	}
	return public, private, nil
}

// checkVPCNATs declines NAT gateways the module wouldn't make: regional
// ones, ones no private route table routes to, and ones without an
// Elastic IP.
func checkVPCNATs(vpc Resource, m vpcMembers, routing vpcRouting) error {
	for _, nat := range m.nats {
		switch {
		case mustAttr(nat.r, "subnet_id") == nil:
			return Declinef("%s is a regional NAT gateway, in no subnet, and the module makes zonal ones", nat.Address())
		case !slices.ContainsFunc(routing.privateTables, func(t *vpcPart) bool { return routing.natOf[t.Address()] == nat }):
			return Declinef("%s has a NAT gateway no private route table routes to, and the module routes the private subnets through it", vpc.Address())
		case len(m.eips) == 0:
			return Declinef("%s has no Elastic IP of its own", nat.Address())
		}
	}
	return nil
}

// orderVPCNATs sorts the public subnets and the NAT gateways as the module
// places them: a single NAT gateway's subnet first, or with one in each
// zone, the NAT gateways in the order of their subnets.
func orderVPCNATs(vpc Resource, public []*vpcSubnet, nats []*vpcPart) ([]*vpcSubnet, []*vpcPart, error) {
	sortSubnets(public)
	switch {
	case len(nats) == 1:
		i := slices.IndexFunc(public, func(s *vpcSubnet) bool { return nats[0].r.RefersTo("subnet_id", s.Resource) })
		if i < 0 {
			return nil, nil, Declinef("%s isn't in a public subnet of %s", nats[0].Address(), vpc.Address())
		}
		first := public[i]
		public = append([]*vpcSubnet{first}, slices.Delete(public, i, i+1)...)
	case len(nats) > 1:
		// natsByZone has the NAT gateway of each public subnet, in order.
		natsByZone := make([]*vpcPart, 0, len(nats))
		for _, s := range public {
			i := slices.IndexFunc(nats, func(n *vpcPart) bool { return n.r.RefersTo("subnet_id", s.Resource) })
			if i < 0 {
				return nil, nil, Declinef("%s has no NAT gateway, and with one in each zone the module makes one in each public subnet", s.Address())
			}
			natsByZone = append(natsByZone, nats[i])
		}
		for _, nat := range nats {
			if !slices.Contains(natsByZone, nat) {
				return nil, nil, Declinef("%s isn't in a public subnet of %s, or shares it with another", nat.Address(), vpc.Address())
			}
		}
		nats = natsByZone
	}
	return public, nats, nil
}

// vpcEIPs returns, by NAT gateway, its Elastic IP, declining Elastic IPs
// that are no NAT gateway's.
func vpcEIPs(vpc Resource, nats, eips []*vpcPart) (map[string]*vpcPart, error) {
	eipOf := map[string]*vpcPart{}
	for _, nat := range nats {
		i := slices.IndexFunc(eips, func(e *vpcPart) bool { return nat.r.RefersTo("allocation_id", e.Resource) })
		if i < 0 {
			return nil, Declinef("%s doesn't use an Elastic IP of %s", nat.Address(), vpc.Address())
		}
		eipOf[nat.Address()] = eips[i]
	}
	for _, e := range eips {
		if !slices.ContainsFunc(nats, func(n *vpcPart) bool { return eipOf[n.Address()] == e }) {
			return nil, Declinef("%s isn't the Elastic IP of a NAT gateway of %s", e.Address(), vpc.Address())
		}
	}
	return eipOf, nil
}

// vpcPrivateRouteTables returns the private route tables in the module's
// order. With a NAT gateway in each zone, each private subnet routes
// through the one in its zone, the module's private route table of the
// same index.
func vpcPrivateRouteTables(vpc Resource, public, private []*vpcSubnet, nats []*vpcPart, routing vpcRouting) ([]*vpcPart, error) {
	if len(nats) <= 1 {
		return routing.privateTables, nil
	}
	if len(private) != len(public) {
		return nil, Declinef("%s has %d public and %d private subnets, and with a NAT gateway in each zone the module pairs them by zone", vpc.Address(), len(public), len(private))
	}
	tables := make([]*vpcPart, 0, len(private))
	for i, s := range private {
		if routing.natOf[s.table.Address()] != nats[i] {
			return nil, Declinef("%s doesn't route through the NAT gateway in its zone, and the module routes each private subnet through the one in its zone", s.Address())
		}
		tables = append(tables, s.table)
	}
	return tables, nil
}

// setVPCPublic maps the public subnets, their route table with its
// default route, and the internet gateway.
func setVPCPublic(call *Call, vpc Resource, public []*vpcSubnet, table, igw *vpcPart, name string) error {
	if err := vpcSubnets(call, vpc, "public", public, []*vpcPart{table}, name); err != nil {
		return err
	}
	call.Imports = append(call.Imports, Import{To: "aws_route.public_internet_gateway[0]", Of: table.Address(), Suffix: "_0.0.0.0/0", Identity: defaultRouteIdentity})
	if igw.name != name {
		return Declinef("%s isn't named %q, as the module names it", igw.Address(), name)
	}
	call.Addresses[igw.Address()] = "aws_internet_gateway.this[0]"
	call.Outputs[igw.Address()] = map[string]string{"arn": "igw_arn", "id": "igw_id"}
	return igw.r.Done()
}

// setVPCPrivate maps the private subnets, their route tables with their
// default routes, and the NAT gateways with their Elastic IPs.
func setVPCPrivate(call *Call, vpc Resource, private []*vpcSubnet, tables, nats []*vpcPart, eipOf map[string]*vpcPart, azs []string, name string) error {
	if err := vpcSubnets(call, vpc, "private", private, tables, name); err != nil {
		return err
	}
	for i, t := range tables {
		call.Imports = append(call.Imports, Import{To: fmt.Sprintf("aws_route.private_nat_gateway[%d]", i), Of: t.Address(), Suffix: "_0.0.0.0/0", Identity: defaultRouteIdentity})
	}
	call.Set("enable_nat_gateway", hclwrite.TokensForValue(cty.True))
	if len(nats) > 1 {
		call.Set("one_nat_gateway_per_az", hclwrite.TokensForValue(cty.True))
	} else {
		call.Set("single_nat_gateway", hclwrite.TokensForValue(cty.True))
	}
	for i, nat := range nats {
		if err := vpcNAT(call, vpc, i, nat, eipOf[nat.Address()], name+"-"+azs[i]); err != nil {
			return err
		}
	}
	return nil
}
