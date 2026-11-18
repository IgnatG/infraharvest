// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

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
		maps.DeleteFunc(common, func(k, v string) bool {
			pv, ok := p.tags[k]
			return !ok || pv != v
		})
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
			maps.DeleteFunc(own, func(k, v string) bool {
				cv, ok := common[k]
				return ok && cv == v
			})
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
